package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"vodmanager/internal/store"
)

var (
	// ErrInvalidCredentials cobre usuário inexistente, senha errada e usuário desabilitado.
	// É deliberadamente um único erro: a API não deve revelar qual dos três ocorreu.
	ErrInvalidCredentials = errors.New("usuário ou senha inválidos")
	// ErrRateLimited indica bloqueio temporário por excesso de tentativas.
	ErrRateLimited = errors.New("tentativas de login em excesso")
	// ErrUnauthenticated indica ausência de sessão ou token válidos.
	ErrUnauthenticated = errors.New("não autenticado")
)

// Options configura o serviço de autenticação.
type Options struct {
	SessionTTL       time.Duration
	LoginMaxAttempts int
	LoginWindow      time.Duration
}

// Service concentra login, sessões e tokens de API.
type Service struct {
	store   *store.Store
	log     *slog.Logger
	opts    Options
	limiter *LoginLimiter
}

// NewService cria o serviço.
func NewService(st *store.Store, log *slog.Logger, opts Options) *Service {
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = 12 * time.Hour
	}
	if opts.LoginMaxAttempts <= 0 {
		opts.LoginMaxAttempts = 5
	}
	if opts.LoginWindow <= 0 {
		opts.LoginWindow = 15 * time.Minute
	}
	return &Service{
		store:   st,
		log:     log,
		opts:    opts,
		limiter: NewLoginLimiter(opts.LoginMaxAttempts, opts.LoginWindow),
	}
}

// LoginResult é o retorno de um login bem-sucedido.
type LoginResult struct {
	User      *store.User
	SessionID int64
	Token     string
	ExpiresAt time.Time
}

// Login autentica por usuário e senha e abre uma sessão.
//
// O custo do Argon2id é pago mesmo quando o usuário não existe, para que o tempo de
// resposta não revele quais nomes de usuário são válidos.
func (s *Service) Login(ctx context.Context, username, password, userAgent, clientIP string) (*LoginResult, time.Duration, error) {
	username = strings.TrimSpace(username)
	key := username + "|" + clientIP

	if allowed, retryAfter := s.limiter.Allow(key); !allowed {
		return nil, retryAfter, ErrRateLimited
	}

	user, err := s.store.GetUserByUsername(ctx, username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, 0, fmt.Errorf("login: %w", err)
	}

	hash := dummyHash
	if user != nil {
		hash = user.PasswordHash
	}
	ok, verifyErr := VerifyPassword(password, hash)
	if verifyErr != nil && user != nil {
		// Hash corrompido no banco: é erro de operação, não credencial inválida.
		s.log.Error("hash de senha inválido no banco", "user_id", user.ID, "erro", verifyErr)
		return nil, 0, fmt.Errorf("login: %w", verifyErr)
	}
	if !ok || user == nil || !user.Enabled {
		return nil, 0, ErrInvalidCredentials
	}

	token, err := NewToken()
	if err != nil {
		return nil, 0, fmt.Errorf("login: %w", err)
	}
	expiresAt := time.Now().Add(s.opts.SessionTTL)
	sess, err := s.store.CreateSession(ctx, user.ID, token.Hash, userAgent, clientIP, expiresAt)
	if err != nil {
		return nil, 0, fmt.Errorf("login: %w", err)
	}
	if err := s.store.TouchUserLogin(ctx, user.ID); err != nil {
		s.log.Warn("não foi possível registrar o último login", "user_id", user.ID, "erro", err)
	}

	s.limiter.Reset(key)
	return &LoginResult{User: user, SessionID: sess.ID, Token: token.Plain, ExpiresAt: sess.ExpiresAt}, 0, nil
}

// dummyHash é um hash válido de uma senha aleatória, usado para gastar o mesmo tempo de
// verificação quando o usuário não existe.
var dummyHash = mustDummyHash()

func mustDummyHash() string {
	p, err := GeneratePassword()
	if err != nil {
		panic("auth: não foi possível gerar o hash de comparação: " + err.Error())
	}
	h, err := HashPassword(p)
	if err != nil {
		panic("auth: não foi possível gerar o hash de comparação: " + err.Error())
	}
	return h
}

// Logout revoga a sessão do token informado.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.store.RevokeSession(ctx, HashToken(token))
}

// Principal é quem está autenticado numa requisição.
type Principal struct {
	User      *store.User
	SessionID int64
	TokenID   int64
	Via       string // "session" | "api_token"
}

// AuthenticateSession valida um token de sessão.
func (s *Service) AuthenticateSession(ctx context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	sess, user, err := s.store.LookupSession(ctx, HashToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrUnauthenticated
		}
		return nil, err
	}
	if err := s.store.TouchSession(ctx, sess.ID); err != nil {
		s.log.Warn("não foi possível atualizar a sessão", "session_id", sess.ID, "erro", err)
	}
	return &Principal{User: user, SessionID: sess.ID, Via: "session"}, nil
}

// AuthenticateAPIToken valida um token de API.
func (s *Service) AuthenticateAPIToken(ctx context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	tok, user, err := s.store.LookupAPIToken(ctx, HashToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrUnauthenticated
		}
		return nil, err
	}
	if err := s.store.TouchAPIToken(ctx, tok.ID); err != nil {
		s.log.Warn("não foi possível atualizar o token de API", "token_id", tok.ID, "erro", err)
	}
	return &Principal{User: user, TokenID: tok.ID, Via: "api_token"}, nil
}

// SweepLimiter descarta contadores de login já expirados.
func (s *Service) SweepLimiter() int { return s.limiter.Sweep() }

// SessionTTL devolve a validade configurada das sessões.
func (s *Service) SessionTTL() time.Duration { return s.opts.SessionTTL }
