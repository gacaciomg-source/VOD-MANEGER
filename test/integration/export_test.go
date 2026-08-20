package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"vodmanager/internal/edge"
	"vodmanager/internal/export"
	"vodmanager/internal/store"
)

// ambienteExport sobe a saída pública do catálogo sobre um acervo pequeno e conhecido:
// um filme e uma série com dois episódios em duas temporadas.
type ambienteExport struct {
	*testEnv
	base       string
	origemBase string
	usuario    string
	senha      string
	credID     int64
	filmeID    int64
	serieID    int64
}

func montarExport(t *testing.T) *ambienteExport {
	t.Helper()
	_, env := newAPI(t)
	ctx := context.Background()

	// A fonte declara URLs que NÃO podem aparecer na saída: é o invariante mais
	// importante deste pacote.
	const origem = "http://fonte-secreta.exemplo.tld"
	lista := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-name="Interestelar (2014)" group-title="Filmes | Ficcao",Interestelar (2014)` + "\n" +
		origem + "/movie/usuario/senha/1.mp4\n" +
		`#EXTINF:-1 tvg-name="Arquivo X S01E01" group-title="Series | Misterio",Arquivo X S01E01` + "\n" +
		origem + "/series/usuario/senha/2.mkv\n" +
		`#EXTINF:-1 tvg-name="Arquivo X S02E03" group-title="Series | Misterio",Arquivo X S02E03` + "\n" +
		origem + "/series/usuario/senha/3.mkv\n"

	fonte := novaFonteM3U(t, lista)
	src := cadastrarFonte(t, env, "Fonte de Catálogo", store.SourceKindM3U,
		fonte.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)
	if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
		t.Fatalf("SyncSource: %v", err)
	}

	filmes, err := env.Store.ListContents(ctx, store.ContentFilter{Type: "movie"})
	if err != nil || len(filmes.Items) != 1 {
		t.Fatalf("esperava 1 filme, veio %v (%v)", filmes, err)
	}
	series, err := env.Store.ListContents(ctx, store.ContentFilter{Type: "series"})
	if err != nil || len(series.Items) != 1 {
		t.Fatalf("esperava 1 série, veio %v (%v)", series, err)
	}

	autenticador := edge.NewAuthenticator(env.Store, chaveDeTeste(t))
	const usuario, senha = "vodm_lista", "senha-de-lista-de-teste"
	cred, err := env.Store.CreateStreamCredential(ctx, "Cliente 1", "", usuario,
		autenticador.HashSenha(senha), nil, 0, nil)
	if err != nil {
		t.Fatalf("CreateStreamCredential: %v", err)
	}

	r := chi.NewRouter()
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)

	export.New(export.Deps{
		Store:   env.Store,
		Auth:    autenticador,
		Log:     env.Log,
		BaseURL: func(*http.Request) string { return ts.URL },
	}).Rotas(r)

	return &ambienteExport{
		testEnv: env, base: ts.URL, origemBase: origem,
		usuario: usuario, senha: senha, credID: cred.ID,
		filmeID: filmes.Items[0].ID, serieID: series.Items[0].ID,
	}
}

func (a *ambienteExport) get(t *testing.T, caminho string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(a.base + caminho)
	if err != nil {
		t.Fatalf("GET %s: %v", caminho, err)
	}
	defer resp.Body.Close()
	corpo, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("lendo corpo: %v", err)
	}
	return resp, string(corpo)
}

func (a *ambienteExport) credenciais() string {
	return "username=" + a.usuario + "&password=" + a.senha
}

// ---------------------------------------------------------------------------

