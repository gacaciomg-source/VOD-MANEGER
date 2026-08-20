package api

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"vodmanager/internal/auth"
	"vodmanager/internal/cryptobox"
	"vodmanager/internal/edge"
	"vodmanager/internal/store"
)

type novaCredencialRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	ExpiresAt   *string `json:"expires_at"`
	// Username e Password vazios fazem a máquina gerar os dois. Preenchidos, valem o que
	// o administrador escolheu — é o caso de quem já vende acesso e quer manter o login
	// que o cliente conhece.
	Username string `json:"username"`
	Password string `json:"password"`
}

// Limites do que o administrador pode escolher como usuário e senha.
//
// O usuário viaja dentro do caminho da URL, então não pode conter barra nem nada que
// precise de escape: um caractere errado aqui produz um link que falha em silêncio.
const (
	minSenhaStream   = 8
	maxUsuarioStream = 64
	maxSenhaStream   = 128
)

var usuarioStreamValido = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validarCredenciaisEscolhidas confere usuário e senha informados pelo administrador.
func validarCredenciaisEscolhidas(usuario, senha string) (string, string) {
	if usuario != "" {
		if len(usuario) > maxUsuarioStream {
			return "o usuário pode ter no máximo 64 caracteres", "username"
		}
		if !usuarioStreamValido.MatchString(usuario) {
			return "o usuário aceita apenas letras, números, ponto, hífen e sublinhado — " +
				"ele viaja dentro do endereço do vídeo", "username"
		}
	}
	if senha != "" {
		if len([]rune(senha)) < minSenhaStream {
			return "a senha precisa ter ao menos 8 caracteres", "password"
		}
		if len(senha) > maxSenhaStream {
			return "a senha pode ter no máximo 128 caracteres", "password"
		}
		if !usuarioStreamValido.MatchString(senha) {
			return "a senha aceita apenas letras, números, ponto, hífen e sublinhado — " +
				"ela viaja dentro do endereço do vídeo", "password"
		}
	}
	return "", ""
}

// cifrarSenhaDeStream guarda a senha de forma recuperável.
//
// Diferente de uma senha de pessoa, esta é lida por máquina e precisa ser entregue pronta
// ao cliente dentro de uma URL. Cifrada com a mesma chave mestra das credenciais das
// fontes, e com AAD ligado ao usuário dono dela.
func (s *Server) cifrarSenhaDeStream(username, senha string) ([]byte, error) {
	if s.deps.Crypto == nil {
		return nil, nil
	}
	return s.deps.Crypto.Seal([]byte(senha), cryptobox.StreamCredentialAAD(username))
}

// senhaDeStream recupera a senha em claro de uma credencial.
//
// Devolve vazio — sem erro — para credenciais criadas antes da migração 0006, que só
// têm o HMAC. Nesse caso o painel oferece trocar a senha em vez de mostrar o link.
func (s *Server) senhaDeStream(c *store.StreamCredential) string {
	if s.deps.Crypto == nil || len(c.PasswordEnc) == 0 {
		return ""
	}
	claro, err := s.deps.Crypto.Open(c.PasswordEnc, cryptobox.StreamCredentialAAD(c.Username))
	if err != nil {
		s.deps.Log.Warn("não foi possível decifrar a senha da credencial",
			"credencial", c.Name, "erro", err)
		return ""
	}
	return string(claro)
}

