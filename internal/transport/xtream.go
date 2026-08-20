package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"vodmanager/internal/ingest"
	"vodmanager/internal/sources"
	"vodmanager/internal/sources/xtream"
)

// XtreamProvider fala com uma API compatível com Xtream.
//
// Implementação própria, escrita a partir do formato observável do protocolo. Cobre
// apenas o subconjunto de VOD: live TV está fora de escopo.
type XtreamProvider struct{}

// NewXtreamProvider cria o provider.
func NewXtreamProvider() *XtreamProvider { return &XtreamProvider{} }

// Kind identifica o tipo de fonte.
func (p *XtreamProvider) Kind() string { return "xtream" }

// Capabilities descreve o que a API oferece.
func (p *XtreamProvider) Capabilities() sources.Capabilities {
	return sources.Capabilities{
		HasCategories:       true,
		HasSeries:           true,
		HasStableIDs:        true,
		SupportsIncremental: true,
		ProvidesTMDBID:      true,
	}
}

// FetchCatalog percorre categorias, filmes e séries.
//
// O ponto caro é get_series_info: uma requisição POR SÉRIE. A política aprovada
// (docs/07 §6.1) é buscar detalhe apenas de séries novas ou cuja entrada na listagem
// mudou de digest. Isso transforma "3.000 requisições por sincronização" em
// "algumas dezenas".
func (p *XtreamProvider) FetchCatalog(ctx context.Context, cfg sources.Config, prev sources.State, emit func(ingest.RawItem) error) (sources.Result, error) {
	client := NewClient(cfg.Timeout, cfg.RequestBudget, cfg.UserAgent)
	result := sources.Result{
		State: sources.State{
			ProviderKind: p.Kind(),
			Version:      1,
			ItemDigests:  map[string]string{},
		},
	}
	finish := func(err error) (sources.Result, error) {
		result.Requests = client.Used()
		result.Partial = result.Partial || client.Exceeded()
		result.State.FetchedAt = time.Now()
		return result, err
	}

	// --- Categorias de filmes -------------------------------------------------
	catVOD, err := p.categorias(ctx, client, cfg, "get_vod_categories", "movie")
	if err != nil {
		return finish(err)
	}
	result.Categories = append(result.Categories, catVOD...)
	mapaVOD := porID(catVOD)

	// --- Filmes ---------------------------------------------------------------
	dados, err := client.Get(ctx, p.apiURL(cfg, "get_vod_streams"))
	if err != nil {
		if isBudget(err) {
			result.Partial = true
			return finish(nil)
		}
		return finish(err)
	}
	if _, err := xtream.ParseVODStreams(dados, mapaVOD, time.Now(), emit); err != nil {
		return finish(err)
	}

	// --- Categorias de séries -------------------------------------------------
	catSeries, err := p.categorias(ctx, client, cfg, "get_series_categories", "series")
	if err != nil && !isBudget(err) {
		return finish(err)
	}
	result.Categories = append(result.Categories, catSeries...)
	mapaSeries := porID(catSeries)

	// --- Séries ---------------------------------------------------------------
	dadosSeries, err := client.Get(ctx, p.apiURL(cfg, "get_series"))
	if err != nil {
		if isBudget(err) {
			result.Partial = true
			return finish(nil)
		}
		return finish(err)
	}
	lista, err := xtream.ParseSeriesList(dadosSeries, mapaSeries)
	if err != nil {
		return finish(err)
	}

	// --- Episódios, com o incremental que protege a fonte ---------------------
	for _, serie := range lista {
		result.State.ItemDigests[serie.ID] = serie.Digest

		anterior, jaVista := prev.ItemDigests[serie.ID]
		if jaVista && anterior == serie.Digest {
			// A entrada da série não mudou: os episódios dela também não mudaram.
			// Esta é a única linha que separa uma sincronização barata de uma que
			// martela a fonte com milhares de requisições.
			result.SkippedDetails++
			continue
		}

		if client.Remaining() <= 0 {
			result.Partial = true
			break
		}

		detalhe, err := client.Get(ctx, p.apiURL(cfg, "get_series_info")+"&series_id="+queryEscape(serie.ID))
		if err != nil {
			if isBudget(err) {
				result.Partial = true
				break
			}
			// Uma série que falha não derruba a sincronização inteira: o digest dela
			// não é gravado, então a próxima run tenta de novo.
			delete(result.State.ItemDigests, serie.ID)
			continue
		}
		if _, err := xtream.ParseSeriesInfo(detalhe, serie, time.Now(), emit); err != nil {
			delete(result.State.ItemDigests, serie.ID)
			continue
		}
	}

	return finish(nil)
}