func TestListaM3UExigeCredencial(t *testing.T) {
	amb := montarExport(t)

	casos := map[string]string{
		"sem credencial":  "/get.php",
		"senha errada":    "/get.php?username=" + amb.usuario + "&password=errada",
		"usuário errado":  "/get.php?username=ninguem&password=" + amb.senha,
		"caminho sem par": "/playlist/" + amb.usuario + "/errada",
	}
	for nome, caminho := range casos {
		resp, corpo := amb.get(t, caminho)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, esperava 401", nome, resp.StatusCode)
		}
		if strings.Contains(corpo, "EXTINF") {
			t.Errorf("%s: a lista vazou sem autenticação", nome)
		}
	}
}

func TestListaM3UTrazFilmesEEpisodios(t *testing.T) {
	amb := montarExport(t)

	resp, corpo := amb.get(t, "/get.php?"+amb.credenciais())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, corpo)
	}
	if !strings.HasPrefix(corpo, "#EXTM3U\n") {
		t.Fatalf("a lista precisa começar com #EXTM3U, veio: %.40q", corpo)
	}

	// O filme, com o link apontando para nós.
	linkFilme := fmt.Sprintf("%s/movie/%s/%s/%d.mp4", amb.base, amb.usuario, amb.senha, amb.filmeID)
	if !strings.Contains(corpo, linkFilme) {
		t.Errorf("link do filme ausente: %s", linkFilme)
	}
	if !strings.Contains(corpo, `group-title="Filmes | Ficcao"`) {
		t.Error("a categoria do filme não foi preservada na lista")
	}

	// Os episódios, identificados por temporada e número.
	for _, esperado := range []string{"Arquivo X S01E01", "Arquivo X S02E03"} {
		if !strings.Contains(corpo, esperado) {
			t.Errorf("episódio ausente da lista: %s", esperado)
		}
	}
	if strings.Count(corpo, "#EXTINF") != 3 {
		t.Errorf("entradas na lista = %d, esperava 3", strings.Count(corpo, "#EXTINF"))
	}
}

// O invariante que não pode falhar: a lista é entregue a terceiros, e o endereço da
// fonte é o que o sistema inteiro existe para esconder.
func TestListaM3UNuncaExpoeAFonte(t *testing.T) {
	amb := montarExport(t)

	_, corpo := amb.get(t, "/get.php?"+amb.credenciais())
	if strings.Contains(corpo, amb.origemBase) {
		t.Fatal("a URL da fonte apareceu na lista M3U")
	}
	if strings.Contains(corpo, "fonte-secreta") || strings.Contains(corpo, "/usuario/senha/") {
		t.Fatal("dados da fonte vazaram na lista M3U")
	}
	// Todo link precisa apontar para o nosso endereço.
	for _, linha := range strings.Split(corpo, "\n") {
		if strings.HasPrefix(linha, "http") && !strings.HasPrefix(linha, amb.base) {
			t.Errorf("link fora do nosso endereço: %s", linha)
		}
	}
}

func TestListaM3UFiltraPorTipo(t *testing.T) {
	amb := montarExport(t)

	_, soFilmes := amb.get(t, "/get.php?"+amb.credenciais()+"&conteudo=filmes")
	if strings.Contains(soFilmes, "Arquivo X") {
		t.Error("conteudo=filmes trouxe episódio")
	}
	if !strings.Contains(soFilmes, "Interestelar") {
		t.Error("conteudo=filmes não trouxe o filme")
	}

	_, soSeries := amb.get(t, "/get.php?"+amb.credenciais()+"&conteudo=series")
	if strings.Contains(soSeries, "Interestelar") {
		t.Error("conteudo=series trouxe filme")
	}
	if !strings.Contains(soSeries, "Arquivo X") {
		t.Error("conteudo=series não trouxe os episódios")
	}
}