// handleCreateStreamCredential cria uma credencial de SAÍDA.
//
// Usuário e senha podem ser escolhidos pelo administrador — é o caso de quem já vende
// acesso e quer manter o login que o cliente conhece. Em branco, a máquina gera os dois
// com 32 bytes de entropia.
//
// A senha fica guardada em duas formas: HMAC, que autentica cada requisição de vídeo sem
// decifrar nada, e cifrada, para o painel conseguir montar o link pronto depois.
func (s *Server) handleCreateStreamCredential(w http.ResponseWriter, r *http.Request) {
	if s.deps.StreamAuth == nil {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "streaming_disabled",
			"este processo não gerencia credenciais de streaming")
		return
	}
	var req novaCredencialRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "informe um nome", "name")
		return
	}

	var expira *time.Time
	if req.ExpiresAt != nil && strings.TrimSpace(*req.ExpiresAt) != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
				"expires_at precisa estar em RFC3339", "expires_at")
			return
		}
		expira = &t
	}

	username := strings.TrimSpace(req.Username)
	senhaClara := strings.TrimSpace(req.Password)
	if msg, campo := validarCredenciaisEscolhidas(username, senhaClara); msg != "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", msg, campo)
		return
	}

	// O que o administrador não escolheu, a máquina gera com entropia de verdade.
	if username == "" {
		gerado, err := auth.NewToken()
		if err != nil {
			s.fail(w, r, err, "gerando usuário")
			return
		}
		// Curto o bastante para caber numa URL sem ficar ilegível.
		username = "vodm_" + gerado.Plain[:12]
	}
	if senhaClara == "" {
		gerada, err := auth.NewToken()
		if err != nil {
			s.fail(w, r, err, "gerando senha")
			return
		}
		senhaClara = gerada.Plain
	}

	cifrada, err := s.cifrarSenhaDeStream(username, senhaClara)
	if err != nil {
		s.fail(w, r, err, "protegendo a senha")
		return
	}

	var criador int64
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		criador = p.User.ID
	}

	cred, err := s.deps.Store.CreateStreamCredential(r.Context(),
		strings.TrimSpace(req.Name), req.Description, username,
		s.deps.StreamAuth.HashSenha(senhaClara), cifrada, criador, expira)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, s.deps.Log, http.StatusConflict, "username_em_uso",
				"já existe uma credencial com este usuário", "username")
			return
		}
		s.fail(w, r, err, "criando credencial de streaming")
		return
	}

	s.logEvent(r, "stream", "info", "credencial de streaming criada: "+cred.Name, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusCreated, map[string]any{
		"credential": cred,
		"username":   username,
		"password":   senhaClara,
	})
}

func (s *Server) handleListStreamCredentials(w http.ResponseWriter, r *http.Request) {
	creds, err := s.deps.Store.ListStreamCredentials(r.Context())
	if err != nil {
		s.fail(w, r, err, "listando credenciais de streaming")
		return
	}

	// Quantas reproduções cada credencial tem AGORA. Vem da memória do processo que
	// serve os bytes, não do banco.
	ativas := map[int64]int{}
	if s.deps.StreamProxy != nil {
		ativas = s.deps.StreamProxy.Conexoes().Snapshot()
	}
	saida := make([]map[string]any, 0, len(creds))
	for i := range creds {
		saida = append(saida, map[string]any{
			"credential":         creds[i],
			"active_connections": ativas[creds[i].ID],
		})
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"credentials": saida})
}

type trocaSenhaRequest struct {
	// Password vazia faz a máquina gerar uma nova.
	Password string `json:"password"`
}

// handleRotateStreamCredential troca a senha mantendo o mesmo usuário.
//
// Os links já entregues continuam com o mesmo usuário: só a senha muda. É o caminho para
// cortar quem compartilhou o acesso sem refazer o cadastro do cliente.
func (s *Server) handleRotateStreamCredential(w http.ResponseWriter, r *http.Request) {
	if s.deps.StreamAuth == nil {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "streaming_disabled",
			"este processo não gerencia credenciais de streaming")
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}

	// Corpo é opcional aqui: sem ele, a senha é gerada.
	var req trocaSenhaRequest
	_ = decodeJSON(w, r, &req)

	nova := strings.TrimSpace(req.Password)
	if msg, campo := validarCredenciaisEscolhidas("", nova); msg != "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", msg, campo)
		return
	}
	if nova == "" {
		gerada, err := auth.NewToken()
		if err != nil {
			s.fail(w, r, err, "gerando senha")
			return
		}
		nova = gerada.Plain
	}

	// Precisamos do usuário para o AAD: a senha cifrada fica ligada a ele.
	atual, err := s.credencialPorID(r, id)
	if err != nil {
		s.fail(w, r, err, "buscando credencial")
		return
	}
	cifrada, err := s.cifrarSenhaDeStream(atual.Username, nova)
	if err != nil {
		s.fail(w, r, err, "protegendo a senha")
		return
	}

	cred, err := s.deps.Store.RotateStreamCredentialPassword(r.Context(), id,
		s.deps.StreamAuth.HashSenha(nova), cifrada)
	if err != nil {
		s.fail(w, r, err, "trocando senha da credencial")
		return
	}
	// A senha antiga precisa parar de valer imediatamente.
	s.deps.StreamAuth.Invalidar(cred.Username)

	s.logEvent(r, "stream", "warn", "senha da credencial trocada: "+cred.Name, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"credential": cred,
		"username":   cred.Username,
		"password":   nova,
		"aviso":      "A senha anterior parou de funcionar e quem estava assistindo foi desconectado.",
	})
}

