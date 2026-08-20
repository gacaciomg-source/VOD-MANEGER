package api

import (
	"errors"
	"net/http"
	"strconv"
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
		Secure:   s.deps.CookieSecure,
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
		Secure:   s.deps.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	s.logEvent(r, "auth", "info", "logout", actorOf(r), nil)
	w.WriteHeader(http.StatusNoContent)
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
