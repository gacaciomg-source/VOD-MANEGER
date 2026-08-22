package api

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vodmanager/internal/auth"
	"vodmanager/internal/store"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type userResponse struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	Role        string     `json:"role"`
	Enabled     bool       `json:"enabled"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

func toUserResponse(u *store.User) userResponse {
	return userResponse{
		ID: u.ID, Username: u.Username, Role: u.Role,
		Enabled: u.Enabled, LastLoginAt: u.LastLoginAt, CreatedAt: u.CreatedAt,
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe usuário e senha", "username", "password")
		return
	}

	clientIP := auth.ClientIP(r, s.deps.TrustProxy)
	result, retryAfter, err := s.deps.Auth.Login(r.Context(), req.Username, req.Password, r.UserAgent(), clientIP)
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		s.logEvent(r, "auth", "warn", "login bloqueado por excesso de tentativas", req.Username, nil)
		writeError(w, s.deps.Log, http.StatusTooManyRequests, "rate_limited",
			"tentativas de login em excesso; tente novamente mais tarde")
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.logEvent(r, "auth", "warn", "tentativa de login inválida", req.Username, nil)
		writeError(w, s.deps.Log, http.StatusUnauthorized, "invalid_credentials", "usuário ou senha inválidos")
		return
	case err != nil:
		s.deps.Log.Error("falha no login", "erro", err)
		writeError(w, s.deps.Log, http.StatusInternalServerError, "internal", "erro interno ao autenticar")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     s.deps.CookieName,
		Value:    result.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cookieSeguro(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  result.ExpiresAt,
	})
	s.logEvent(r, "auth", "info", "login bem-sucedido", result.User.Username, nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"user":       toUserResponse(result.User),
		"expires_at": result.ExpiresAt,
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeError(w, s.deps.Log, http.StatusUnauthorized, "unauthenticated", "autenticação necessária")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"user":     toUserResponse(p.User),
		"auth_via": p.Via,
		"node_id":  s.deps.NodeID,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.deps.CookieName); err == nil {
		if err := s.deps.Auth.Logout(r.Context(), c.Value); err != nil {
			s.deps.Log.Warn("falha ao revogar sessão", "erro", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.deps.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cookieSeguro(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	s.logEvent(r, "auth", "info", "logout", actorOf(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

// cookieSeguro decide, por requisição, se o cookie de sessão exige HTTPS.
//
// Por requisição, e não por configuração global, porque as duas portas de entrada convivem
// e sempre vão conviver: o domínio com certificado, e o http://IP:PORTA que nunca se fecha
// para ninguém ficar sem entrada. Uma chave global teria de escolher uma das duas —
// ligada, o navegador descartaria o cookie vindo pelo IP e o login pararia de funcionar
// justo no caminho que existe para socorrer; desligada, a sessão trafegaria sem a proteção
// que o HTTPS já permite dar.
//
// VODM_COOKIE_SECURE continua valendo como imposição: quem só atende por HTTPS pode
// exigir o atributo sempre.
func (s *Server) cookieSeguro(r *http.Request) bool {
	if s.deps.CookieSecure || r.TLS != nil {
		return true
	}
	// Atrás do nginx a conexão TLS termina antes de chegar aqui; quem sabe do esquema
	// original é o cabeçalho. Vale a mesma regra do IP do cliente: só é aceito quando quem
	// entregou a requisição foi o proxy da própria máquina.
	if s.deps.TrustProxy && ehLoopback(r.RemoteAddr) {
		return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	}
	return false
}

// ehLoopback informa se a requisição veio da própria máquina.
func ehLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// limparCookieSessao apaga o cookie de sessão no navegador.
//
// Compartilhado entre o logout e a troca de senha: os dois precisam encerrar a sessão do
// lado do cliente, e divergir nos atributos deixaria um cookie órfão em um dos caminhos.
func limparCookieSessao(w http.ResponseWriter, nome string, seguro bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     nome,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   seguro,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
