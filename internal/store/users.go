package store

import (
	"context"
	"time"
)

// Papéis de usuário administrativo.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// ValidUserRole informa se o papel é aceito pelo schema.
func ValidUserRole(role string) bool {
	switch role {
	case RoleAdmin, RoleOperator, RoleViewer:
		return true
	}
	return false
}

// User é um usuário administrativo do painel.
type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"` // nunca serializado
	Role         string     `json:"role"`
	Enabled      bool       `json:"enabled"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const userColumns = `id, username, password_hash, role, enabled, last_login_at, created_at, updated_at`

// CreateUser insere um usuário. O hash já deve vir pronto do pacote auth.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, $2, $3)
		RETURNING `+userColumns,
		username, passwordHash, role)
	u, err := scanUser(row)
	return u, wrapErr("criando usuário", err)
}

// GetUserByUsername busca por nome de usuário.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1`, username)
	u, err := scanUser(row)
	return u, wrapErr("buscando usuário", err)
}

// GetUserByID busca por id.
func (s *Store) GetUserByID(ctx context.Context, id int64) (*User, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	u, err := scanUser(row)
	return u, wrapErr("buscando usuário", err)
}

// CountUsers conta os usuários existentes (usado no bootstrap do primeiro admin).
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, wrapErr("contando usuários", err)
}

// TouchUserLogin registra o último login bem-sucedido.
func (s *Store) TouchUserLogin(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET last_login_at = now(), updated_at = now() WHERE id = $1`, id)
	return wrapErr("registrando login", err)
}

// SetUserPassword troca o hash de senha do usuário.
func (s *Store) SetUserPassword(ctx context.Context, id int64, passwordHash string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, id, passwordHash)
	if err != nil {
		return wrapErr("trocando senha", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("trocando senha", ErrNotFound)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Enabled,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// ListUsers devolve todos os usuários do painel.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, wrapErr("listando usuários", err)
	}
	defer rows.Close()

	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, wrapErr("listando usuários", err)
		}
		out = append(out, *u)
	}
	return out, wrapErr("listando usuários", rows.Err())
}

// UserPatch altera papel e estado de um usuário. Nulo significa "não mexer".
type UserPatch struct {
	Role    *string
	Enabled *bool
}

// UpdateUser aplica um patch parcial.
func (s *Store) UpdateUser(ctx context.Context, id int64, p UserPatch) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE users SET
			role    = coalesce($2, role),
			enabled = coalesce($3, enabled),
			updated_at = now()
		WHERE id = $1
		RETURNING `+userColumns, id, p.Role, p.Enabled)
	u, err := scanUser(row)
	return u, wrapErr("atualizando usuário", err)
}

// DeleteUser remove um usuário.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return wrapErr("removendo usuário", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("removendo usuário", ErrNotFound)
	}
	return nil
}

// ContarAdministradoresAtivos conta quantos admins habilitados existem.
//
// Existe para impedir a última porta de se fechar por dentro: remover, desabilitar ou
// rebaixar o único administrador deixaria o sistema sem ninguém capaz de gerenciá-lo, e
// não há tela de recuperação.
func (s *Store) ContarAdministradoresAtivos(ctx context.Context, exceto int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM users
		WHERE role = 'admin' AND enabled AND id <> $1`, exceto).Scan(&n)
	return n, wrapErr("contando administradores", err)
}