// credencialPorID busca uma credencial de saída na lista.
//
// São poucas dezenas de linhas e a listagem já está pronta e testada; uma consulta
// dedicada só para isto seria código a mais para manter sem ganho mensurável.
func (s *Server) credencialPorID(r *http.Request, id int64) (*store.StreamCredential, error) {
	creds, err := s.deps.Store.ListStreamCredentials(r.Context())
	if err != nil {
		return nil, err
	}
	for i := range creds {
		if creds[i].ID == id {
			return &creds[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// handleCredentialLinks devolve os endereços prontos de uma credencial.
//
// É o único lugar onde a senha em claro volta a aparecer depois da criação. Exige papel
// de escrita e é registrado em evento — como acontece com a URL de origem de uma variante.
func (s *Server) handleCredentialLinks(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	cred, err := s.credencialPorID(r, id)
	if err != nil {
		s.fail(w, r, err, "buscando credencial")
		return
	}

	base := s.baseURLConteudo(r)
	senha := s.senhaDeStream(cred)

	resposta := map[string]any{
		"credential_id":    cred.ID,
		"name":             cred.Name,
		"username":         cred.Username,
		"password":         senha,
		"base_url":         base,
		"base_url_e_local": enderecoLocal(base),
		// Falso para credenciais criadas antes de a senha passar a ser recuperável. O
		// painel usa isto para oferecer a troca de senha em vez de um link incompleto.
		"senha_disponivel": senha != "",
		"ativa":            cred.Ativa(time.Now()),
	}

	if senha != "" {
		q := url.Values{"username": {cred.Username}, "password": {senha}}
		resposta["m3u_url"] = base + "/get.php?" + q.Encode() + "&type=m3u_plus&output=mp4"
		resposta["m3u_filmes_url"] = base + "/get.php?" + q.Encode() + "&conteudo=filmes"
		resposta["m3u_series_url"] = base + "/get.php?" + q.Encode() + "&conteudo=series"
		resposta["xtream_url"] = base + "/player_api.php?" + q.Encode()
		s.logEvent(r, "stream", "info",
			"links de acesso exibidos para a credencial: "+cred.Name, actorOf(r), nil)
	}
	writeJSON(w, s.deps.Log, http.StatusOK, resposta)
}

type patchCredencialRequest struct {
	Name           *string   `json:"name"`
	Description    *string   `json:"description"`
	Enabled        *bool     `json:"enabled"`
	MaxConnections *int      `json:"max_connections"`
	AllowedCIDRs   *[]string `json:"allowed_cidrs"`
}

// handleUpdateStreamCredential ajusta nome, limites e restrição de origem.
func (s *Server) handleUpdateStreamCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	var req patchCredencialRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	if req.MaxConnections != nil && *req.MaxConnections <= 0 {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"max_connections precisa ser positivo, ou nulo para sem limite", "max_connections")
		return
	}

	patch := store.StreamCredentialPatch{
		Name: req.Name, Description: req.Description, Enabled: req.Enabled,
		AllowedCIDRs: req.AllowedCIDRs,
	}
	// Presente no corpo = alterar (inclusive para nulo, que significa "sem limite").
	if strings.Contains(bodyKeys(r), "max_connections") || req.MaxConnections != nil {
		patch.MaxConnections = &req.MaxConnections
	}

	cred, err := s.deps.Store.UpdateStreamCredential(r.Context(), id, patch)
	if err != nil {
		s.fail(w, r, err, "atualizando credencial")
		return
	}
	// Os limites novos precisam valer já na próxima requisição de vídeo.
	if s.deps.StreamAuth != nil {
		s.deps.StreamAuth.Invalidar(cred.Username)
	}
	writeJSON(w, s.deps.Log, http.StatusOK, cred)
}

// bodyKeys existe só para o caso do max_connections nulo explícito; o corpo já foi lido,
// então devolvemos vazio e confiamos no ponteiro. Mantido para deixar a intenção clara.
func bodyKeys(*http.Request) string { return "" }

// handleRevokeStreamCredential corta o acesso imediatamente.
func (s *Server) handleRevokeStreamCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}

	creds, err := s.deps.Store.ListStreamCredentials(r.Context())
	if err != nil {
		s.fail(w, r, err, "buscando credencial")
		return
	}
	var username string
	for _, c := range creds {
		if c.ID == id {
			username = c.Username
			break
		}
	}

	if err := s.deps.Store.RevokeStreamCredential(r.Context(), id); err != nil {
		s.fail(w, r, err, "revogando credencial")
		return
	}
	// Invalida o cache do autenticador: sem isso a revogação levaria até o TTL para valer.
	if s.deps.StreamAuth != nil && username != "" {
		s.deps.StreamAuth.Invalidar(username)
	}

	s.logEvent(r, "stream", "warn", "credencial de streaming revogada", actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteStreamCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	if err := s.deps.Store.DeleteStreamCredential(r.Context(), id); err != nil {
		s.fail(w, r, err, "removendo credencial")
		return
	}
	if s.deps.StreamAuth != nil {
		s.deps.StreamAuth.InvalidarTudo()
	}
	s.logEvent(r, "stream", "warn", "credencial de streaming removida", actorOf(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

// handlePlaybackLinks devolve os links públicos de um conteúdo.
//
// É a resposta para "qual link eu cadastro no XC_VM?". Diferente da URL de origem, este
// link aponta para o VOD Manager e nunca revela a fonte.
func (s *Server) handlePlaybackLinks(w http.ResponseWriter, r *http.Request) {
	if s.deps.StreamAuth == nil {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "streaming_disabled",
			"este processo não serve streaming")
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	ehEpisodio := strings.Contains(r.URL.Path, "/episodes/")

	var alvo *store.StreamTarget
	if ehEpisodio {
		alvo, _, err = s.deps.Store.ResolveEpisodeForStream(r.Context(), id)
	} else {
		alvo, _, err = s.deps.Store.ResolveContentForStream(r.Context(), id)
	}
	if err != nil {
		s.fail(w, r, err, "resolvendo conteúdo")
		return
	}

	// Endereço de CONTEÚDO: é este link que vai para o cliente.
	base := s.baseURLConteudo(r)
	resposta := map[string]any{
		"target_id": alvo.ID,
		"title":     alvo.Title,
		"extension": alvo.Extension,
		// O painel usa isto para avisar antes de o administrador colar um link que só
		// funciona na própria máquina.
		"base_url":         base,
		"base_url_e_local": enderecoLocal(base),
	}

	// Link estável por credencial: é o que vai para o XC_VM.
	creds, err := s.deps.Store.ListStreamCredentials(r.Context())
	if err != nil {
		s.fail(w, r, err, "listando credenciais")
		return
	}
	// Só quem pode escrever vê a senha em claro. Um usuário de leitura enxerga o catálogo
	// e os links, mas não as credenciais que dão acesso a eles.
	podeVerSenha := false
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		podeVerSenha = p.User.Role == store.RoleAdmin || p.User.Role == store.RoleOperator
	}

	links := []map[string]any{}
	for i := range creds {
		c := creds[i]
		if !c.Ativa(time.Now()) {
			continue
		}
		// Link pronto quando a senha é recuperável e quem pediu tem papel de escrita.
		// Para um usuário de leitura, ou para credencial anterior à migração 0006,
		// sobra o marcador — melhor que expor a senha a quem não deveria vê-la.
		senha, completo := "SUA_SENHA", false
		if podeVerSenha {
			if clara := s.senhaDeStream(&creds[i]); clara != "" {
				senha, completo = clara, true
			}
		}
		links = append(links, map[string]any{
			"credential_id":   c.ID,
			"credential_name": c.Name,
			"url_template":    edge.LinkPublico(base, c.Username, senha, alvo),
			"username":        c.Username,
			"pronto":          completo,
		})
	}
	resposta["credential_links"] = links

	// Link assinado e temporário: funciona na hora, sem credencial, para testar num player.
	caminho := "/stream/" + strconv.FormatInt(alvo.ID, 10)
	if ehEpisodio {
		caminho = "/stream/e/" + strconv.FormatInt(alvo.ID, 10)
	}
	expira, assinatura := s.deps.StreamAuth.AssinarURL(caminho, 12*time.Hour)
	resposta["temporary_url"] = base + caminho +
		"?exp=" + strconv.FormatInt(expira, 10) + "&sig=" + assinatura
	resposta["temporary_expires_at"] = time.Unix(expira, 0).Format(time.RFC3339)

	writeJSON(w, s.deps.Log, http.StatusOK, resposta)
}

// baseURL descobre o endereço público deste servidor.
//
// Ordem de precedência:
//
//  1. o que o administrador configurou no painel (vale para todos, sem reiniciar);
//  2. a variável de ambiente (para quem prefere fixar no deploy);
//  3. o Host da requisição — que dentro da própria máquina é "localhost" e NÃO funciona
//     em lugar nenhum além dela. É a causa mais comum de "o link não abre no XC_VM".
func (s *Server) baseURL(r *http.Request) string {
	if v, err := s.deps.Store.GetSetting(r.Context(), store.SettingPublicBaseURL, ""); err == nil && v != "" {
		return strings.TrimRight(v, "/")
	}
	if s.deps.PublicBaseURL != "" {
		return strings.TrimRight(s.deps.PublicBaseURL, "/")
	}
	esquema := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		esquema = "https"
	}
	return esquema + "://" + r.Host
}

// enderecoLocal informa se um endereço só funciona dentro da própria máquina.
func enderecoLocal(base string) bool {
	b := strings.ToLower(base)
	return strings.Contains(b, "localhost") ||
		strings.Contains(b, "127.0.0.1") ||
		strings.Contains(b, "://[::1]")
}

type configPublicaRequest struct {
	PublicBaseURL string `json:"public_base_url"`
	// ContentBaseURL vazio faz o conteúdo usar o mesmo endereço do painel.
	ContentBaseURL string `json:"content_base_url"`
}

// handleGetSettings devolve as configurações editáveis pelo painel.
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	guardado, _ := s.deps.Store.GetSetting(r.Context(), store.SettingPublicBaseURL, "")
	conteudoGuardado, _ := s.deps.Store.GetSetting(r.Context(), store.SettingContentBaseURL, "")
	conteudoEmUso := s.baseURLConteudo(r)

	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"public_base_url":          guardado,
		"public_base_url_em_uso":   base,
		"public_base_url_e_local":  enderecoLocal(base),
		"definido_por_ambiente":    s.deps.PublicBaseURL != "",
		"content_base_url":         conteudoGuardado,
		"content_base_url_em_uso":  conteudoEmUso,
		"content_base_url_e_local": enderecoLocal(conteudoEmUso),
	})
}

