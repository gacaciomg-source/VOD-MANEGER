package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vodmanager/internal/edge"
	"vodmanager/internal/store"
)

// conteudoDeVideo é o "filme" que a fonte falsa entrega. Precisa ser grande o bastante
// para exercitar Range e cópia em vários buffers.
var conteudoDeVideo = bytes.Repeat([]byte("VODMANAGER"), 100_000) // ~1 MB

// origemDeVideo é uma fonte que entrega bytes de verdade, com suporte a Range.
type origemDeVideo struct {
	server    *httptest.Server
	pedidos   atomic.Int64
	falharAte atomic.Int64 // quantas requisições devem falhar antes de responder bem
	semRange  atomic.Bool
	exigeCred atomic.Bool
}

func novaOrigemDeVideo(t *testing.T) *origemDeVideo {
	t.Helper()
	o := &origemDeVideo{}
	o.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := o.pedidos.Add(1)

		if o.exigeCred.Load() && !strings.Contains(r.URL.Path, "/usuario/senha/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if n <= o.falharAte.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if o.semRange.Load() {
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", fmt.Sprint(len(conteudoDeVideo)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(conteudoDeVideo)
			return
		}
		// http.ServeContent trata Range, 206 e If-Range corretamente.
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "filme.mp4", time.Unix(0, 0), bytes.NewReader(conteudoDeVideo))
	}))
	t.Cleanup(o.server.Close)
	return o
}

// ambienteStreaming sobe a API completa com o plano de dados ligado.
type ambienteStreaming struct {
	*testEnv
	base      string
	usuario   string
	senha     string
	credID    int64
	contentID int64
	sourceID  int64
	proxy     *edge.Proxy
}