// Revogar precisa cortar a lista, não só o vídeo.
func TestListaM3UParaAposRevogacao(t *testing.T) {
	amb := montarExport(t)

	if resp, _ := amb.get(t, "/get.php?"+amb.credenciais()); resp.StatusCode != http.StatusOK {
		t.Fatalf("antes da revogação o acesso deveria funcionar, veio %d", resp.StatusCode)
	}
	if err := amb.Store.RevokeStreamCredential(context.Background(), amb.credID); err != nil {
		t.Fatalf("RevokeStreamCredential: %v", err)
	}
	// O autenticador guarda a credencial por poucos segundos; o painel invalida o cache
	// ao revogar, e aqui fazemos o mesmo para exercitar o efeito imediato.
	edge.NewAuthenticator(amb.Store, chaveDeTeste(t)).InvalidarTudo()

	esperarNegado(t, amb)
}

// esperarNegado tolera o cache curto de credencial do autenticador.
func esperarNegado(t *testing.T, amb *ambienteExport) {
	t.Helper()
	for range 30 {
		resp, _ := amb.get(t, "/get.php?"+amb.credenciais())
		if resp.StatusCode == http.StatusUnauthorized {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("credencial revogada continuou entregando a lista")
}

// ---------------------------------------------------------------------------
// API Xtream
// ---------------------------------------------------------------------------

func TestXtreamHandshake(t *testing.T) {
	amb := montarExport(t)

	resp, corpo := amb.get(t, "/player_api.php?"+amb.credenciais())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, corpo)
	}

	var out struct {
		UserInfo struct {
			Auth     int    `json:"auth"`
			Status   string `json:"status"`
			Username string `json:"username"`
		} `json:"user_info"`
		ServerInfo struct {
			URL            string `json:"url"`
			Port           string `json:"port"`
			ServerProtocol string `json:"server_protocol"`
		} `json:"server_info"`
	}
	if err := json.Unmarshal([]byte(corpo), &out); err != nil {
		t.Fatalf("resposta não é JSON: %v — %s", err, corpo)
	}
	if out.UserInfo.Auth != 1 || out.UserInfo.Status != "Active" {
		t.Errorf("handshake não autorizou: %+v", out.UserInfo)
	}
	if out.UserInfo.Username != amb.usuario {
		t.Errorf("username = %q", out.UserInfo.Username)
	}
	// Host e porta separados: é assim que o cliente remonta as URLs de vídeo.
	if out.ServerInfo.URL == "" || out.ServerInfo.Port == "" {
		t.Errorf("server_info incompleto: %+v", out.ServerInfo)
	}
	if strings.Contains(out.ServerInfo.URL, "://") {
		t.Errorf("server_info.url deve ser só o host, veio %q", out.ServerInfo.URL)
	}
}

func TestXtreamNegaCredencialInvalida(t *testing.T) {
	amb := montarExport(t)

	resp, corpo := amb.get(t, "/player_api.php?username="+amb.usuario+"&password=errada")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, esperava 401", resp.StatusCode)
	}
	if strings.Contains(corpo, `"auth":1`) {
		t.Error("resposta negada trouxe auth=1")
	}
}

func TestXtreamCategoriasEFilmes(t *testing.T) {
	amb := montarExport(t)

	_, corpo := amb.get(t, "/player_api.php?"+amb.credenciais()+"&action=get_vod_categories")
	var cats []struct {
		CategoryID   string `json:"category_id"`
		CategoryName string `json:"category_name"`
	}
	if err := json.Unmarshal([]byte(corpo), &cats); err != nil {
		t.Fatalf("categorias não são JSON: %v — %s", err, corpo)
	}
	if len(cats) == 0 {
		t.Fatal("nenhuma categoria de filme foi exportada")
	}
	// O id precisa ser texto: cliente que recebe número costuma descartar o item.
	if cats[0].CategoryID == "" {
		t.Error("category_id vazio")
	}

	_, corpo = amb.get(t, "/player_api.php?"+amb.credenciais()+"&action=get_vod_streams")
	var filmes []struct {
		Name       string `json:"name"`
		StreamID   int64  `json:"stream_id"`
		StreamType string `json:"stream_type"`
		Extension  string `json:"container_extension"`
		Direct     string `json:"direct_source"`
	}
	if err := json.Unmarshal([]byte(corpo), &filmes); err != nil {
		t.Fatalf("filmes não são JSON: %v — %s", err, corpo)
	}
	if len(filmes) != 1 {
		t.Fatalf("filmes = %d, esperava 1", len(filmes))
	}
	if filmes[0].StreamID != amb.filmeID {
		t.Errorf("stream_id = %d, esperava %d", filmes[0].StreamID, amb.filmeID)
	}
	if filmes[0].StreamType != "movie" || filmes[0].Extension == "" {
		t.Errorf("filme mal formado: %+v", filmes[0])
	}
	// direct_source preenchido mandaria o cliente buscar o vídeo fora do nosso servidor.
	if filmes[0].Direct != "" {
		t.Errorf("direct_source deveria ser vazio, veio %q", filmes[0].Direct)
	}
}

