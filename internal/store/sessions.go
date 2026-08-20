package store

import (
	"context"
	"time"
)

// Session é uma sessão de painel. O token em claro nunca é persistido.
type Session struct {
	ID         int64
	UserID     int64
	UserAgent  string
	ClientIP   string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// CreateSession grava uma sessão a partir do hash do token.
func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash []byte, userAgent, clientIP string, expiresAt time.Time) (*Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx, `
		INSERT INTO sessions (user_id, token_hash, user_agent, client_ip, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, coalesce(user_agent,''), coalesce(client_ip,''),
		          created_at, last_seen_at, expires_at, revoked_at`,
		userID, tokenHash, userAgent, clientIP, expiresAt,
	).Scan(&sess.ID, &sess.UserID, &sess.UserAgent, &sess.ClientIP,
		&sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt, &sess.RevokedAt)
	if err != nil {
		return nil, wrapErr("criando sessão", err)
	}
	return &sess, nil
}

// LookupSession devolve a sessão VÁLIDA e o usuário dono, a partir do hash do token.
//
// Uma única query resolve validade da sessão e estado do usuário: sessão revogada,
// expirada ou de usuário desabilitado simplesmente não retorna linha.
func (s *Store) LookupSession(ctx context.Context, tokenHash []byte) (*Session, *User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT s.id, s.user_id, coalesce(s.user_agent,''), coalesce(s.client_ip,''),
		       s.created_at, s.last_seen_at, s.expires_at, s.revoked_at,
		       u.id, u.username, u.password_hash, u.role, u.enabled,
		       u.last_login_at, u.created_at, u.updated_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.expires_at > now()
		  AND u.enabled`, tokenHash)

	var sess Session
	var u User
	err := row.Scan(&sess.ID, &sess.UserID, &sess.UserAgent, &sess.ClientIP,
		&sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt, &sess.RevokedAt,
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Enabled,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, nil, wrapErr("buscando sessão", err)
	}
	return &sess, &u, nil
}

// TouchSession atualiza o last_seen_at da sessão.
func (s *Store) TouchSession(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = now() WHERE id = $1`, id)
	return wrapErr("atualizando sessão", err)
}

// RevokeSession revoga uma sessão pelo hash do token (logout).
func (s *Store) RevokeSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash)
	return wrapErr("revogando sessão", err)
}

// RevokeUserSessions revoga todas as sessões de um usuário.
func (s *Store) RevokeUserSessions(ctx context.Context, userID int64) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	if err != nil {
		return 0, wrapErr("revogando sessões do usuário", err)
	}
	return tag.RowsAffected(), nil
}

// PurgeExpiredSessions apaga sessões expiradas ou revogadas há mais de `grace`.
func (s *Store) PurgeExpiredSessions(ctx context.Context, grace time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM sessions
		WHERE expires_at < now() - $1::interval
		   OR (revoked_at IS NOT NULL AND revoked_at < now() - $1::interval)`,
		grace.String())
	if err != nil {
		return 0, wrapErr("limpando sessões", err)
	}
	return tag.RowsAffected(), nil
}