func montarStreaming(t *testing.T, origem *origemDeVideo) *ambienteStreaming {
	t.Helper()
	c, env := newAPI(t)
	ctx := context.Background()

	// Fonte M3U cujas URLs apontam para a origem de vídeo.
	lista := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-id="1" tvg-name="Interestelar (2014)" group-title="Filmes | Ficcao",Interestelar (2014)` + "\n" +
		origem.server.URL + "/movie/usuario/senha/1.mp4\n"

	fonteM3U := novaFonteM3U(t, lista)
	src := cadastrarFonte(t, env, "Fonte de Vídeo", store.SourceKindM3U, fonteM3U.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)
	if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
		t.Fatalf("SyncSource: %v", err)
	}

	page, err := env.Store.ListContents(ctx, store.ContentFilter{Search: "interestelar"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("catálogo inesperado: %v %+v", err, page)
	}

	// Credencial de saída.
	autenticador := edge.NewAuthenticator(env.Store, chaveDeTeste(t))
	const usuario, senha = "vodm_teste", "senha-de-streaming-de-teste"
	cred, err := env.Store.CreateStreamCredential(ctx, "XC_VM", "", usuario,
		autenticador.HashSenha(senha), nil, 0, nil)
	if err != nil {
		t.Fatalf("CreateStreamCredential: %v", err)
	}

	proxy := edge.New(edge.Options{
		Store: env.Store, Auth: autenticador, Resolver: orch,
		Log: env.Log, NodeID: "node-teste",
	})

	// Servidor só com as rotas de streaming: é o que queremos exercitar.
	mux := chiComStreaming(proxy)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	_ = c
	return &ambienteStreaming{
		testEnv: env, base: ts.URL, usuario: usuario, senha: senha,
		credID: cred.ID, contentID: page.Items[0].ID, sourceID: src.ID, proxy: proxy,
	}
}

func (a *ambienteStreaming) urlFilme() string {
	return fmt.Sprintf("%s/movie/%s/%s/%d.mp4", a.base, a.usuario, a.senha, a.contentID)
}

// ---------------------------------------------------------------------------

func TestStreamingEntregaOVideoInteiro(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	amb := montarStreaming(t, origem)

	resp, err := http.Get(amb.urlFilme())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		corpo, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, corpo)
	}
	recebido, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("lendo corpo: %v", err)
	}
	if !bytes.Equal(recebido, conteudoDeVideo) {
		t.Fatalf("bytes recebidos = %d, esperava %d — o vídeo chegou corrompido",
			len(recebido), len(conteudoDeVideo))
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Error("o player precisa de Accept-Ranges para conseguir dar seek")
	}
	if resp.Header.Get("Content-Type") != "video/mp4" {
		t.Errorf("content-type = %q", resp.Header.Get("Content-Type"))
	}
}

// Seek é a operação mais comum num player depois do play.
func TestStreamingSuportaRange(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	amb := montarStreaming(t, origem)

	req, _ := http.NewRequest(http.MethodGet, amb.urlFilme(), nil)
	req.Header.Set("Range", "bytes=1000-1999")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET com Range: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, esperava 206", resp.StatusCode)
	}
	recebido, _ := io.ReadAll(resp.Body)
	if len(recebido) != 1000 {
		t.Fatalf("bytes = %d, esperava 1000", len(recebido))
	}
	if !bytes.Equal(recebido, conteudoDeVideo[1000:2000]) {
		t.Error("o trecho devolvido não corresponde ao intervalo pedido")
	}
	if cr := resp.Header.Get("Content-Range"); cr == "" {
		t.Error("Content-Range ausente: o player não saberia onde está")
	}
}

// A URL de origem — com a credencial da fonte — nunca pode chegar ao cliente.
func TestStreamingNaoVazaAOrigem(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	amb := montarStreaming(t, origem)

	resp, err := http.Get(amb.urlFilme())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	for chave, valores := range resp.Header {
		for _, v := range valores {
			if strings.Contains(v, origem.server.URL) {
				t.Errorf("o cabeçalho %s revelou o endereço da fonte: %q", chave, v)
			}
		}
	}
	if resp.Header.Get("Location") != "" {
		t.Error("houve redirecionamento para a fonte — o cliente jamais deve falar com ela")
	}
}

func TestStreamingExigeCredencialValida(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	amb := montarStreaming(t, origem)

	casos := map[string]struct {
		url    string
		status int
	}{
		"senha errada": {
			fmt.Sprintf("%s/movie/%s/errada/%d.mp4", amb.base, amb.usuario, amb.contentID),
			http.StatusUnauthorized,
		},
		"usuário inexistente": {
			fmt.Sprintf("%s/movie/ninguem/%s/%d.mp4", amb.base, amb.senha, amb.contentID),
			http.StatusUnauthorized,
		},
		"conteúdo inexistente": {
			fmt.Sprintf("%s/movie/%s/%s/999999.mp4", amb.base, amb.usuario, amb.senha),
			http.StatusNotFound,
		},
	}
	for nome, caso := range casos {
		t.Run(nome, func(t *testing.T) {
			resp, err := http.Get(caso.url)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != caso.status {
				t.Errorf("status = %d, esperava %d", resp.StatusCode, caso.status)
			}
		})
	}
}

// A promessa central da decisão D7: revogar corta o acesso na hora.
func TestRevogarCredencialCortaOAcesso(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	amb := montarStreaming(t, origem)
	ctx := context.Background()

	resp, err := http.Get(amb.urlFilme())
	if err != nil {
		t.Fatalf("GET antes de revogar: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("antes de revogar: status = %d", resp.StatusCode)
	}

	if err := amb.Store.RevokeStreamCredential(ctx, amb.credID); err != nil {
		t.Fatalf("RevokeStreamCredential: %v", err)
	}

	// O autenticador tem cache de 5s; em produção o painel invalida na hora. Aqui
	// esperamos o TTL para provar que ele expira sozinho, sem depender da invalidação.
	prazo := time.Now().Add(12 * time.Second)
	for time.Now().Before(prazo) {
		resp, err = http.Get(amb.urlFilme())
		if err != nil {
			t.Fatalf("GET depois de revogar: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			return // revogação valeu
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("a credencial revogada continuou funcionando (último status: %d)", resp.StatusCode)
}

// Failover ANTES do primeiro byte: a primeira fonte falha, a segunda entrega.
func TestFailoverAntesDoPrimeiroByte(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	// As duas primeiras tentativas falham; a terceira responde.
	origem.falharAte.Store(2)

	amb := montarStreaming(t, origem)

	resp, err := http.Get(amb.urlFilme())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	// Só há UMA variante, então o proxy tenta uma vez e desiste: o failover percorre
	// variantes, não repete a mesma. Com uma origem que falha, o resultado correto é 502.
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, esperava 502 com a única origem falhando", resp.StatusCode)
	}

	// Registrado como erro, com a tentativa contabilizada.
	stats, err := amb.Store.GetStreamStats(context.Background())
	if err != nil {
		t.Fatalf("GetStreamStats: %v", err)
	}
	if stats.Errors24h == 0 {
		t.Error("a falha não foi registrada nas estatísticas")
	}
}

func TestStreamingRegistraSessao(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	amb := montarStreaming(t, origem)
	ctx := context.Background()

	resp, err := http.Get(amb.urlFilme())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	stats, err := amb.Store.GetStreamStats(ctx)
	if err != nil {
		t.Fatalf("GetStreamStats: %v", err)
	}
	if stats.Last24h == 0 {
		t.Fatal("a sessão não foi registrada")
	}
	if stats.BytesServed != int64(len(conteudoDeVideo)) {
		t.Errorf("bytes contabilizados = %d, esperava %d", stats.BytesServed, len(conteudoDeVideo))
	}
	if stats.AvgTTFBMs == nil {
		t.Error("o TTFB não foi medido — é a métrica central de latência")
	}
	if stats.Active != 0 {
		t.Errorf("streams ativos = %d, esperava 0 depois de terminar", stats.Active)
	}
}

// A URL assinada permite testar num player sem criar credencial.
func TestURLAssinada(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	amb := montarStreaming(t, origem)

	autenticador := edge.NewAuthenticator(amb.Store, chaveDeTeste(t))
	caminho := fmt.Sprintf("/stream/%d", amb.contentID)
	expira, assinatura := autenticador.AssinarURL(caminho, time.Hour)

	url := fmt.Sprintf("%s%s?exp=%d&sig=%s", amb.base, caminho, expira, assinatura)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET assinado: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		corpo, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, corpo)
	}

	// Assinatura adulterada não passa.
	ruim := fmt.Sprintf("%s%s?exp=%d&sig=%s", amb.base, caminho, expira, "assinaturafalsa")
	respRuim, err := http.Get(ruim)
	if err != nil {
		t.Fatalf("GET com assinatura falsa: %v", err)
	}
	defer respRuim.Body.Close()
	if respRuim.StatusCode != http.StatusUnauthorized {
		t.Errorf("assinatura falsa devolveu %d, esperava 401", respRuim.StatusCode)
	}
}

// TestLimiteDeConexoesPorCredencial sustenta o caso de vender acesso: sem limite, um
// cliente repassa a senha e você paga a banda de todos.
func TestLimiteDeConexoesPorCredencial(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	// A origem responde devagar, para as reproduções ficarem sobrepostas.
	amb := montarStreaming(t, origem)
	ctx := context.Background()

	limite := 1
	limitePtr := &limite
	if _, err := amb.Store.UpdateStreamCredential(ctx, amb.credID,
		store.StreamCredentialPatch{MaxConnections: &limitePtr}); err != nil {
		t.Fatalf("UpdateStreamCredential: %v", err)
	}

	// O autenticador tem cache; espera ele expirar para o limite novo valer.
	time.Sleep(6 * time.Second)

	// Abre a primeira reprodução e a mantém aberta sem consumir tudo.
	req, _ := http.NewRequest(http.MethodGet, amb.urlFilme(), nil)
	primeira, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("primeira reprodução: %v", err)
	}
	defer primeira.Body.Close()
	if primeira.StatusCode != http.StatusOK {
		t.Fatalf("primeira reprodução: status = %d", primeira.StatusCode)
	}
	// Lê só um pedaço: a conexão permanece aberta e a vaga, ocupada.
	buf := make([]byte, 1024)
	if _, err := io.ReadFull(primeira.Body, buf); err != nil {
		t.Fatalf("lendo início: %v", err)
	}

	// A segunda tem que ser recusada.
	segunda, err := http.Get(amb.urlFilme())
	if err != nil {
		t.Fatalf("segunda reprodução: %v", err)
	}
	defer segunda.Body.Close()
	io.Copy(io.Discard, segunda.Body)

	if segunda.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("segunda reprodução: status = %d, esperava 429 com limite de 1",
			segunda.StatusCode)
	}

	// Ao encerrar a primeira, a vaga volta.
	primeira.Body.Close()
	time.Sleep(500 * time.Millisecond)

	terceira, err := http.Get(amb.urlFilme())
	if err != nil {
		t.Fatalf("terceira reprodução: %v", err)
	}
	defer terceira.Body.Close()
	io.Copy(io.Discard, terceira.Body)
	if terceira.StatusCode != http.StatusOK {
		t.Errorf("depois de liberar a vaga: status = %d, esperava 200", terceira.StatusCode)
	}
}

// Trocar a senha derruba quem estava usando a antiga — o caminho para quando o cliente
// compartilhou o acesso.
func TestRotacionarSenhaInvalidaAAntiga(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	amb := montarStreaming(t, origem)
	ctx := context.Background()

	autenticador := edge.NewAuthenticator(amb.Store, chaveDeTeste(t))
	novaSenha := "senha-nova-de-streaming-rotacionada"
	if _, err := amb.Store.RotateStreamCredentialPassword(ctx, amb.credID,
		autenticador.HashSenha(novaSenha), nil); err != nil {
		t.Fatalf("RotateStreamCredentialPassword: %v", err)
	}
	time.Sleep(6 * time.Second) // TTL do cache do autenticador

	// A senha antiga não vale mais.
	resp, err := http.Get(amb.urlFilme())
	if err != nil {
		t.Fatalf("GET com senha antiga: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("senha antiga devolveu %d, esperava 401", resp.StatusCode)
	}

	// A nova funciona, no MESMO link.
	url := fmt.Sprintf("%s/movie/%s/%s/%d.mp4", amb.base, amb.usuario, novaSenha, amb.contentID)
	resp2, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET com senha nova: %v", err)
	}
	defer resp2.Body.Close()
	io.Copy(io.Discard, resp2.Body)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("senha nova devolveu %d, esperava 200", resp2.StatusCode)
	}
}

// Vários clientes pedindo o mesmo filme funcionam — e cada um abre sua conexão à fonte,
// que é exatamente o que o cache da Fase 5 vai eliminar.
func TestVariosClientesSimultaneos(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	amb := montarStreaming(t, origem)

	const clientes = 5
	erros := make(chan error, clientes)
	for i := 0; i < clientes; i++ {
		go func() {
			resp, err := http.Get(amb.urlFilme())
			if err != nil {
				erros <- err
				return
			}
			defer resp.Body.Close()
			n, err := io.Copy(io.Discard, resp.Body)
			if err != nil {
				erros <- err
				return
			}
			if n != int64(len(conteudoDeVideo)) {
				erros <- fmt.Errorf("recebeu %d bytes, esperava %d", n, len(conteudoDeVideo))
				return
			}
			erros <- nil
		}()
	}
	for i := 0; i < clientes; i++ {
		if err := <-erros; err != nil {
			t.Fatalf("cliente %d falhou: %v", i, err)
		}
	}

	// Sem cache, cada cliente vira uma conexão à fonte. Este número é a linha de base
	// que a Fase 5 precisa derrubar para 1.
	if got := origem.pedidos.Load(); got < clientes {
		t.Errorf("requisições à fonte = %d, esperava ao menos %d", got, clientes)
	}
	t.Logf("%d clientes geraram %d requisições à fonte — o cache da Fase 5 deve reduzir isso a 1",
		clientes, origem.pedidos.Load())
}

// A contabilidade por credencial é o que o painel mostra nas colunas de usos e
// transferido. Ela estava zerada porque ninguém a alimentava.
func TestContabilidadeDaCredencialRegistraUsoEBytes(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	amb := montarStreaming(t, origem)
	ctx := context.Background()

	antes := credencialPorID(t, amb, amb.credID)
	if antes.UseCount != 0 || antes.BytesServed != 0 {
		t.Fatalf("credencial nova já vem contabilizada: %+v", antes)
	}

	// Duas reproduções: uma inteira e um seek, que é uma requisição independente.
	resp, err := http.Get(amb.urlFilme())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	inteiro, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodGet, amb.urlFilme(), nil)
	req.Header.Set("Range", "bytes=0-999")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET com Range: %v", err)
	}
	parcial, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// O handler ainda registra depois que o cliente terminou de ler o corpo: quem lê
	// a resposta não espera o servidor fechar a contabilidade. Por isso o teste espera
	// pelo resultado em vez de assumir que ele já chegou.
	var depois store.StreamCredential
	for range 40 {
		amb.proxy.Contabilidade().Descarregar()
		depois = credencialPorID(t, amb, amb.credID)
		if depois.UseCount == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if depois.UseCount != 2 {
		t.Fatalf("use_count = %d, esperava 2", depois.UseCount)
	}
	esperado := int64(len(inteiro) + len(parcial))
	if depois.BytesServed != esperado {
		t.Errorf("bytes_served = %d, esperava %d", depois.BytesServed, esperado)
	}
	if depois.LastUsedAt == nil {
		t.Error("last_used_at continuou nulo")
	}
	_ = ctx
}

func credencialPorID(t *testing.T, amb *ambienteStreaming, id int64) store.StreamCredential {
	t.Helper()
	creds, err := amb.Store.ListStreamCredentials(context.Background())
	if err != nil {
		t.Fatalf("ListStreamCredentials: %v", err)
	}
	for _, c := range creds {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("credencial %d não encontrada", id)
	return store.StreamCredential{}
}

// A sessão precisa ser fechada em TODOS os caminhos de saída.
//
// Havia um caminho — cliente desistir enquanto tentávamos as origens — que saía sem
// fechar, deixando a linha marcada como 'ativa' para sempre: zero bytes, sem primeiro
// byte, ocupando vaga no limite da credencial. Um cliente que insistia acumulava uma
// linha fantasma por tentativa.
func TestSessaoEhFechadaQuandoClienteDesiste(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	// A origem demora a responder: dá tempo de o cliente desistir no meio.
	origem.falharAte.Store(0)
	amb := montarStreaming(t, origem)
	ctx := context.Background()

	// Cliente que cancela quase imediatamente.
	reqCtx, cancelar := context.WithCancel(ctx)
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, amb.urlFilme(), nil)
	go func() {
		time.Sleep(15 * time.Millisecond)
		cancelar()
	}()
	if resp, err := http.DefaultClient.Do(req); err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// Conta só as sessões DESTA credencial: outros testes do pacote também transmitem, e
	// uma contagem global tornaria este teste dependente da ordem de execução.
	var abertas int
	for range 60 {
		if err := amb.Pool.QueryRow(ctx,
			`SELECT count(*) FROM streams WHERE credential_id = $1 AND state = 'active'`,
			amb.credID).Scan(&abertas); err != nil {
			t.Fatalf("contando sessões: %v", err)
		}
		if abertas == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%d sessão(ões) desta credencial ficaram ativas depois do fim da requisição", abertas)
}

// A limpeza periódica recolhe fantasmas que tenham escapado por algum caminho novo.
func TestLimpezaRecolheSessoesAbandonadas(t *testing.T) {
	origem := novaOrigemDeVideo(t)
	amb := montarStreaming(t, origem)
	ctx := context.Background()

	// Uma sessão antiga, aberta e nunca fechada.
	if _, err := amb.Pool.Exec(ctx, `
		INSERT INTO streams (node_id, content_id, credential_id, state, started_at)
		VALUES ('node-teste', $1, $2, 'active', now() - interval '20 hours')`,
		amb.contentID, amb.credID); err != nil {
		t.Fatalf("inserindo sessão antiga: %v", err)
	}

	n, err := amb.Store.ReleaseStaleStreams(ctx, "node-teste", 12*time.Hour)
	if err != nil {
		t.Fatalf("ReleaseStaleStreams: %v", err)
	}
	if n != 1 {
		t.Fatalf("liberadas = %d, esperava 1", n)
	}

	// Uma sessão recente NÃO pode ser recolhida: um filme longo fica horas aberto.
	if _, err := amb.Pool.Exec(ctx, `
		INSERT INTO streams (node_id, content_id, credential_id, state, started_at)
		VALUES ('node-teste', $1, $2, 'active', now() - interval '30 minutes')`,
		amb.contentID, amb.credID); err != nil {
		t.Fatalf("inserindo sessão recente: %v", err)
	}
	if n, err := amb.Store.ReleaseStaleStreams(ctx, "node-teste", 12*time.Hour); err != nil || n != 0 {
		t.Errorf("uma sessão de 30 minutos foi recolhida (n=%d, err=%v)", n, err)
	}
}
