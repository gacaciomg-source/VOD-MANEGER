package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"vodmanager/internal/cryptobox"
	"vodmanager/internal/ingest"
	"vodmanager/internal/sources"
	"vodmanager/internal/store"
	vsync "vodmanager/internal/sync"
	"vodmanager/internal/transport"
	"vodmanager/test/fixtures"
)

// fonteFalsa é um servidor que se comporta como uma fonte real e registra tudo que
// recebe. As requisições registradas são a prova de que a sincronização não toca em mídia.
type fonteFalsa struct {
	mu      sync.Mutex
	pedidos []string
	server  *httptest.Server
}

func (f *fonteFalsa) registrar(caminho string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pedidos = append(f.pedidos, caminho)
}

func (f *fonteFalsa) recebidos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pedidos...)
}

// pedidosDeMidia devolve as requisições que foram para rotas de vídeo.
func (f *fonteFalsa) pedidosDeMidia() []string {
	var out []string
	for _, p := range f.recebidos() {
		if strings.Contains(p, "/movie/") || strings.Contains(p, "/series/") || strings.Contains(p, "/live/") {
			out = append(out, p)
		}
	}
	return out
}

// novaFonteM3U sobe um servidor que entrega uma playlist M3U.
func novaFonteM3U(t *testing.T, playlist string) *fonteFalsa {
	t.Helper()
	f := &fonteFalsa{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.registrar(r.URL.Path)
		if strings.Contains(r.URL.Path, "get.php") || strings.HasSuffix(r.URL.Path, ".m3u") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(playlist))
			return
		}
		// Qualquer outra rota é mídia: se chegar requisição aqui, o teste falha.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("bytes de video"))
	}))
	t.Cleanup(f.server.Close)
	return f
}

