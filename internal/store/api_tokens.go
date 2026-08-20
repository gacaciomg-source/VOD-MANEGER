package store

import (
	"context"
	"time"
)

// APIToken é um token de automação. O valor em claro só existe na criação.
type APIToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Enabled    bool       `json:"enabled"`
	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

const apiTokenColumns = `id, user_id, name, prefix, enabled, expires_at, revoked_at, last_used_at, created_at`

// CreateAPIToken grava um token a partir do seu hash.
func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name, prefix string, tokenHash []byte, expiresAt *time.Time) (*APIToken, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO api_tokens (user_id, name, prefix, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+apiTokenColumns,
		userID, name, prefix, tokenHash, expiresAt)
	t, err := scanAPIToken(row)
	return t, wrapErr("criando token de API", err)
}

// LookupAPIToken devolve o token válido e o usuário dono, pelo hash.
func (s *Store) LookupAPIToken(ctx context.Context, tokenHash []byte) (*APIToken, *User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT t.id, t.user_id, t.name, t.prefix, t.enabled, t.expires_at,
		       t.revoked_at, t.last_used_at, t.created_at,
		       u.id, u.username, u.password_hash, u.role, u.enabled,
		       u.last_login_at, u.created_at, u.updated_at
		FROM api_tokens t
		JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1
		  AND t.enabled
		  AND t.revoked_at IS NULL
		  AND (t.expires_at IS NULL OR t.expires_at > now())
		  AND u.enabled`, tokenHash)

	var t APIToken
	var u User
	err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Enabled, &t.ExpiresAt,
		&t.RevokedAt, &t.LastUsedAt, &t.CreatedAt,
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Enabled,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, nil, wrapErr("buscando token de API", err)
	}
	return &t, &u, nil
}

// ListAPITokens lista os tokens de um usuário.
func (s *Store) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+apiTokenColumns+` FROM api_tokens WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, wrapErr("listando tokens de API", err)
	}
	defer rows.Close()

	out := []APIToken{}
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, wrapErr("listando tokens de API", err)
		}
		out = append(out, *t)
	}
	return out, wrapErr("listando tokens de API", rows.Err())
}

// RevokeAPIToken revoga um token do usuário informado.
func (s *Store) RevokeAPIToken(ctx context.Context, userID, tokenID int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE api_tokens SET revoked_at = now(), enabled = false
		 WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, tokenID, userID)
	if err != nil {
		return wrapErr("revogando token de API", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("revogando token de API", ErrNotFound)
	}
	return nil
}

// TouchAPIToken registra o último uso.
func (s *Store) TouchAPIToken(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = now() WHERE id = $1`, id)
	return wrapErr("atualizando token de API", err)
}

func scanAPIToken(row rowScanner) (*APIToken, error) {
	var t APIToken
	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Enabled,
		&t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt, &t.CreatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}
