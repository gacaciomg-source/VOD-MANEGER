package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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
			// ExpDate é a informação que faltava: a fonte SEMPRE a envia, e ninguém a lia.
			//
			// Vem como texto com um timestamp unix, às vezes número, às vezes vazia ou nula
			// — daí o RawMessage. Uma conta sem vencimento manda vazio, e isso é legítimo.
			ExpDate json.RawMessage `json:"exp_date"`
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

	// O vencimento, que era a informação que faltava.
	//
	// Uma fonte vencida NÃO para de responder: ela aceita a conexão, devolve 200 e entrega,
	// no lugar de cada filme, mil e seiscentos bytes com a frase "sua lista está expirada".
	// Para o sistema tudo parecia normal; para quem assistia, todo conteúdo dela abria com
	// zero segundos, sem nada nos registros apontando a causa.
	//
	// Vazio é legítimo: contas sem prazo existem, e exigir a data recusaria fontes boas.
	if venc, ok := vencimentoXtream(resposta.UserInfo.ExpDate); ok && time.Now().After(venc) {
		return fmt.Errorf("xtream: a assinatura nesta fonte venceu em %s — "+
			"ela continua respondendo, mas entrega um aviso no lugar dos vídeos",
			venc.Format("02/01/2006"))
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

// vencimentoXtream interpreta o exp_date, que vem em três formatos diferentes.
//
// Texto com número (`"1735689600"`), número puro (`1735689600`), ou vazio/nulo. A variação
// não é descuido de um painel só: cada implementação de Xtream escolheu a sua, e um cliente
// que aceite apenas uma recusa metade das fontes do mercado.
//
// O segundo retorno é "havia data", e não "deu certo": ausência é o caso legítimo de uma
// conta sem prazo, e tratá-la como erro faria fontes boas parecerem vencidas.
func vencimentoXtream(bruto json.RawMessage) (time.Time, bool) {
	// Os espacos sao aparados DEPOIS das aspas tambem: " 1735689600 " tem espaco por
	// dentro delas, e aparar so por fora deixaria o numero incolavel.
	texto := strings.TrimSpace(strings.Trim(strings.TrimSpace(string(bruto)), `"`))
	if texto == "" || texto == "null" || texto == "0" {
		return time.Time{}, false
	}
	segundos, err := strconv.ParseInt(texto, 10, 64)
	if err != nil || segundos <= 0 {
		return time.Time{}, false
	}
	return time.Unix(segundos, 0), true
}

// Assinatura lê a validade da conta na fonte, sem tocar em vídeo.
//
// # Por que existe além do Probe
//
// O Probe responde sim ou não: dá para usar esta fonte agora? Esta responde outra coisa —
// ATÉ QUANDO dá. E é essa a pergunta que permite avisar antes, em vez de descobrir com os
// clientes reclamando de conteúdo que não abre.
//
// Uma fonte vencida não falha: ela aceita a conexão, responde 200 e entrega, no lugar de cada
// filme, um aviso de dois quilobytes. Sem esta leitura, nada no sistema distingue isso de uma
// operação normal.
func (p *XtreamProvider) Assinatura(ctx context.Context, cfg sources.Config) (sources.Assinatura, error) {
	client := NewClient(cfg.Timeout, 1, cfg.UserAgent)
	dados, err := client.Get(ctx, p.apiURL(cfg, ""))
	if err != nil {
		return sources.Assinatura{}, err
	}

	var resposta struct {
		UserInfo struct {
			Status  string          `json:"status"`
			ExpDate json.RawMessage `json:"exp_date"`
		} `json:"user_info"`
	}
	if err := json.Unmarshal(dados, &resposta); err != nil {
		return sources.Assinatura{}, fmt.Errorf("xtream: resposta de autenticação inesperada")
	}

	a := sources.Assinatura{Status: strings.TrimSpace(resposta.UserInfo.Status)}
	if venc, ok := vencimentoXtream(resposta.UserInfo.ExpDate); ok {
		a.Expira = &venc
		a.Vencida = time.Now().After(venc)
	}
	// O status também vence a conta, mesmo sem data: alguns painéis dizem "Expired" e não
	// mandam exp_date nenhum.
	if st := strings.ToLower(a.Status); st != "" && st != "active" {
		a.Vencida = true
	}
	return a, nil
}
