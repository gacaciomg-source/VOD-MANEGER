package auth

import (
	"context"
	"net"
	"net/http"
	"strings"

	"vodmanager/internal/store"
)

type contextKey struct{ name string }

var principalKey = contextKey{"principal"}

// WithPrincipal anexa o principal ao contexto.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFrom recupera o principal do contexto.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey).(*Principal)
	return p, ok
}

// Deny é chamado quando a requisição não passa na autenticação/autorização.
type Deny func(w http.ResponseWriter, r *http.Request, status int, code, message string)

// Middleware autentica por cookie de sessão ou por `Authorization: Bearer <token>`.
func (s *Service) Middleware(cookieName string, deny Deny) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var (
				principal *Principal
				err       error
			)

			if bearer := bearerToken(r); bearer != "" {
				principal, err = s.AuthenticateAPIToken(r.Context(), bearer)
			} else if c, cerr := r.Cookie(cookieName); cerr == nil {
				principal, err = s.AuthenticateSession(r.Context(), c.Value)
			} else {
				deny(w, r, http.StatusUnauthorized, "unauthenticated", "autenticação necessária")
				return
			}

			if err != nil {
				deny(w, r, http.StatusUnauthorized, "unauthenticated", "credenciais inválidas ou expiradas")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

// RequireRole exige um dos papéis informados.
func RequireRole(deny Deny, allowed ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				deny(w, r, http.StatusUnauthorized, "unauthenticated", "autenticação necessária")
				return
			}
			for _, role := range allowed {
				if p.User.Role == role {
					next.ServeHTTP(w, r)
					return
				}
			}
			deny(w, r, http.StatusForbidden, "forbidden", "seu papel não permite esta operação")
		})
	}
}

// CanWrite informa se o papel pode alterar dados.
func CanWrite(role string) bool {
	return role == store.RoleAdmin || role == store.RoleOperator
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	scheme, value, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}

// ClientIP extrai o IP do cliente. Só confia em X-Forwarded-For quando `trustProxy`
// for verdadeiro — caso contrário o cabeçalho é forjável e o rate limit por IP vira
// inútil.
func ClientIP(r *http.Request, trustProxy bool) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	// X-Forwarded-For só vale quando quem entregou a requisição foi o proxy da própria
	// máquina.
	//
	// O cabeçalho é escrito pelo cliente e reescrito pelo proxy — ele não prova nada
	// sozinho. Com a porta da aplicação aberta ao mundo (e ela FICA aberta de propósito,
	// para os links já entregues continuarem funcionando), confiar nele sem olhar de onde
	// veio deixaria qualquer pessoa escolher o IP com que aparece: o suficiente para
	// escapar do limite de telas simultâneas e para envenenar o registro de falhas.
	//
	// Exigir que o vizinho imediato seja o loopback fecha isso sem custo nenhum: o nginx
	// fala de 127.0.0.1, e quem chega direto na porta não fala.
	if trustProxy && ehLoopback(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}
	}
	return host
}

// ehLoopback informa se o endereço é a própria máquina.
func ehLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
