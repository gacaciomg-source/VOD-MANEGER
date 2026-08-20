// Package store é a camada de acesso a dados: pgx/v5 com SQL escrito à mão.
//
// Sem ORM: as queries do caminho crítico precisam ser previsíveis e auditáveis.
package store

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"vodmanager/internal/db"
)

var (
	// ErrNotFound indica que o registro não existe.
	ErrNotFound = errors.New("registro não encontrado")
	// ErrConflict indica violação de unicidade (nome já usado, por exemplo).
	ErrConflict = errors.New("registro em conflito com um existente")
	// ErrInvalid indica violação de uma regra declarada no schema (CHECK constraint).
	ErrInvalid = errors.New("dados inválidos para o schema")
)

// Store agrupa o acesso a dados de todos os agregados da Fase 1.
type Store struct {
	pool *db.Pool
}

// New cria um Store sobre um pool já aberto.
func New(pool *db.Pool) *Store { return &Store{pool: pool} }

// Pool expõe o pool para usos pontuais (health check, testes de integração).
func (s *Store) Pool() *db.Pool { return s.pool }

// wrapErr traduz erros do Postgres para os erros de domínio do pacote.
func wrapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%s: %w (%s)", op, ErrConflict, pgErr.ConstraintName)
		case "23514", "23502", "22P02": // check_violation, not_null, invalid_text_representation
			return fmt.Errorf("%s: %w (%s)", op, ErrInvalid, pgErr.ConstraintName)
		case "23503": // foreign_key_violation
			return fmt.Errorf("%s: %w (%s)", op, ErrNotFound, pgErr.ConstraintName)
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}