// novaFonteXtream sobe um servidor que responde como um painel compatível com Xtream.
func novaFonteXtream(t *testing.T) *fonteFalsa {
	t.Helper()
	f := &fonteFalsa{}

	vodCats := fixtures.Read(t, "xtream/vod_categories.json")
	vodStreams := fixtures.Read(t, "xtream/vod_streams.json")
	serieCats := fixtures.Read(t, "xtream/series_categories.json")
	series := fixtures.Read(t, "xtream/series.json")
	serieInfo := fixtures.Read(t, "xtream/series_info_5501.json")

	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.registrar(r.URL.Path)

		if !strings.HasSuffix(r.URL.Path, "player_api.php") {
			// Rota de mídia. Nenhuma requisição deveria chegar aqui durante o sync.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bytes de video"))
			return
		}

		q := r.URL.Query()
		if q.Get("username") != "usuario" || q.Get("password") != "senha" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user_info":{"auth":0}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch q.Get("action") {
		case "":
			_, _ = w.Write([]byte(`{"user_info":{"auth":1,"status":"Active"}}`))
		case "get_vod_categories":
			_, _ = w.Write(vodCats)
		case "get_vod_streams":
			_, _ = w.Write(vodStreams)
		case "get_series_categories":
			_, _ = w.Write(serieCats)
		case "get_series":
			_, _ = w.Write(series)
		case "get_series_info":
			if q.Get("series_id") == "5501" {
				_, _ = w.Write(serieInfo)
				return
			}
			_, _ = w.Write([]byte(`{"info":{},"episodes":{}}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// montarOrquestrador cria um orquestrador ligado ao banco de teste.
func montarOrquestrador(t *testing.T, env *testEnv) *vsync.Orchestrator {
	t.Helper()
	normalizer, err := ingest.NewNormalizer()
	if err != nil {
		t.Fatalf("NewNormalizer: %v", err)
	}
	return vsync.New(vsync.Options{
		Store:      env.Store,
		Crypto:     env.Crypto,
		Normalizer: normalizer,
		Providers: map[string]sources.Provider{
			"m3u":    transport.NewM3UProvider(),
			"xtream": transport.NewXtreamProvider(),
		},
		Log:    env.Log,
		NodeID: "node-teste",
	})
}

// cadastrarFonte cria a fonte e grava a credencial cifrada.
func cadastrarFonte(t *testing.T, env *testEnv, nome, kind, baseURL string, comCredencial bool) *store.Source {
	t.Helper()
	ctx := context.Background()
	src, err := env.Store.CreateSource(ctx, store.NewSource{
		Name: nome, Kind: kind, BaseURL: baseURL,
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if !comCredencial {
		return src
	}
	segredo, err := json.Marshal(map[string]string{"password": "senha"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	cifrado, err := env.Crypto.Seal(segredo, cryptobox.SourceCredentialAAD(src.ID))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := env.Store.SetSourceCredential(ctx, src.ID, "usuario", cifrado, 1); err != nil {
		t.Fatalf("SetSourceCredential: %v", err)
	}
	return src
}

// ---------------------------------------------------------------------------

// TestSincronizacaoM3UPontaAPonta é o teste que prova que o sistema faz algo útil:
// cadastrar uma fonte, sincronizar e ter catálogo no banco.
func TestSincronizacaoM3UPontaAPonta(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	playlist := string(fixtures.Read(t, "m3u/filmes.m3u")) + "\n" +
		strings.TrimPrefix(string(fixtures.Read(t, "m3u/series.m3u")), "#EXTM3U\n")
	fonte := novaFonteM3U(t, playlist)

	src := cadastrarFonte(t, env, "Fonte M3U", store.SourceKindM3U, fonte.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)

	rel, err := orch.SyncSource(ctx, src.ID, "manual")
	if err != nil {
		t.Fatalf("SyncSource: %v", err)
	}
	if rel.State != "succeeded" {
		t.Fatalf("estado = %q, erro = %q", rel.State, rel.Error)
	}

	// 8 filmes + 8 séries no fixture.
	if rel.Seen != 16 {
		t.Errorf("itens vistos = %d, esperava 16", rel.Seen)
	}
	if rel.New == 0 {
		t.Fatal("nenhum item novo foi criado — o catálogo ficaria vazio")
	}
	// "Série Sem Numeração" não tem temporada/episódio: vai para unresolved, não vira filme.
	if rel.Rejected != 1 {
		t.Errorf("rejeitados = %d, esperava 1 (a série sem numeração)", rel.Rejected)
	}

	// --- O catálogo existe de fato --------------------------------------------
	stats, err := env.Store.GetCatalogStats(ctx)
	if err != nil {
		t.Fatalf("GetCatalogStats: %v", err)
	}
	if stats.Movies != 8 {
		t.Errorf("filmes = %d, esperava 8", stats.Movies)
	}
	if stats.Series != 3 {
		t.Errorf("séries = %d, esperava 3 (Breaking Bad, Cidade Invisível, Dark)", stats.Series)
	}
	if stats.Episodes != 7 {
		t.Errorf("episódios = %d, esperava 7", stats.Episodes)
	}
	if stats.Unresolved != 1 {
		t.Errorf("não resolvidos = %d, esperava 1", stats.Unresolved)
	}

	// --- Um filme concreto, com normalização aplicada -------------------------
	page, err := env.Store.ListContents(ctx, store.ContentFilter{Type: store.ContentMovie, Search: "interestelar"})
	if err != nil {
		t.Fatalf("ListContents: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("busca por interestelar devolveu %d itens", len(page.Items))
	}
	filme := page.Items[0]
	if filme.NormalizedTitle != "interestelar" {
		t.Errorf("título normalizado = %q", filme.NormalizedTitle)
	}
	if filme.Year == nil || *filme.Year != 2014 {
		t.Errorf("ano = %v — deveria ter sido extraído do título", filme.Year)
	}
	if filme.VariantCount != 1 {
		t.Errorf("variantes = %d, esperava 1", filme.VariantCount)
	}

	// --- Uma série com temporadas e episódios ---------------------------------
	seriesPage, err := env.Store.ListContents(ctx, store.ContentFilter{Type: store.ContentSeries, Search: "breaking"})
	if err != nil {
		t.Fatalf("ListContents séries: %v", err)
	}
	if len(seriesPage.Items) != 1 {
		t.Fatalf("busca por breaking devolveu %d séries", len(seriesPage.Items))
	}
	temporadas, err := env.Store.ListSeasons(ctx, seriesPage.Items[0].ID)
	if err != nil {
		t.Fatalf("ListSeasons: %v", err)
	}
	if len(temporadas) != 1 || temporadas[0].SeasonNumber != 1 {
		t.Fatalf("temporadas = %+v", temporadas)
	}
	if len(temporadas[0].Episodes) != 3 {
		t.Errorf("episódios da temporada 1 = %d, esperava 3 (S01E01, S01E02, 1x03)",
			len(temporadas[0].Episodes))
	}

	// --- A GARANTIA CENTRAL: nenhuma URL de mídia foi aberta -------------------
	if midia := fonte.pedidosDeMidia(); len(midia) > 0 {
		t.Fatalf("a sincronização abriu %d URL(s) de mídia — isso nunca pode acontecer:\n%v",
			len(midia), midia)
	}
	if total := len(fonte.recebidos()); total != 1 {
		t.Errorf("requisições à fonte = %d, esperava exatamente 1 (a playlist): %v",
			total, fonte.recebidos())
	}
}

// TestCategoriasNaoDuplicam trava o bug visto no primeiro sync real: o M3U não informa
// se um group-title é de filme ou de série, e criar uma categoria canônica "unknown"
// duplicava no painel toda categoria que os itens já criavam com o tipo certo.
func TestCategoriasNaoDuplicam(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte := novaFonteM3U(t, string(fixtures.Read(t, "m3u/filmes.m3u")))
	src := cadastrarFonte(t, env, "Fonte", store.SourceKindM3U, fonte.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)

	if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
		t.Fatalf("SyncSource: %v", err)
	}

	cats, err := env.Store.ListCategories(ctx)
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}

	vistas := map[string]string{}
	for _, c := range cats {
		if c.ContentType == "unknown" {
			t.Errorf("categoria canônica %q ficou com tipo unknown — o painel mostraria duplicada", c.Name)
		}
		if anterior, repetida := vistas[c.NormalizedName]; repetida {
			t.Errorf("categoria %q aparece duas vezes (tipos %q e %q)", c.NormalizedName, anterior, c.ContentType)
		}
		vistas[c.NormalizedName] = c.ContentType
	}

	// A sincronização NÃO cria categoria: essa é a mudança de contrato. Antes, cada
	// group-title virava uma pasta nova, e "Filmes | Lancamentos" de uma fonte e
	// "LANÇAMENTOS" de outra viravam duas pastas para alguém mesclar depois.
	if len(cats) != 0 {
		t.Errorf("categorias criadas = %d, esperava 0 — a sincronização não deve criar pasta", len(cats))
	}

	// O que ela faz é registrar PENDÊNCIAS: uma decisão a tomar, uma vez.
	pendencias, err := env.Store.ListarPendencias(ctx)
	if err != nil {
		t.Fatalf("ListarPendencias: %v", err)
	}
	if len(pendencias) != 5 {
		t.Fatalf("pendências = %d, esperava 5 — uma por group-title do fixture", len(pendencias))
	}
	for _, p := range pendencias {
		if p.Declarado == "" || p.ContentType == "unknown" {
			t.Errorf("pendência mal formada: %+v", p)
		}
	}
}

// Depois que o administrador marca uma principal, a sincronização vincula sozinha o que
// tiver nome idêntico — e essa categoria deixa de aparecer como pendência.
func TestCategoriaPrincipalVinculaSozinhaPorNomeIdentico(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	lista := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-name="Filme A (2020)" group-title="Acao",Filme A (2020)` + "\n" +
		"http://origem.exemplo.tld/movie/u/s/1.mp4\n"

	// A principal existe ANTES da sincronização.
	principalID, err := env.Store.CriarPrincipal(ctx, "Acao", "acao", "movie")
	if err != nil {
		t.Fatalf("CriarPrincipal: %v", err)
	}

	fonte := novaFonteM3U(t, lista)
	src := cadastrarFonte(t, env, "Fonte", store.SourceKindM3U, fonte.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)
	if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
		t.Fatalf("SyncSource: %v", err)
	}

	pendencias, err := env.Store.ListarPendencias(ctx)
	if err != nil {
		t.Fatalf("ListarPendencias: %v", err)
	}
	if len(pendencias) != 0 {
		t.Errorf("pendências = %d, esperava 0 — o nome era idêntico ao da principal", len(pendencias))
	}

	// E o conteúdo entrou na pasta certa.
	var categoria *int64
	if err := env.Pool.QueryRow(ctx,
		`SELECT category_id FROM contents WHERE type = 'movie' LIMIT 1`).Scan(&categoria); err != nil {
		t.Fatalf("consultando conteúdo: %v", err)
	}
	if categoria == nil || *categoria != principalID {
		t.Errorf("categoria do conteúdo = %v, esperava %d", categoria, principalID)
	}
}

// TestSincronizacaoXtreamPontaAPonta cobre o caminho com API e credenciais.
func TestSincronizacaoXtreamPontaAPonta(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte := novaFonteXtream(t)
	src := cadastrarFonte(t, env, "Fonte Xtream", store.SourceKindXtream, fonte.server.URL, true)
	orch := montarOrquestrador(t, env)

	rel, err := orch.SyncSource(ctx, src.ID, "manual")
	if err != nil {
		t.Fatalf("SyncSource: %v", err)
	}
	if rel.State != "succeeded" {
		t.Fatalf("estado = %q, erro = %q", rel.State, rel.Error)
	}

	stats, err := env.Store.GetCatalogStats(ctx)
	if err != nil {
		t.Fatalf("GetCatalogStats: %v", err)
	}
	// 4 filmes com stream_id no fixture.
	if stats.Movies != 4 {
		t.Errorf("filmes = %d, esperava 4", stats.Movies)
	}
	// Breaking Bad tem 3 episódios com id; Cidade Invisível responde info vazio.
	if stats.Episodes != 3 {
		t.Errorf("episódios = %d, esperava 3", stats.Episodes)
	}
	// A sincronização não cria categoria — quem decide é o administrador. O que ela faz é
	// registrar as categorias da fonte como pendências, com o tipo correto que a API
	// Xtream informa.
	if stats.Categories != 0 {
		t.Errorf("categorias criadas = %d, esperava 0", stats.Categories)
	}
	pendencias, err := env.Store.ListarPendencias(ctx)
	if err != nil {
		t.Fatalf("ListarPendencias: %v", err)
	}
	if len(pendencias) == 0 {
		t.Error("nenhuma categoria da fonte foi registrada como pendência")
	}
	for _, p := range pendencias {
		if p.ContentType != "movie" && p.ContentType != "series" {
			t.Errorf("pendência com tipo inesperado %q: a API Xtream informa o tipo", p.ContentType)
		}
	}

	// A variante de Xtream NÃO guarda URL: guarda a referência a resolver.
	page, err := env.Store.ListContents(ctx, store.ContentFilter{Search: "interestelar"})
	if err != nil {
		t.Fatalf("ListContents: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("esperava 1 conteúdo, veio %d", len(page.Items))
	}
	variantes, err := env.Store.ListVariantsForTarget(ctx, store.TargetContent, page.Items[0].ID)
	if err != nil {
		t.Fatalf("ListVariantsForTarget: %v", err)
	}
	if len(variantes) != 1 {
		t.Fatalf("variantes = %d", len(variantes))
	}
	if variantes[0].OriginURL != "" {
		t.Errorf("a variante de Xtream guardou URL: %q — ela deve ser materializada só no transporte",
			variantes[0].OriginURL)
	}
	if len(variantes[0].StreamRef) == 0 {
		t.Error("a variante ficou sem stream_ref: não haveria como resolvê-la depois")
	}

	// --- A URL só é montada quando pedida, e no formato do protocolo ----------
	url, err := orch.ResolveStreamURL(ctx, &variantes[0])
	if err != nil {
		t.Fatalf("ResolveStreamURL: %v", err)
	}
	esperado := fonte.server.URL + "/movie/usuario/senha/12345.mp4"
	if url != esperado {
		t.Errorf("URL resolvida = %q, esperava %q", url, esperado)
	}

	// --- Nenhuma requisição de mídia durante o sync ---------------------------
	if midia := fonte.pedidosDeMidia(); len(midia) > 0 {
		t.Fatalf("a sincronização abriu URL de mídia: %v", midia)
	}
	// 4 listagens (categorias de filme, filmes, categorias de série, séries)
	// + 1 get_series_info por série NOVA (são 2 séries com id no fixture) = 6.
	if rel.Requests != 6 {
		t.Errorf("requisições = %d, esperava 6 (4 listagens + 2 detalhes de série)", rel.Requests)
	}
}

// TestSincronizacaoIncrementalNaoRepeteDetalheDeSerie prova a economia aprovada em
// docs/07 §6.1: a segunda sincronização não repete get_series_info.
func TestSincronizacaoIncrementalNaoRepeteDetalheDeSerie(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte := novaFonteXtream(t)
	src := cadastrarFonte(t, env, "Fonte Xtream", store.SourceKindXtream, fonte.server.URL, true)
	orch := montarOrquestrador(t, env)

	primeira, err := orch.SyncSource(ctx, src.ID, "manual")
	if err != nil {
		t.Fatalf("primeira sincronização: %v", err)
	}

	segunda, err := orch.SyncSource(ctx, src.ID, "manual")
	if err != nil {
		t.Fatalf("segunda sincronização: %v", err)
	}

	if segunda.Requests >= primeira.Requests {
		t.Errorf("a segunda sincronização fez %d requisições contra %d da primeira — "+
			"o incremental de get_series_info não está funcionando",
			segunda.Requests, primeira.Requests)
	}
	// Nada mudou na fonte: tudo deve cair em "inalterado".
	if segunda.New != 0 {
		t.Errorf("a segunda sincronização criou %d itens novos, esperava 0", segunda.New)
	}
	if segunda.Unchanged == 0 {
		t.Error("nenhum item foi contado como inalterado — o digest não está sendo comparado")
	}

	// Reimportar não duplica: os contadores do catálogo continuam iguais.
	stats, err := env.Store.GetCatalogStats(ctx)
	if err != nil {
		t.Fatalf("GetCatalogStats: %v", err)
	}
	if stats.Movies != 4 {
		t.Errorf("filmes após duas sincronizações = %d, esperava 4 — houve duplicação", stats.Movies)
	}
}

// TestAgrupamentoEntreDuasFontes prova o valor central do sistema: o mesmo filme em duas
// fontes diferentes vira UM conteúdo com DUAS variantes.
func TestAgrupamentoEntreDuasFontes(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	playlistA := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-id="1" tvg-name="Interestelar (2014)" group-title="FILMES",Interestelar (2014)` + "\n" +
		"http://fonte-a.exemplo.tld/movie/u/s/1.mp4\n"
	// Mesma obra, título com decoração diferente e outra fonte.
	playlistB := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-id="99" tvg-name="Interestelar 2014 1080p DUAL" group-title="FILMES",Interestelar 2014 1080p DUAL` + "\n" +
		"http://fonte-b.exemplo.tld/vod/99.mkv\n"

	fonteA := novaFonteM3U(t, playlistA)
	fonteB := novaFonteM3U(t, playlistB)

	srcA := cadastrarFonte(t, env, "Fonte A", store.SourceKindM3U, fonteA.server.URL+"/lista.m3u", false)
	srcB := cadastrarFonte(t, env, "Fonte B", store.SourceKindM3U, fonteB.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)

	if _, err := orch.SyncSource(ctx, srcA.ID, "manual"); err != nil {
		t.Fatalf("sincronizando fonte A: %v", err)
	}
	if _, err := orch.SyncSource(ctx, srcB.ID, "manual"); err != nil {
		t.Fatalf("sincronizando fonte B: %v", err)
	}

	stats, err := env.Store.GetCatalogStats(ctx)
	if err != nil {
		t.Fatalf("GetCatalogStats: %v", err)
	}
	if stats.Movies != 1 {
		t.Fatalf("filmes = %d, esperava 1 — as duas fontes deveriam ter sido agrupadas", stats.Movies)
	}
	if stats.Variants != 2 {
		t.Fatalf("variantes = %d, esperava 2", stats.Variants)
	}

	page, err := env.Store.ListContents(ctx, store.ContentFilter{})
	if err != nil {
		t.Fatalf("ListContents: %v", err)
	}
	if page.Items[0].VariantCount != 2 {
		t.Errorf("o conteúdo tem %d variantes, esperava 2", page.Items[0].VariantCount)
	}

	// As variantes vêm na ordem de prioridade da fonte.
	variantes, err := env.Store.ListVariantsForTarget(ctx, store.TargetContent, page.Items[0].ID)
	if err != nil {
		t.Fatalf("ListVariantsForTarget: %v", err)
	}
	if len(variantes) != 2 {
		t.Fatalf("variantes = %d", len(variantes))
	}
	if variantes[0].SourceID != srcA.ID {
		t.Errorf("a primeira variante é da fonte %d, esperava a de prioridade 1 (%d)",
			variantes[0].SourceID, srcA.ID)
	}
	// A fonte B trouxe as tags de qualidade; elas foram preservadas, não descartadas.
	if len(variantes[1].QualityTags) == 0 {
		t.Error("as tags de qualidade da fonte B foram perdidas")
	}
}

// TestFilmesSemAnoAgrupamEntreFontes trava o bug visto no primeiro uso real do painel:
// "Toy Story", "Rocky II" e "O Poderoso Chefão" apareciam DUAS vezes no catálogo, uma
// por fonte, porque sem ano o matching não alcançava o limiar de agrupamento.
func TestFilmesSemAnoAgrupamEntreFontes(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// A mesma lista servida por duas fontes distintas: todo conteúdo deveria ter
	// exatamente uma entrada no catálogo, com duas origens.
	lista := string(fixtures.Read(t, "m3u/filmes.m3u"))
	fonteA := novaFonteM3U(t, lista)
	fonteB := novaFonteM3U(t, lista)

	srcA := cadastrarFonte(t, env, "Fonte A", store.SourceKindM3U, fonteA.server.URL+"/lista.m3u", false)
	srcB := cadastrarFonte(t, env, "Fonte B", store.SourceKindM3U, fonteB.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)

	if _, err := orch.SyncSource(ctx, srcA.ID, "manual"); err != nil {
		t.Fatalf("sincronizando A: %v", err)
	}
	if _, err := orch.SyncSource(ctx, srcB.ID, "manual"); err != nil {
		t.Fatalf("sincronizando B: %v", err)
	}

	stats, err := env.Store.GetCatalogStats(ctx)
	if err != nil {
		t.Fatalf("GetCatalogStats: %v", err)
	}
	if stats.Movies != 8 {
		t.Errorf("filmes = %d, esperava 8 — a mesma lista em duas fontes não pode duplicar o catálogo", stats.Movies)
	}
	if stats.Variants != 16 {
		t.Errorf("variantes = %d, esperava 16 (8 filmes × 2 fontes)", stats.Variants)
	}

	// Todo conteúdo precisa ter as DUAS origens.
	page, err := env.Store.ListContents(ctx, store.ContentFilter{Type: store.ContentMovie, Limit: 50})
	if err != nil {
		t.Fatalf("ListContents: %v", err)
	}
	for _, c := range page.Items {
		if c.VariantCount != 2 {
			t.Errorf("%q tem %d origem(ns), esperava 2 — não foi agrupado entre as fontes",
				c.Title, c.VariantCount)
		}
	}

	// O ano de "O Poderoso Chefão" está só no nome de exibição, não no tvg-name.
	// Aproveitá-lo é o que permite agrupar sem depender do limiar sem-ano.
	chefao, err := env.Store.ListContents(ctx, store.ContentFilter{Search: "chefao"})
	if err != nil {
		t.Fatalf("busca: %v", err)
	}
	if len(chefao.Items) != 1 {
		t.Fatalf("busca por chefão devolveu %d itens, esperava 1", len(chefao.Items))
	}
	if chefao.Items[0].Year == nil || *chefao.Items[0].Year != 1972 {
		t.Errorf("ano = %v, esperava 1972 extraído do nome de exibição", chefao.Items[0].Year)
	}
}

// TestVersoesDeIdiomaNaoSaoAgrupadas reproduz o caso relatado no uso real: a versão
// legendada foi agrupada com a dublada e sumiu da categoria de legendados.
func TestVersoesDeIdiomaNaoSaoAgrupadas(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	lista := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-id="1" tvg-name="#SeAcabó: Diário das Campeãs" group-title="FILMES | DUBLADOS",#SeAcabó: Diário das Campeãs` + "\n" +
		"http://fonte-a.exemplo.tld/movie/u/s/1.mp4\n" +
		`#EXTINF:-1 tvg-id="2" tvg-name="#SeAcabó: Diário das Campeãs [L]" group-title="FILMES | LEGENDADOS",#SeAcabó: Diário das Campeãs [L]` + "\n" +
		"http://fonte-a.exemplo.tld/movie/u/s/2.mp4\n" +
		// Estes dois são obras DIFERENTES e não podem colidir: o bloco entre parênteses
		// precisa permanecer no título.
		`#EXTINF:-1 tvg-id="3" tvg-name="#Natal" group-title="FILMES",#Natal` + "\n" +
		"http://fonte-a.exemplo.tld/movie/u/s/3.mp4\n" +
		`#EXTINF:-1 tvg-id="4" tvg-name="Natal (Ao Vivo)" group-title="FILMES",Natal (Ao Vivo)` + "\n" +
		"http://fonte-a.exemplo.tld/movie/u/s/4.mp4\n"

	fonte := novaFonteM3U(t, lista)
	src := cadastrarFonte(t, env, "Fonte", store.SourceKindM3U, fonte.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)

	if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
		t.Fatalf("SyncSource: %v", err)
	}

	stats, err := env.Store.GetCatalogStats(ctx)
	if err != nil {
		t.Fatalf("GetCatalogStats: %v", err)
	}
	if stats.Movies != 4 {
		t.Fatalf("filmes = %d, esperava 4 — cada versão e cada obra é uma entrada própria", stats.Movies)
	}

	// A versão dublada e a legendada precisam ser conteúdos distintos, cada um com uma
	// única origem.
	seacabo, err := env.Store.ListContents(ctx, store.ContentFilter{Search: "seacabo"})
	if err != nil {
		t.Fatalf("busca: %v", err)
	}
	if len(seacabo.Items) != 2 {
		t.Fatalf("busca devolveu %d entradas, esperava 2 (dublada e legendada)", len(seacabo.Items))
	}
	chaves := map[string]bool{}
	for _, c := range seacabo.Items {
		if c.VariantCount != 1 {
			t.Errorf("%q tem %d origens, esperava 1", c.Title, c.VariantCount)
		}
		chaves[c.LanguageKey] = true
	}
	if !chaves[""] || !chaves["leg"] {
		t.Errorf("versões de idioma = %v, esperava a padrão e a legendada", chaves)
	}

	// "#Natal" e "Natal (Ao Vivo)" são obras diferentes.
	natal, err := env.Store.ListContents(ctx, store.ContentFilter{Search: "natal"})
	if err != nil {
		t.Fatalf("busca natal: %v", err)
	}
	if len(natal.Items) != 2 {
		t.Fatalf("busca por natal devolveu %d entradas, esperava 2 obras distintas", len(natal.Items))
	}
	for _, c := range natal.Items {
		if c.VariantCount != 1 {
			t.Errorf("%q tem %d origens, esperava 1 — as duas obras foram fundidas", c.Title, c.VariantCount)
		}
	}
}

// TestItemQueDesapareceNaoEApagado cobre a regra de preservação da doc 03 §7.
func TestItemQueDesapareceNaoEApagado(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	completa := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-id="1" tvg-name="Filme Um (2001)" group-title="FILMES",Filme Um (2001)` + "\n" +
		"http://fonte-a.exemplo.tld/movie/u/s/1.mp4\n" +
		`#EXTINF:-1 tvg-id="2" tvg-name="Filme Dois (2002)" group-title="FILMES",Filme Dois (2002)` + "\n" +
		"http://fonte-a.exemplo.tld/movie/u/s/2.mp4\n"
	reduzida := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-id="1" tvg-name="Filme Um (2001)" group-title="FILMES",Filme Um (2001)` + "\n" +
		"http://fonte-a.exemplo.tld/movie/u/s/1.mp4\n"

	atual := completa
	f := &fonteFalsa{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.registrar(r.URL.Path)
		_, _ = w.Write([]byte(atual))
	}))
	t.Cleanup(f.server.Close)

	src := cadastrarFonte(t, env, "Fonte", store.SourceKindM3U, f.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)

	if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
		t.Fatalf("primeira sincronização: %v", err)
	}
	stats, _ := env.Store.GetCatalogStats(ctx)
	if stats.Movies != 2 {
		t.Fatalf("filmes após a primeira sincronização = %d, esperava 2", stats.Movies)
	}

	// O segundo filme desaparece da fonte.
	atual = reduzida
	rel, err := orch.SyncSource(ctx, src.ID, "manual")
	if err != nil {
		t.Fatalf("segunda sincronização: %v", err)
	}
	if rel.Missing != 1 {
		t.Errorf("ausentes = %d, esperava 1", rel.Missing)
	}

	// Nada foi apagado.
	stats, _ = env.Store.GetCatalogStats(ctx)
	if stats.Movies != 2 {
		t.Errorf("filmes = %d — o conteúdo que desapareceu NÃO pode ser apagado", stats.Movies)
	}
	if stats.Variants != 2 {
		t.Errorf("variantes = %d — a variante ausente não pode ser removida", stats.Variants)
	}

	// A tolerância padrão é 2: a variante ainda está disponível na primeira ausência.
	if stats.Unavailable != 0 {
		t.Errorf("indisponíveis = %d — a tolerância deveria segurar na primeira ausência", stats.Unavailable)
	}

	// Insistindo, ela passa a indisponível — mas continua no banco.
	for i := 0; i < 2; i++ {
		if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
			t.Fatalf("sincronização %d: %v", i+3, err)
		}
	}
	stats, _ = env.Store.GetCatalogStats(ctx)
	if stats.Unavailable != 1 {
		t.Errorf("indisponíveis = %d, esperava 1 após esgotar a tolerância", stats.Unavailable)
	}
	if stats.Variants != 2 {
		t.Errorf("variantes = %d — marcar indisponível não é apagar", stats.Variants)
	}
}

// TestExclusaoDeFonteNaoApagaConteudoESeLimpaSobDemanda cobre as duas metades da regra
// de preservação: excluir a fonte NÃO apaga o catálogo, e a limpeza só acontece por ação
// explícita do administrador.
func TestExclusaoDeFonteNaoApagaConteudoESeLimpaSobDemanda(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte := novaFonteM3U(t, string(fixtures.Read(t, "m3u/filmes.m3u")))
	src := cadastrarFonte(t, env, "Fonte", store.SourceKindM3U, fonte.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)

	if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
		t.Fatalf("SyncSource: %v", err)
	}

	// Um conteúdo é marcado como preservado: ele nunca pode ser removido, nem pela
	// limpeza manual.
	page, err := env.Store.ListContents(ctx, store.ContentFilter{Limit: 1})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("ListContents: %v", err)
	}
	protegido := page.Items[0].ID
	if _, err := env.Pool.Exec(ctx, `UPDATE contents SET preserved = true WHERE id = $1`, protegido); err != nil {
		t.Fatalf("marcando preservado: %v", err)
	}

	// Excluir a fonte: as variantes somem, o catálogo permanece.
	if err := env.Store.DeleteSource(ctx, src.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	stats, _ := env.Store.GetCatalogStats(ctx)
	if stats.Variants != 0 {
		t.Errorf("variantes = %d, esperava 0 após excluir a fonte", stats.Variants)
	}
	if stats.Movies != 8 {
		t.Errorf("filmes = %d — excluir a fonte NÃO pode apagar o catálogo", stats.Movies)
	}

	// A prévia informa o tamanho do estrago antes de qualquer remoção.
	previa, err := env.Store.PreviewOrphanCleanup(ctx)
	if err != nil {
		t.Fatalf("PreviewOrphanCleanup: %v", err)
	}
	if previa.Movies != 7 {
		t.Errorf("prévia = %d filmes, esperava 7 (8 menos o preservado)", previa.Movies)
	}
	stats, _ = env.Store.GetCatalogStats(ctx)
	if stats.Movies != 8 {
		t.Error("a prévia removeu conteúdo — ela precisa ser somente leitura")
	}

	// A limpeza só acontece quando pedida, e respeita a proteção.
	removidos, err := env.Store.PurgeOrphanContents(ctx)
	if err != nil {
		t.Fatalf("PurgeOrphanContents: %v", err)
	}
	if removidos.Movies != 7 {
		t.Errorf("removidos = %d, esperava 7", removidos.Movies)
	}

	stats, _ = env.Store.GetCatalogStats(ctx)
	if stats.Movies != 1 {
		t.Errorf("filmes = %d, esperava 1 — o preservado tem que sobreviver", stats.Movies)
	}
	var aindaLa bool
	if err := env.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM contents WHERE id = $1)`, protegido).Scan(&aindaLa); err != nil {
		t.Fatalf("verificando preservado: %v", err)
	}
	if !aindaLa {
		t.Error("o conteúdo marcado como preservado foi removido pela limpeza")
	}
}