func TestXtreamSerieComTemporadasEEpisodios(t *testing.T) {
	amb := montarExport(t)

	_, corpo := amb.get(t, "/player_api.php?"+amb.credenciais()+"&action=get_series")
	var series []struct {
		Name         string `json:"name"`
		SeriesID     int64  `json:"series_id"`
		EpisodeCount int    `json:"episode_count"`
	}
	if err := json.Unmarshal([]byte(corpo), &series); err != nil {
		t.Fatalf("séries não são JSON: %v — %s", err, corpo)
	}
	if len(series) != 1 || series[0].SeriesID != amb.serieID {
		t.Fatalf("séries inesperadas: %+v", series)
	}
	if series[0].EpisodeCount != 2 {
		t.Errorf("episode_count = %d, esperava 2", series[0].EpisodeCount)
	}

	_, corpo = amb.get(t, fmt.Sprintf("/player_api.php?%s&action=get_series_info&series_id=%d",
		amb.credenciais(), amb.serieID))
	var info struct {
		Info struct {
			Name string `json:"name"`
		} `json:"info"`
		Seasons  []map[string]any `json:"seasons"`
		Episodes map[string][]struct {
			ID         string `json:"id"`
			EpisodeNum int    `json:"episode_num"`
			Season     int    `json:"season"`
			Extension  string `json:"container_extension"`
			Direct     string `json:"direct_source"`
		} `json:"episodes"`
	}
	if err := json.Unmarshal([]byte(corpo), &info); err != nil {
		t.Fatalf("detalhe da série não é JSON: %v — %s", err, corpo)
	}
	if info.Info.Name == "" {
		t.Error("info.name vazio")
	}
	// Duas temporadas distintas, cada uma com seu episódio.
	if len(info.Episodes) != 2 {
		t.Fatalf("temporadas em episodes = %d, esperava 2: %+v", len(info.Episodes), info.Episodes)
	}
	if len(info.Seasons) != 2 {
		t.Errorf("seasons = %d, esperava 2", len(info.Seasons))
	}
	for chave, eps := range info.Episodes {
		if len(eps) != 1 {
			t.Errorf("temporada %s tem %d episódios, esperava 1", chave, len(eps))
			continue
		}
		if eps[0].ID == "" || eps[0].Extension == "" {
			t.Errorf("episódio mal formado na temporada %s: %+v", chave, eps[0])
		}
		if eps[0].Direct != "" {
			t.Errorf("direct_source deveria ser vazio: %q", eps[0].Direct)
		}
		if fmt.Sprint(eps[0].Season) != chave {
			t.Errorf("temporada %s traz episódio marcado como %d", chave, eps[0].Season)
		}
	}
}