// ResolveStreamURL compõe a URL de mídia com as credenciais da fonte.
//
// Esta é a única função do sistema que junta credencial e URL. Ela NUNCA é chamada
// durante a sincronização — só quando um cliente pede o vídeo.
//
// Formato do protocolo: {base}/movie/{usuario}/{senha}/{id}.{ext}
func (p *XtreamProvider) ResolveStreamURL(cfg sources.Config, target sources.StreamTarget) (string, error) {
	if target.OriginURL != "" {
		return target.OriginURL, nil
	}
	if target.StreamRef == nil {
		return "", fmt.Errorf("xtream: variante sem stream_ref nem URL")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return "", fmt.Errorf("xtream: a fonte %d não tem credenciais configuradas", cfg.SourceID)
	}

	var segmento string
	switch target.StreamRef.Kind {
	case ingest.StreamRefXtreamMovie:
		segmento = "movie"
	case ingest.StreamRefXtreamSeries:
		segmento = "series"
	default:
		return "", fmt.Errorf("xtream: tipo de referência desconhecido: %q", target.StreamRef.Kind)
	}

	ext := primeiroNaoVazio(target.StreamRef.Extension, target.ContainerExt, "mp4")
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")

	return fmt.Sprintf("%s/%s/%s/%s/%s.%s",
		base, segmento,
		pathEscape(cfg.Username), pathEscape(cfg.Password),
		pathEscape(target.StreamRef.ID), strings.TrimPrefix(ext, "."),
	), nil
}

// Probe confirma que a API responde e que as credenciais valem, sem tocar em vídeo.
func (p *XtreamProvider) Probe(ctx context.Context, cfg sources.Config) error {
	client := NewClient(cfg.Timeout, 1, cfg.UserAgent)
	dados, err := client.Get(ctx, p.apiURL(cfg, ""))
	if err != nil {
		return err
	}

	var resposta struct {
		UserInfo struct {
			Auth    json.RawMessage `json:"auth"`
			Status  string          `json:"status"`
			Message string          `json:"message"`
		} `json:"user_info"`
	}
	if err := json.Unmarshal(dados, &resposta); err != nil {
		return fmt.Errorf("xtream: resposta de autenticação inesperada")
	}
	// auth vem como 0/1 (número) ou "0"/"1" (string), dependendo do painel.
	auth := strings.Trim(string(resposta.UserInfo.Auth), `"`)
	if auth == "0" || auth == "" {
		if resposta.UserInfo.Message != "" {
			return fmt.Errorf("xtream: autenticação recusada pela fonte")
		}
		return fmt.Errorf("xtream: autenticação recusada pela fonte")
	}
	if st := strings.ToLower(resposta.UserInfo.Status); st != "" && st != "active" {
		return fmt.Errorf("xtream: a conta na fonte está com status %q", resposta.UserInfo.Status)
	}
	return nil
}

// --- auxiliares --------------------------------------------------------------

func (p *XtreamProvider) categorias(ctx context.Context, client *Client, cfg sources.Config, acao, tipo string) ([]sources.Category, error) {
	dados, err := client.Get(ctx, p.apiURL(cfg, acao))
	if err != nil {
		return nil, err
	}
	cats, err := xtream.ParseCategories(dados, tipo)
	if err != nil {
		// Categoria é acessório: uma fonte que não responde categorias ainda tem
		// catálogo utilizável, com os itens caindo em "sem categoria".
		return nil, nil
	}
	out := make([]sources.Category, 0, len(cats))
	for _, c := range cats {
		out = append(out, sources.Category{ExternalID: c.ID, Name: c.Name, ContentType: c.ContentType})
	}
	return out, nil
}

// apiURL monta a URL do player_api.php com as credenciais.
func (p *XtreamProvider) apiURL(cfg sources.Config, acao string) string {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	url := fmt.Sprintf("%s/player_api.php?username=%s&password=%s",
		base, queryEscape(cfg.Username), queryEscape(cfg.Password))
	if acao != "" {
		url += "&action=" + queryEscape(acao)
	}
	return url
}

func porID(cats []sources.Category) map[string]string {
	m := make(map[string]string, len(cats))
	for _, c := range cats {
		m[c.ExternalID] = c.Name
	}
	return m
}

func isBudget(err error) bool {
	return err != nil && strings.Contains(err.Error(), sources.ErrBudgetExceeded.Error())
}

func primeiroNaoVazio(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
