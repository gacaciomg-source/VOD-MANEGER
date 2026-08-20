package transport

import (
	"context"
	"fmt"
	"strings"
	"time"

	"vodmanager/internal/ingest"
	"vodmanager/internal/sources"
	"vodmanager/internal/sources/m3u"
)

// M3UProvider busca e interpreta uma playlist M3U.
type M3UProvider struct{}

// NewM3UProvider cria o provider.
func NewM3UProvider() *M3UProvider { return &M3UProvider{} }

// Kind identifica o tipo de fonte.
func (p *M3UProvider) Kind() string { return "m3u" }

// Capabilities descreve o que uma playlist M3U oferece.
//
// HasStableIDs é falso porque tvg-id é opcional e frequentemente ausente; quando existe,
// a normalização o usa como identidade. SupportsIncremental é verdadeiro num sentido
// limitado: comparamos o digest da playlist inteira, o que evita reprocessar quando nada
// mudou, mas não evita baixá-la.
func (p *M3UProvider) Capabilities() sources.Capabilities {
	return sources.Capabilities{
		HasCategories:       true, // via group-title
		HasSeries:           true, // via convenção no título
		HasStableIDs:        false,
		SupportsIncremental: true,
	}
}

// FetchCatalog baixa a playlist e emite um RawItem por entrada.
//
// A playlist é lida em STREAMING: o consumo de memória independe do tamanho do arquivo.
// Nenhuma URL de mídia é aberta — elas são copiadas como texto.
func (p *M3UProvider) FetchCatalog(ctx context.Context, cfg sources.Config, prev sources.State, emit func(ingest.RawItem) error) (sources.Result, error) {
	client := NewClient(cfg.Timeout, cfg.RequestBudget, cfg.UserAgent)
	result := sources.Result{State: sources.State{ProviderKind: p.Kind(), Version: 1}}

	url := p.playlistURL(cfg)
	body, err := client.Open(ctx, url)
	if err != nil {
		result.Requests = client.Used()
		result.Partial = client.Exceeded()
		return result, err
	}
	defer body.Close()

	// O digest da playlist é calculado enquanto ela é lida — sem segunda passagem e sem
	// manter o conteúdo em memória.
	hasher := newStreamHasher(body)

	categorias := map[string]bool{}
	stats, err := m3u.Parse(hasher, m3u.ParseOptions{FetchedAt: time.Now()}, func(item ingest.RawItem) error {
		if g := strings.TrimSpace(item.GroupTitle); g != "" && !categorias[g] {
			categorias[g] = true
			result.Categories = append(result.Categories, sources.Category{
				Name:        g,
				ContentType: "unknown", // o M3U não separa filme de série por categoria
			})
		}
		return emit(item)
	})
	result.Requests = client.Used()
	result.Partial = client.Exceeded()
	if err != nil {
		return result, err
	}

	result.State.CatalogDigest = hasher.Digest()
	result.State.FetchedAt = time.Now()
	result.State.ItemDigests = nil // o M3U não tem detalhe por item a pular

	// Se o conteúdo é byte a byte o mesmo da última vez, informamos ao orquestrador —
	// que pode encurtar o diff.
	if prev.CatalogDigest != "" && prev.CatalogDigest == result.State.CatalogDigest {
		result.SkippedDetails = stats.Items
	}
	return result, nil
}

// ResolveStreamURL devolve a URL que a própria playlist já forneceu.
//
// Para M3U não há composição com credencial: a URL veio pronta da fonte.
func (p *M3UProvider) ResolveStreamURL(_ sources.Config, target sources.StreamTarget) (string, error) {
	if target.OriginURL == "" {
		return "", fmt.Errorf("m3u: variante sem URL de origem")
	}
	return target.OriginURL, nil
}

// Probe verifica se a playlist responde, sem baixá-la inteira.
func (p *M3UProvider) Probe(ctx context.Context, cfg sources.Config) error {
	client := NewClient(cfg.Timeout, 1, cfg.UserAgent)
	body, err := client.Open(ctx, p.playlistURL(cfg))
	if err != nil {
		return err
	}
	defer body.Close()

	// Lê só o suficiente para confirmar que é uma playlist, e não uma página de erro.
	buf := make([]byte, 512)
	n, _ := body.Read(buf)
	if !strings.Contains(string(buf[:n]), "#EXTM3U") && !strings.Contains(string(buf[:n]), "#EXTINF") {
		return fmt.Errorf("m3u: a resposta não parece uma playlist M3U")
	}
	return nil
}

// playlistURL monta a URL da playlist.
//
// Quando há credencial configurada e a URL base não a traz, usamos o endpoint get.php
// no padrão dos painéis compatíveis com Xtream. Se a base já é uma URL completa de
// playlist, ela é usada como está.
func (p *M3UProvider) playlistURL(cfg sources.Config) string {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.Username == "" || cfg.Password == "" {
		return base
	}
	if strings.Contains(base, "get.php") || strings.Contains(base, ".m3u") {
		return base
	}
	return fmt.Sprintf("%s/get.php?username=%s&password=%s&type=m3u_plus&output=mpegts",
		base, queryEscape(cfg.Username), queryEscape(cfg.Password))
}