// handleUpdateSettings grava as configurações editáveis.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req configPublicaRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}

	valor := strings.TrimRight(strings.TrimSpace(req.PublicBaseURL), "/")
	if valor != "" {
		u, err := url.Parse(valor)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
				"informe um endereço completo, como http://198.51.100.10:8080", "public_base_url")
			return
		}
	}

	conteudo := strings.TrimRight(strings.TrimSpace(req.ContentBaseURL), "/")
	if conteudo != "" {
		u, err := url.Parse(conteudo)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
				"informe um endereço completo, como https://tv.seudominio.com", "content_base_url")
			return
		}
	}

	if err := s.deps.Store.SetSetting(r.Context(), store.SettingPublicBaseURL, valor); err != nil {
		s.fail(w, r, err, "gravando configuração")
		return
	}
	if err := s.deps.Store.SetSetting(r.Context(), store.SettingContentBaseURL, conteudo); err != nil {
		s.fail(w, r, err, "gravando configuração")
		return
	}
	s.logEvent(r, "config", "info", "endereços alterados: painel="+valor+" conteúdo="+conteudo, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"ok": true, "public_base_url": valor, "content_base_url": conteudo,
	})
}

func (s *Server) handleListActiveStreams(w http.ResponseWriter, r *http.Request) {
	streams, err := s.deps.Store.ListActiveStreams(r.Context(), 200)
	if err != nil {
		s.fail(w, r, err, "listando streams ativos")
		return
	}
	stats, err := s.deps.Store.GetStreamStats(r.Context())
	if err != nil {
		s.fail(w, r, err, "consultando estatísticas de streaming")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"active": streams, "stats": stats})
}

// baseURLConteudo é o endereço que vai NOS LINKS entregues aos clientes.
//
// Separado do endereço do painel: o link do vídeo chega às mãos do cliente, e com ele o
// cliente descobre onde fica o sistema — inclusive a tela de administração. Com dois
// domínios, o que você entrega não revela por onde você administra.
//
// Sem configuração própria, cai no endereço do painel: é o comportamento de sempre, e
// continua correto para quem só tem um domínio.
func (s *Server) baseURLConteudo(r *http.Request) string {
	if v, err := s.deps.Store.GetSetting(r.Context(), store.SettingContentBaseURL, ""); err == nil && v != "" {
		return strings.TrimRight(v, "/")
	}
	return s.baseURL(r)
}
