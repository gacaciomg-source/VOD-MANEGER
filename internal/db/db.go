// Package db abre o pool de conexões e aplica as migrações.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool é o pool de conexões do Postgres usado por toda a aplicação.
type Pool = pgxpool.Pool

// Options controla a abertura do pool.
type Options struct {
	DatabaseURL    string
	MaxConns       int32
	ConnectTimeout time.Duration
	// ApplicationName aparece em pg_stat_activity — útil para diagnosticar
	// qual nó está segurando conexões quando houver mais de um.
	ApplicationName string
}

// Open cria o pool e valida a conexão com um ping.
func Open(ctx context.Context, opts Options) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(opts.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: DSN inválido: %w", err)
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	if opts.ConnectTimeout > 0 {
		cfg.ConnConfig.ConnectTimeout = opts.ConnectTimeout
	}
	if opts.ApplicationName != "" {
		if cfg.ConnConfig.RuntimeParams == nil {
			cfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		cfg.ConnConfig.RuntimeParams["application_name"] = opts.ApplicationName
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: criando pool: %w", err)
	}

	pingCtx := ctx
	if opts.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		pingCtx, cancel = context.WithTimeout(ctx, opts.ConnectTimeout)
		defer cancel()
	}
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: sem conexão com o Postgres: %w", err)
	}
	return pool, nil
}