// TestSincronizacaoRegistraExecucaoEEvento garante rastreabilidade.
func TestSincronizacaoRegistraExecucaoEEvento(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte := novaFonteM3U(t, string(fixtures.Read(t, "m3u/filmes.m3u")))
	src := cadastrarFonte(t, env, "Fonte", store.SourceKindM3U, fonte.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)

	rel, err := orch.SyncSource(ctx, src.ID, "manual")
	if err != nil {
		t.Fatalf("SyncSource: %v", err)
	}

	runs, err := env.Store.ListSyncRuns(ctx, &src.ID, 10)
	if err != nil {
		t.Fatalf("ListSyncRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("execuções = %d, esperava 1", len(runs))
	}
	run := runs[0]
	if run.State != "succeeded" || run.FinishedAt == nil {
		t.Errorf("execução = %+v", run)
	}
	if run.ItemsSeen != rel.Seen || run.ItemsNew != rel.New {
		t.Errorf("contadores da execução não batem com o relatório: %+v vs %+v", run, rel)
	}
	if run.NodeID != "node-teste" {
		t.Errorf("node_id = %q — a coluna existe para separar quem fez o quê no futuro", run.NodeID)
	}

	eventos, err := env.Store.ListEvents(ctx, store.EventFilter{Category: "sync"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(eventos) == 0 {
		t.Fatal("nenhum evento de sincronização registrado")
	}
}

// TestCredencialNaoVazaNaSincronizacao é a guarda de credencial no caminho real, com
// HTTP de verdade.
func TestCredencialNaoVazaNaSincronizacao(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte := novaFonteXtream(t)
	src := cadastrarFonte(t, env, "Fonte Xtream", store.SourceKindXtream, fonte.server.URL, true)
	orch := montarOrquestrador(t, env)

	if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
		t.Fatalf("SyncSource: %v", err)
	}

	// O payload bruto de nenhuma variante pode conter a senha.
	rows, err := env.Pool.Query(ctx, `SELECT raw_payload::text, origin_url FROM source_variants`)
	if err != nil {
		t.Fatalf("consultando variantes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload, origem string
		if err := rows.Scan(&payload, &origem); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if strings.Contains(payload, "senha") {
			t.Errorf("a senha da fonte apareceu no raw_payload: %s", payload)
		}
		if strings.Contains(origem, "senha") {
			t.Errorf("a senha apareceu em origin_url: %s", origem)
		}
	}

	// Nem os eventos de sincronização.
	eventos, err := env.Store.ListEvents(ctx, store.EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, e := range eventos {
		if strings.Contains(e.Message, "senha") || strings.Contains(string(e.Data), "senha") {
			t.Errorf("a senha apareceu num evento: %s %s", e.Message, e.Data)
		}
	}
}

// TestSincronizacaoDeFonteXtreamSemCredencialFalhaComClareza: erro útil, não obscuro.
func TestSincronizacaoDeFonteXtreamSemCredencialFalhaComClareza(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte := novaFonteXtream(t)
	src := cadastrarFonte(t, env, "Sem credencial", store.SourceKindXtream, fonte.server.URL, false)
	orch := montarOrquestrador(t, env)

	_, err := orch.SyncSource(ctx, src.ID, "manual")
	if err == nil {
		t.Fatal("esperava falha ao sincronizar Xtream sem credencial")
	}

	runs, _ := env.Store.ListSyncRuns(ctx, &src.ID, 1)
	if len(runs) != 1 || runs[0].State != "failed" {
		t.Fatalf("a execução deveria ficar registrada como falha: %+v", runs)
	}
	if runs[0].ErrorMessage == "" {
		t.Error("a execução falhou sem mensagem de erro registrada")
	}
}

// TestProbeDaFonte cobre o botão "testar fonte" do painel.
func TestProbeDaFonte(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	orch := montarOrquestrador(t, env)

	fonte := novaFonteXtream(t)

	ok := cadastrarFonte(t, env, "Credencial certa", store.SourceKindXtream, fonte.server.URL, true)
	if err := orch.TestSource(ctx, ok.ID); err != nil {
		t.Errorf("fonte com credencial correta deveria passar: %v", err)
	}

	ruim := cadastrarFonte(t, env, "Sem credencial", store.SourceKindXtream, fonte.server.URL, false)
	if err := orch.TestSource(ctx, ruim.ID); err == nil {
		t.Error("fonte sem credencial deveria falhar no probe")
	}

	m3uFonte := novaFonteM3U(t, "#EXTM3U\n")
	m3uSrc := cadastrarFonte(t, env, "M3U", store.SourceKindM3U, m3uFonte.server.URL+"/lista.m3u", false)
	if err := orch.TestSource(ctx, m3uSrc.ID); err != nil {
		t.Errorf("playlist válida deveria passar no probe: %v", err)
	}
}

// TestTetoDeRequisicoesMarcaComoParcial: o mecanismo que impede banimento por abuso.
func TestTetoDeRequisicoesMarcaComoParcial(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte := novaFonteXtream(t)
	src := cadastrarFonte(t, env, "Fonte Xtream", store.SourceKindXtream, fonte.server.URL, true)

	// Orçamento de 2 requisições: não dá nem para as listagens todas.
	orcamento := 2
	if _, err := env.Store.UpdateSourceBudget(ctx, src.ID, orcamento); err != nil {
		t.Fatalf("UpdateSourceBudget: %v", err)
	}

	orch := montarOrquestrador(t, env)
	rel, err := orch.SyncSource(ctx, src.ID, "manual")
	if err != nil {
		t.Fatalf("SyncSource não deveria falhar por teto: %v", err)
	}
	if rel.State != "partial" {
		t.Fatalf("estado = %q, esperava partial", rel.State)
	}
	if rel.Requests > orcamento {
		t.Errorf("requisições = %d, o teto de %d foi ultrapassado", rel.Requests, orcamento)
	}
	// Coleta parcial não pode marcar ausências: nada sumiu, só não foi buscado.
	if rel.Missing != 0 {
		t.Errorf("ausentes = %d — uma coleta parcial não pode marcar ausências", rel.Missing)
	}
}