// Ações que não servimos precisam responder vazio, não erro: cliente que recebe 404 numa
// aba costuma tratar como servidor fora do ar e desistir das outras.
func TestXtreamAcoesNaoSuportadasRespondemVazio(t *testing.T) {
	amb := montarExport(t)

	for _, acao := range []string{"get_live_categories", "get_live_streams", "acao_inexistente"} {
		resp, corpo := amb.get(t, "/player_api.php?"+amb.credenciais()+"&action="+acao)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d", acao, resp.StatusCode)
		}
		if strings.TrimSpace(corpo) != "[]" {
			t.Errorf("%s: corpo = %q, esperava []", acao, corpo)
		}
	}
}

func TestXtreamGuiaVazioMasValido(t *testing.T) {
	amb := montarExport(t)

	resp, corpo := amb.get(t, "/xmltv.php?"+amb.credenciais())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(corpo, "<tv") {
		t.Errorf("guia não é XMLTV: %s", corpo)
	}
}

// A senha de saída precisa voltar em claro para o painel montar o link pronto — foi por
// não voltar que o administrador tinha de substituir SUA_SENHA à mão em cada link.
func TestLinksProntosDaCredencial(t *testing.T) {
	c, _ := newAPI(t)
	c.loginOK()

	// Usuário e senha escolhidos pelo administrador.
	resp, body := c.do(http.MethodPost, "/api/v1/stream-credentials", map[string]string{
		"name": "Cliente Escolhido", "username": "joao.silva", "password": "senha-do-joao",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("criação = %d: %s", resp.StatusCode, body)
	}
	var criada struct {
		Credential struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"credential"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &criada); err != nil {
		t.Fatalf("decodificando: %v", err)
	}
	if criada.Credential.Username != "joao.silva" || criada.Password != "senha-do-joao" {
		t.Fatalf("o que foi escolhido não foi gravado: %+v", criada)
	}

	// O mesmo usuário não pode ser cadastrado duas vezes.
	resp, _ = c.do(http.MethodPost, "/api/v1/stream-credentials", map[string]string{
		"name": "Outro", "username": "joao.silva", "password": "senha-do-outro",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("usuário repetido = %d, esperava 409", resp.StatusCode)
	}

	// Caracteres que quebrariam a URL são recusados.
	for _, ruim := range []string{"com/barra", "com espaço", "com?query"} {
		resp, _ := c.do(http.MethodPost, "/api/v1/stream-credentials", map[string]string{
			"name": "X", "username": ruim, "password": "senha-valida",
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("usuário %q = %d, esperava 400", ruim, resp.StatusCode)
		}
	}

	// Os links vêm prontos, com a senha embutida.
	caminho := fmt.Sprintf("/api/v1/stream-credentials/%d/links", criada.Credential.ID)
	resp, body = c.do(http.MethodGet, caminho, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("links = %d: %s", resp.StatusCode, body)
	}
	var links struct {
		Password        string `json:"password"`
		Username        string `json:"username"`
		SenhaDisponivel bool   `json:"senha_disponivel"`
		M3U             string `json:"m3u_url"`
		M3UFilmes       string `json:"m3u_filmes_url"`
		Xtream          string `json:"xtream_url"`
	}
	if err := json.Unmarshal(body, &links); err != nil {
		t.Fatalf("decodificando links: %v", err)
	}
	if !links.SenhaDisponivel || links.Password != "senha-do-joao" {
		t.Fatalf("a senha não voltou: %+v", links)
	}
	for nome, u := range map[string]string{"m3u": links.M3U, "filmes": links.M3UFilmes, "xtream": links.Xtream} {
		if !strings.Contains(u, "senha-do-joao") || !strings.Contains(u, "joao.silva") {
			t.Errorf("link %s não está pronto para uso: %s", nome, u)
		}
	}
}

// A senha volta cifrada no banco, nunca em claro — é o que separa "recuperável pelo
// administrador" de "legível por quem obtiver um dump".
func TestSenhaDeSaidaNaoFicaEmClaroNoBanco(t *testing.T) {
	c, env := newAPI(t)
	c.loginOK()

	const senha = "senha-que-nao-pode-vazar"
	resp, body := c.do(http.MethodPost, "/api/v1/stream-credentials", map[string]string{
		"name": "Sigiloso", "username": "cliente.sigiloso", "password": senha,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("criação = %d: %s", resp.StatusCode, body)
	}

	creds, err := env.Store.ListStreamCredentials(context.Background())
	if err != nil {
		t.Fatalf("ListStreamCredentials: %v", err)
	}
	var achou bool
	for _, cred := range creds {
		if cred.Username != "cliente.sigiloso" {
			continue
		}
		achou = true
		if len(cred.PasswordEnc) == 0 {
			t.Fatal("a senha não foi cifrada")
		}
		if strings.Contains(string(cred.PasswordEnc), senha) {
			t.Fatal("a senha aparece em claro dentro do blob cifrado")
		}
		// O HMAC continua sendo o que autentica o vídeo, e não é a senha.
		if len(cred.PasswordHMAC) != 32 {
			t.Errorf("HMAC com %d bytes, esperava 32", len(cred.PasswordHMAC))
		}
	}
	if !achou {
		t.Fatal("credencial não encontrada")
	}

	// E a serialização JSON não pode carregar nenhuma das duas formas.
	if bytes.Contains(body, []byte("password_enc")) || bytes.Contains(body, []byte("password_hmac")) {
		t.Error("a resposta da API expôs os campos internos da senha")
	}
}

// O endereço do conteúdo é separado do endereço do painel de propósito: o link do vídeo
// chega ao cliente, e não deve revelar por onde o sistema é administrado.
func TestEnderecoDoConteudoSeparaDoPainel(t *testing.T) {
	c, env := newAPI(t)
	c.loginOK()
	ctx := context.Background()

	// Sem configuração própria, o conteúdo usa o endereço do painel.
	if err := env.Store.SetSetting(ctx, store.SettingPublicBaseURL, "https://painel.exemplo.tld"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	resp, body := c.do(http.MethodGet, "/api/v1/settings", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /settings = %d: %s", resp.StatusCode, body)
	}
	var antes struct {
		ContentEmUso string `json:"content_base_url_em_uso"`
	}
	if err := json.Unmarshal(body, &antes); err != nil {
		t.Fatalf("decodificando: %v", err)
	}
	if antes.ContentEmUso != "https://painel.exemplo.tld" {
		t.Errorf("sem configuração própria, o conteúdo deveria seguir o painel: %q", antes.ContentEmUso)
	}

	// Com endereço próprio, os dois divergem.
	resp, body = c.do(http.MethodPut, "/api/v1/settings", map[string]string{
		"public_base_url":  "https://painel.exemplo.tld",
		"content_base_url": "https://tv.exemplo.tld/",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /settings = %d: %s", resp.StatusCode, body)
	}

	_, body = c.do(http.MethodGet, "/api/v1/settings", nil)
	var depois struct {
		PainelEmUso  string `json:"public_base_url_em_uso"`
		ContentEmUso string `json:"content_base_url_em_uso"`
	}
	if err := json.Unmarshal(body, &depois); err != nil {
		t.Fatalf("decodificando: %v", err)
	}
	if depois.PainelEmUso != "https://painel.exemplo.tld" {
		t.Errorf("painel = %q", depois.PainelEmUso)
	}
	// A barra final é normalizada: sem isso os links sairiam com "//".
	if depois.ContentEmUso != "https://tv.exemplo.tld" {
		t.Errorf("conteúdo = %q, esperava https://tv.exemplo.tld", depois.ContentEmUso)
	}

	// Endereço malformado é recusado.
	if resp, _ := c.do(http.MethodPut, "/api/v1/settings", map[string]string{
		"public_base_url":  "https://painel.exemplo.tld",
		"content_base_url": "tv.exemplo.tld",
	}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("endereço sem esquema = %d, esperava 400", resp.StatusCode)
	}
}
