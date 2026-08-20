package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vodmanager/internal/api"
	"vodmanager/internal/auth"
	"vodmanager/internal/bootstrap"
	"vodmanager/internal/edge"
	"vodmanager/internal/metrics"
	"vodmanager/internal/store"
)

const (
	adminUser = "admin"
	adminPass = "senha-do-admin-de-teste"
	cookie    = "vodm_session"
)

type apiClient struct {
	t    *testing.T
	base string
	http *http.Client
}

// newAPI sobe a API completa contra o banco de teste e devolve um cliente HTTP real.
func newAPI(t *testing.T) (*apiClient, *testEnv) {
	t.Helper()
	env := newTestEnv(t)

	if _, err := bootstrap.EnsureAdmin(t.Context(), env.Store, env.Log, adminUser, adminPass); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}

	server := api.NewServer(api.Deps{
		Store:  env.Store,
		Auth:   auth.NewService(env.Store, env.Log, auth.Options{SessionTTL: time.Hour, LoginMaxAttempts: 5, LoginWindow: time.Minute}),
		Crypto: env.Crypto,
		// Sem isto os handlers de credencial de saída respondem 503: eles existem só
		// quando o processo serve streaming.
		StreamAuth: edge.NewAuthenticator(env.Store, chaveDeTeste(t)),
		Log:        env.Log,
		Metrics:    metrics.New("node-teste", "all", "test"),
		NodeID:     "node-teste",
		Version:    "test",
	})
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &apiClient{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 10 * time.Second}}, env
}

func (c *apiClient) do(method, path string, body any) (*http.Response, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("montando requisição: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("lendo corpo: %v", err)
	}
	return resp, raw
}

func (c *apiClient) login(user, pass string) *http.Response {
	c.t.Helper()
	resp, _ := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": user, "password": pass})
	return resp
}

