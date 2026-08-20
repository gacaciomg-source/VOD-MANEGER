package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migration é um arquivo de migração embutido no binário.
type Migration struct {
	Version int64
	Name    string
	SQL     string
}

// advisoryLockID é o identificador do lock de migração. Garante que dois processos
// subindo ao mesmo tempo (algo esperado quando houver múltiplos nós) não apliquem a
// mesma migração em paralelo.
const advisoryLockID int64 = 8_244_190_31

// LoadMigrations lê e ordena as migrações embutidas.
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("db: lendo migrações: %w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int64]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		versionStr, name, ok := strings.Cut(strings.TrimSuffix(e.Name(), ".sql"), "_")
		if !ok {
			return nil, fmt.Errorf("db: migração %q não segue o padrão NNNN_nome.sql", e.Name())
		}
		version, err := strconv.ParseInt(versionStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("db: migração %q tem versão inválida: %w", e.Name(), err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("db: versão %d duplicada entre %q e %q", version, prev, e.Name())
		}
		seen[version] = e.Name()

		content, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("db: lendo %q: %w", e.Name(), err)
		}
		migrations = append(migrations, Migration{Version: version, Name: name, SQL: string(content)})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// Migrate aplica as migrações pendentes e devolve as versões aplicadas nesta execução.
//
// Cada migração roda dentro de uma transação: ou aplica inteira, ou não aplica.
func Migrate(ctx context.Context, pool *Pool) ([]int64, error) {
	migrations, err := LoadMigrations()
	if err != nil {
		return nil, err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: adquirindo conexão para migrar: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		return nil, fmt.Errorf("db: obtendo lock de migração: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, advisoryLockID)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    bigint      PRIMARY KEY,
			name       text        NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, fmt.Errorf("db: criando schema_migrations: %w", err)
	}

	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("db: lendo schema_migrations: %w", err)
	}
	applied := make(map[int64]bool)
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, fmt.Errorf("db: lendo versão aplicada: %w", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: lendo schema_migrations: %w", err)
	}

	var newlyApplied []int64
	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		if err := applyOne(ctx, conn.Conn(), m); err != nil {
			return newlyApplied, err
		}
		newlyApplied = append(newlyApplied, m.Version)
	}
	return newlyApplied, nil
}

func applyOne(ctx context.Context, conn *pgx.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: iniciando transação da migração %d: %w", m.Version, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("db: aplicando migração %d (%s): %w", m.Version, m.Name, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
		m.Version, m.Name,
	); err != nil {
		return fmt.Errorf("db: registrando migração %d: %w", m.Version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit da migração %d: %w", m.Version, err)
	}
	return nil
}