func (c *apiClient) loginOK() {
	c.t.Helper()
	if resp := c.login(adminUser, adminPass); resp.StatusCode != http.StatusOK {
		c.t.Fatalf("login falhou: status %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------

func TestHealthzEReadyz(t *testing.T) {
	c, _ := newAPI(t)

	resp, body := c.do(http.MethodGet, "/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d: %s", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodGet, "/readyz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"database":"ok"`) {
		t.Errorf("readyz não confirmou o banco: %s", body)
	}
}

func TestMetricsExpostas(t *testing.T) {
	c, _ := newAPI(t)
	c.do(http.MethodGet, "/healthz", nil)

	resp, body := c.do(http.MethodGet, "/metrics", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics = %d", resp.StatusCode)
	}
	for _, want := range []string{"vodm_http_requests_total", "vodm_build_info"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("métrica %q ausente", want)
		}
	}
}

func TestLoginFluxoCompleto(t *testing.T) {
	c, _ := newAPI(t)

	// Sem sessão, /auth/me é 401.
	if resp, _ := c.do(http.MethodGet, "/api/v1/auth/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me sem sessão = %d, esperava 401", resp.StatusCode)
	}

	// Senha errada é 401 e não deve revelar se o usuário existe.
	resp, body := c.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": adminUser, "password": "errada"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("senha errada = %d, esperava 401", resp.StatusCode)
	}
	respInexistente, bodyInexistente := c.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "nao-existe", "password": "errada"})
	if respInexistente.StatusCode != resp.StatusCode || string(bodyInexistente) != string(body) {
		t.Error("a resposta diferencia usuário inexistente de senha errada")
	}

	// Login correto define cookie httpOnly.
	resp = c.login(adminUser, adminPass)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	var achou bool
	for _, ck := range resp.Cookies() {
		if ck.Name == cookie {
			achou = true
			if !ck.HttpOnly {
				t.Error("o cookie de sessão precisa ser HttpOnly")
			}
			if ck.Value == "" {
				t.Error("cookie de sessão vazio")
			}
		}
	}
	if !achou {
		t.Fatal("login não definiu o cookie de sessão")
	}

	// Agora /auth/me responde.
	resp, body = c.do(http.MethodGet, "/api/v1/auth/me", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me = %d: %s", resp.StatusCode, body)
	}
	var me struct {
		User    struct{ Username, Role string } `json:"user"`
		AuthVia string                          `json:"auth_via"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("unmarshal me: %v", err)
	}
	if me.User.Username != adminUser || me.User.Role != store.RoleAdmin || me.AuthVia != "session" {
		t.Fatalf("me inesperado: %+v", me)
	}

	// Logout invalida a sessão.
	if resp, _ := c.do(http.MethodPost, "/api/v1/auth/logout", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	if resp, _ := c.do(http.MethodGet, "/api/v1/auth/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me após logout = %d, esperava 401", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	c, _ := newAPI(t)

	var ultimo *http.Response
	for i := 0; i < 6; i++ {
		ultimo = c.login(adminUser, "senha-errada")
	}
	if ultimo.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("após 6 tentativas: status %d, esperava 429", ultimo.StatusCode)
	}
	if ultimo.Header.Get("Retry-After") == "" {
		t.Error("resposta 429 deveria trazer Retry-After")
	}
	// Mesmo com a senha certa, o bloqueio vale.
	if resp := c.login(adminUser, adminPass); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("bloqueio deveria valer para a senha certa também, status %d", resp.StatusCode)
	}
}

func TestFontesCRUDViaAPI(t *testing.T) {
	c, _ := newAPI(t)
	c.loginOK()

	resp, body := c.do(http.MethodPost, "/api/v1/sources", map[string]any{
		"name": "Fonte A", "kind": "xtream", "base_url": "http://exemplo.tld:8080",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("criar fonte = %d: %s", resp.StatusCode, body)
	}
	var criada store.Source
	if err := json.Unmarshal(body, &criada); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if criada.ID == 0 || criada.Priority != 1 {
		t.Fatalf("fonte criada inesperada: %+v", criada)
	}

	// Nome duplicado é 409.
	if resp, _ := c.do(http.MethodPost, "/api/v1/sources", map[string]any{
		"name": "Fonte A", "kind": "m3u", "base_url": "http://outro.tld",
	}); resp.StatusCode != http.StatusConflict {
		t.Errorf("nome duplicado = %d, esperava 409", resp.StatusCode)
	}

	// Validações de entrada são 400 e apontam os campos.
	resp, body = c.do(http.MethodPost, "/api/v1/sources", map[string]any{
		"name": "", "kind": "torrent", "base_url": "nao-e-url",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("payload inválido = %d, esperava 400", resp.StatusCode)
	}
	for _, campo := range []string{"name", "kind", "base_url"} {
		if !strings.Contains(string(body), campo) {
			t.Errorf("erro não aponta o campo %q: %s", campo, body)
		}
	}

	// Campo desconhecido é recusado (evita limite ignorado em silêncio).
	if resp, _ := c.do(http.MethodPost, "/api/v1/sources", map[string]any{
		"name": "Fonte X", "kind": "m3u", "base_url": "http://x.tld", "max_conexoes": 10,
	}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("campo desconhecido = %d, esperava 400", resp.StatusCode)
	}

	// PATCH parcial.
	resp, body = c.do(http.MethodPatch, "/api/v1/sources/"+itoa(criada.ID), map[string]any{
		"description": "fonte de teste", "max_connections": 8,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d: %s", resp.StatusCode, body)
	}
	var atualizada store.Source
	json.Unmarshal(body, &atualizada)
	if atualizada.Description != "fonte de teste" || atualizada.MaxConnections != 8 {
		t.Fatalf("patch não aplicado: %+v", atualizada)
	}
	if atualizada.Name != "Fonte A" {
		t.Errorf("patch parcial apagou o nome: %q", atualizada.Name)
	}

	// Reordenação.
	segunda := criarFonteAPI(t, c, "Fonte B")
	resp, body = c.do(http.MethodPost, "/api/v1/sources/reorder", map[string]any{
		"ids": []int64{segunda.ID, criada.ID},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reorder = %d: %s", resp.StatusCode, body)
	}
	var lista struct {
		Sources []store.Source `json:"sources"`
	}
	json.Unmarshal(body, &lista)
	if len(lista.Sources) != 2 || lista.Sources[0].Name != "Fonte B" {
		t.Fatalf("ordem após reorder: %+v", lista.Sources)
	}

	// Lista parcial é 400.
	if resp, _ := c.do(http.MethodPost, "/api/v1/sources/reorder", map[string]any{
		"ids": []int64{criada.ID},
	}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("reorder parcial = %d, esperava 400", resp.StatusCode)
	}

	// DELETE e 404 subsequente.
	if resp, _ := c.do(http.MethodDelete, "/api/v1/sources/"+itoa(segunda.ID), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if resp, _ := c.do(http.MethodGet, "/api/v1/sources/"+itoa(segunda.ID), nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("get após delete = %d, esperava 404", resp.StatusCode)
	}
}

// Este é o teste que sustenta a promessa da D7: a credencial de uma fonte não pode
// aparecer em NENHUMA resposta da API. Na Fase 7 ele será estendido para as respostas
// públicas (M3U de saída e API de catálogo).
func TestCredencialDaFonteNuncaVoltaEmRespostaDaAPI(t *testing.T) {
	c, env := newAPI(t)
	c.loginOK()

	src := criarFonteAPI(t, c, "Fonte com segredo")
	const senhaFonte = "SENHA-ULTRA-SECRETA-DA-FONTE"
	const usuarioFonte = "usuario-da-fonte"

	resp, body := c.do(http.MethodPut, "/api/v1/sources/"+itoa(src.ID)+"/credentials", map[string]any{
		"username": usuarioFonte, "password": senhaFonte,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("gravar credencial = %d: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), senhaFonte) {
		t.Fatal("a resposta de gravação devolveu a senha da fonte")
	}

	// Varre todas as respostas de leitura da API procurando a senha.
	rotas := []string{
		"/api/v1/sources",
		"/api/v1/sources/" + itoa(src.ID),
		"/api/v1/events",
		"/api/v1/auth/me",
		"/healthz",
		"/readyz",
		"/metrics",
	}
	for _, rota := range rotas {
		_, corpo := c.do(http.MethodGet, rota, nil)
		if strings.Contains(string(corpo), senhaFonte) {
			t.Errorf("a senha da fonte vazou em %s", rota)
		}
	}

	// A credencial existe e está cifrada.
	var lista struct {
		Sources []store.Source `json:"sources"`
	}
	_, corpo := c.do(http.MethodGet, "/api/v1/sources", nil)
	json.Unmarshal(corpo, &lista)
	if len(lista.Sources) != 1 || !lista.Sources[0].HasCredentials {
		t.Fatalf("has_credentials deveria ser true: %s", corpo)
	}
	var raw []byte
	if err := env.Pool.QueryRow(t.Context(),
		`SELECT secret_enc FROM source_credentials WHERE source_id = $1`, src.ID).Scan(&raw); err != nil {
		t.Fatalf("lendo credencial: %v", err)
	}
	if bytes.Contains(raw, []byte(senhaFonte)) {
		t.Fatal("a senha da fonte está em claro no banco")
	}

	// Remoção.
	if resp, _ := c.do(http.MethodDelete, "/api/v1/sources/"+itoa(src.ID)+"/credentials", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remover credencial = %d", resp.StatusCode)
	}
	if resp, _ := c.do(http.MethodDelete, "/api/v1/sources/"+itoa(src.ID)+"/credentials", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("remover credencial inexistente = %d, esperava 404", resp.StatusCode)
	}
}

func TestPapelViewerNaoEscreve(t *testing.T) {
	c, env := newAPI(t)

	hash, err := auth.HashPassword("senha-do-viewer-teste")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := env.Store.CreateUser(t.Context(), "leitor", hash, store.RoleViewer); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if resp := c.login("leitor", "senha-do-viewer-teste"); resp.StatusCode != http.StatusOK {
		t.Fatalf("login do viewer = %d", resp.StatusCode)
	}

	// Leitura permitida.
	if resp, _ := c.do(http.MethodGet, "/api/v1/sources", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("viewer deveria poder listar fontes, status %d", resp.StatusCode)
	}
	// Escrita negada.
	if resp, _ := c.do(http.MethodPost, "/api/v1/sources", map[string]any{
		"name": "Proibida", "kind": "m3u", "base_url": "http://x.tld",
	}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer criando fonte = %d, esperava 403", resp.StatusCode)
	}
}

func TestTokenDeAPIAutentica(t *testing.T) {
	c, env := newAPI(t)

	user, err := env.Store.GetUserByUsername(t.Context(), adminUser)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	tok, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if _, err := env.Store.CreateAPIToken(t.Context(), user.ID, "ci", tok.Prefix, tok.Hash, nil); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, c.base+"/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Plain)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("requisição com token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token válido = %d, esperava 200", resp.StatusCode)
	}
	corpo, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(corpo), `"auth_via":"api_token"`) {
		t.Errorf("auth_via inesperado: %s", corpo)
	}

	// Token inválido é 401.
	req, _ = http.NewRequest(http.MethodGet, c.base+"/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer token-que-nao-existe")
	resp2, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("requisição com token inválido: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("token inválido = %d, esperava 401", resp2.StatusCode)
	}
}

func TestEventosSaoRegistradosPelaAPI(t *testing.T) {
	c, _ := newAPI(t)
	c.loginOK()
	criarFonteAPI(t, c, "Fonte auditada")

	_, body := c.do(http.MethodGet, "/api/v1/events?category=source", nil)
	var resposta struct {
		Events []store.Event `json:"events"`
	}
	if err := json.Unmarshal(body, &resposta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resposta.Events) == 0 {
		t.Fatal("nenhum evento de fonte registrado")
	}
	e := resposta.Events[0]
	if e.Actor == nil || *e.Actor != adminUser {
		t.Errorf("actor do evento = %v, esperava %q", e.Actor, adminUser)
	}
	if e.NodeID != "node-teste" {
		t.Errorf("node_id do evento = %q", e.NodeID)
	}

	// Query inválida é 400.
	if resp, _ := c.do(http.MethodGet, "/api/v1/events?limit=abc", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("limit inválido = %d, esperava 400", resp.StatusCode)
	}
}

func TestRotasInexistentesEMetodoErrado(t *testing.T) {
	c, _ := newAPI(t)
	c.loginOK()

	if resp, _ := c.do(http.MethodGet, "/api/v1/nao-existe", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("rota inexistente = %d", resp.StatusCode)
	}
	if resp, _ := c.do(http.MethodDelete, "/healthz", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("método errado = %d, esperava 405", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------

func criarFonteAPI(t *testing.T, c *apiClient, name string) store.Source {
	t.Helper()
	resp, body := c.do(http.MethodPost, "/api/v1/sources", map[string]any{
		"name": name, "kind": "m3u", "base_url": "http://exemplo.tld/lista.m3u",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("criando fonte %q: status %d: %s", name, resp.StatusCode, body)
	}
	var s store.Source
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return s
}

func itoa(id int64) string {
	return strings.TrimSpace(jsonNumber(id))
}

func jsonNumber(id int64) string {
	raw, _ := json.Marshal(id)
	return string(raw)
}

// TestConfiguracaoEnderecoPublico cobre o ajuste que faz os links funcionarem fora da
// máquina: sem ele o endereço sai como localhost e o XC_VM não alcança nada.
func TestConfiguracaoEnderecoPublico(t *testing.T) {
	c, _ := newAPI(t)
	c.loginOK()

	// Sem configuração, o endereço vem da requisição — e no teste ele é local.
	resp, body := c.do(http.MethodGet, "/api/v1/settings", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /settings = %d: %s", resp.StatusCode, body)
	}
	var antes struct {
		PublicBaseURL string `json:"public_base_url"`
		EmUso         string `json:"public_base_url_em_uso"`
		ELocal        bool   `json:"public_base_url_e_local"`
	}
	if err := json.Unmarshal(body, &antes); err != nil {
		t.Fatalf("decodificando: %v", err)
	}
	if antes.PublicBaseURL != "" {
		t.Errorf("sem configuração o valor guardado deveria ser vazio, veio %q", antes.PublicBaseURL)
	}
	if !antes.ELocal {
		t.Errorf("endereço de teste %q deveria ser detectado como local", antes.EmUso)
	}

	// Endereço malformado não pode ser aceito: gravá-lo quebraria todos os links.
	for _, ruim := range []string{"192.168.1.50:8080", "ftp://x", "http://"} {
		resp, _ := c.do(http.MethodPut, "/api/v1/settings",
			map[string]string{"public_base_url": ruim})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("PUT /settings com %q = %d, queria 400", ruim, resp.StatusCode)
		}
	}

	// Gravação válida: a barra final é normalizada para não gerar link com "//".
	resp, body = c.do(http.MethodPut, "/api/v1/settings",
		map[string]string{"public_base_url": "http://198.51.100.10:8080/"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /settings = %d: %s", resp.StatusCode, body)
	}

	resp, body = c.do(http.MethodGet, "/api/v1/settings", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /settings = %d: %s", resp.StatusCode, body)
	}
	var depois struct {
		PublicBaseURL string `json:"public_base_url"`
		EmUso         string `json:"public_base_url_em_uso"`
		ELocal        bool   `json:"public_base_url_e_local"`
	}
	if err := json.Unmarshal(body, &depois); err != nil {
		t.Fatalf("decodificando: %v", err)
	}
	if want := "http://198.51.100.10:8080"; depois.PublicBaseURL != want {
		t.Errorf("guardado = %q, queria %q", depois.PublicBaseURL, want)
	}
	if depois.EmUso != depois.PublicBaseURL {
		t.Errorf("em uso = %q, deveria ser o valor guardado %q", depois.EmUso, depois.PublicBaseURL)
	}
	if depois.ELocal {
		t.Error("IP público foi classificado como endereço local")
	}

	// Vazio volta ao endereço da requisição, sem deixar o sistema sem link.
	if resp, body := c.do(http.MethodPut, "/api/v1/settings",
		map[string]string{"public_base_url": ""}); resp.StatusCode != http.StatusOK {
		t.Fatalf("limpando configuração = %d: %s", resp.StatusCode, body)
	}
	_, body = c.do(http.MethodGet, "/api/v1/settings", nil)
	if !strings.Contains(string(body), `"public_base_url_e_local":true`) {
		t.Errorf("após limpar, deveria voltar ao endereço local da requisição: %s", body)
	}
}

// TestTrocaDeSenhaDoPainel cobre o caminho completo: senha atual conferida, sessões
// encerradas, e a senha nova valendo já no login seguinte.
func TestTrocaDeSenhaDoPainel(t *testing.T) {
	c, _ := newAPI(t)
	c.loginOK()

	const novaSenha = "outra-senha-bem-longa-do-admin"

	// Senha atual errada não troca nada.
	resp, body := c.do(http.MethodPost, "/api/v1/auth/change-password",
		map[string]string{"current_password": "chute-errado", "new_password": novaSenha})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("com senha atual errada = %d, esperava 403: %s", resp.StatusCode, body)
	}

	// Senha nova curta demais é recusada pela política de senha.
	resp, _ = c.do(http.MethodPost, "/api/v1/auth/change-password",
		map[string]string{"current_password": adminPass, "new_password": "curta"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("com senha curta = %d, esperava 400", resp.StatusCode)
	}

	// Repetir a senha atual não é troca.
	resp, _ = c.do(http.MethodPost, "/api/v1/auth/change-password",
		map[string]string{"current_password": adminPass, "new_password": adminPass})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("repetindo a senha atual = %d, esperava 400", resp.StatusCode)
	}

	// A troca de verdade.
	resp, body = c.do(http.MethodPost, "/api/v1/auth/change-password",
		map[string]string{"current_password": adminPass, "new_password": novaSenha})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("troca = %d: %s", resp.StatusCode, body)
	}

	// A sessão anterior caiu junto: é o ponto da troca de senha.
	if resp, _ := c.do(http.MethodGet, "/api/v1/auth/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("sessão antiga continuou válida: status %d", resp.StatusCode)
	}
	// A senha antiga não entra mais; a nova entra.
	if resp := c.login(adminUser, adminPass); resp.StatusCode == http.StatusOK {
		t.Error("a senha antiga continuou funcionando")
	}
	if resp := c.login(adminUser, novaSenha); resp.StatusCode != http.StatusOK {
		t.Fatalf("a senha nova não funcionou: status %d", resp.StatusCode)
	}
}

// A gestão de usuários tem uma armadilha que só aparece quando é tarde: deixar o sistema
// sem nenhum administrador. Não há tela de recuperação — a correção exigiria mexer no
// banco à mão.
func TestGestaoDeUsuarios(t *testing.T) {
	c, _ := newAPI(t)
	c.loginOK()

	// Criar um sócio com papel de leitura.
	resp, body := c.do(http.MethodPost, "/api/v1/users", map[string]string{
		"username": "socio", "password": "senha-do-socio-longa", "role": "viewer",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("criação = %d: %s", resp.StatusCode, body)
	}
	var criado struct {
		ID   int64  `json:"id"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(body, &criado); err != nil {
		t.Fatalf("decodificando: %v", err)
	}
	if criado.Role != "viewer" {
		t.Errorf("papel = %q", criado.Role)
	}
	if bytes.Contains(body, []byte("password_hash")) {
		t.Error("a resposta expôs o hash da senha")
	}

	// Nome repetido não passa.
	resp, _ = c.do(http.MethodPost, "/api/v1/users", map[string]string{
		"username": "socio", "password": "outra-senha-longa", "role": "operator",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("usuário repetido = %d, esperava 409", resp.StatusCode)
	}

	// Papel inválido não passa.
	resp, _ = c.do(http.MethodPost, "/api/v1/users", map[string]string{
		"username": "outro", "password": "senha-bem-longa-mesmo", "role": "chefe",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("papel inválido = %d, esperava 400", resp.StatusCode)
	}

	// O sócio entra, mas não gerencia usuários nem cria fontes.
	socio, _ := newAPIClientFor(t, c.base)
	if resp := socio.login("socio", "senha-do-socio-longa"); resp.StatusCode != http.StatusOK {
		t.Fatalf("o sócio não conseguiu entrar: %d", resp.StatusCode)
	}
	if resp, _ := socio.do(http.MethodGet, "/api/v1/users", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer acessou a lista de usuários: %d", resp.StatusCode)
	}
	if resp, _ := socio.do(http.MethodGet, "/api/v1/contents?type=movie", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("viewer não conseguiu ver o catálogo: %d", resp.StatusCode)
	}

	// Desabilitar corta o acesso e derruba a sessão aberta.
	if resp, body := c.do(http.MethodPatch, fmt.Sprintf("/api/v1/users/%d", criado.ID),
		map[string]any{"enabled": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("desabilitando = %d: %s", resp.StatusCode, body)
	}
	if resp, _ := socio.do(http.MethodGet, "/api/v1/auth/me", nil); resp.StatusCode == http.StatusOK {
		t.Error("a sessão do usuário desabilitado continuou válida")
	}
	if resp := socio.login("socio", "senha-do-socio-longa"); resp.StatusCode == http.StatusOK {
		t.Error("usuário desabilitado conseguiu entrar")
	}
}

// O invariante que protege o sistema de ficar sem dono.
func TestNaoDeixaOSistemaSemAdministrador(t *testing.T) {
	c, _ := newAPI(t)
	c.loginOK()

	admins, body := c.do(http.MethodGet, "/api/v1/users", nil)
	if admins.StatusCode != http.StatusOK {
		t.Fatalf("listando = %d: %s", admins.StatusCode, body)
	}
	var lista struct {
		Users []struct {
			ID   int64  `json:"id"`
			Role string `json:"role"`
		} `json:"users"`
		Eu int64 `json:"eu"`
	}
	if err := json.Unmarshal(body, &lista); err != nil {
		t.Fatalf("decodificando: %v", err)
	}

	// Rebaixar o único administrador precisa ser recusado.
	resp, _ := c.do(http.MethodPatch, fmt.Sprintf("/api/v1/users/%d", lista.Eu),
		map[string]any{"role": "viewer"})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("rebaixando o único admin = %d, esperava 409", resp.StatusCode)
	}
	// Desabilitar também.
	resp, _ = c.do(http.MethodPatch, fmt.Sprintf("/api/v1/users/%d", lista.Eu),
		map[string]any{"enabled": false})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("desabilitando o único admin = %d, esperava 409", resp.StatusCode)
	}
	// Remover a própria conta também não.
	resp, _ = c.do(http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", lista.Eu), nil)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("removendo a si mesmo = %d, esperava 409", resp.StatusCode)
	}

	// Com um segundo administrador, o rebaixamento passa a ser permitido.
	if resp, body := c.do(http.MethodPost, "/api/v1/users", map[string]string{
		"username": "admin2", "password": "senha-do-segundo-admin", "role": "admin",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("criando segundo admin = %d: %s", resp.StatusCode, body)
	}
	if resp, body := c.do(http.MethodPatch, fmt.Sprintf("/api/v1/users/%d", lista.Eu),
		map[string]any{"role": "viewer"}); resp.StatusCode != http.StatusOK {
		t.Errorf("com dois admins o rebaixamento deveria passar: %d %s", resp.StatusCode, body)
	}
}

// newAPIClientFor devolve um cliente novo, com sessão própria, contra o mesmo servidor.
func newAPIClientFor(t *testing.T, base string) (*apiClient, error) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &apiClient{t: t, base: base, http: &http.Client{Jar: jar, Timeout: 10 * time.Second}}, nil
}
