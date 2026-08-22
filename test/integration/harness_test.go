// Package integration roda os testes que exigem um Postgres de verdade.
//
// Fonte do banco, em ordem de preferência:
//  1. VODM_TEST_DATABASE_URL — um Postgres que você já tem (CI, Docker, instalação local);
//  2. embedded-postgres — baixa e sobe um Postgres real, sem Docker.
//
// Com -short, os testes são pulados.
package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"vodmanager/internal/cryptobox"
	"vodmanager/internal/db"
	"vodmanager/internal/store"
)

var (
	sharedOnce sync.Once
	sharedURL  string
	sharedStop func()
	sharedErr  error
)

// testEnv é o ambiente de um teste: store pronto, schema migrado, banco limpo.
type testEnv struct {
	Store  *store.Store
	Pool   *db.Pool
	Crypto *cryptobox.Box
	Log    *slog.Logger
}

// newTestEnv devolve um ambiente limpo. Cada teste começa com as tabelas vazias.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("pulando teste de integração em modo -short")
	}

	url := databaseURL(t)
	ctx := context.Background()

	pool, err := db.Open(ctx, db.Options{
		DatabaseURL:     url,
		MaxConns:        5,
		ConnectTimeout:  15 * time.Second,
		ApplicationName: "vodmanager-test",
	})
	if err != nil {
		t.Fatalf("abrindo pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrando: %v", err)
	}
	truncateAll(t, pool)

	key, err := cryptobox.GenerateKey()
	if err != nil {
		t.Fatalf("gerando chave: %v", err)
	}
	parsed, err := cryptobox.ParseKey(key)
	if err != nil {
		t.Fatalf("lendo chave: %v", err)
	}
	box, err := cryptobox.New(parsed)
	if err != nil {
		t.Fatalf("criando cryptobox: %v", err)
	}

	return &testEnv{
		Store:  store.New(pool),
		Pool:   pool,
		Crypto: box,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// truncateAll limpa os dados mantendo o schema — muito mais rápido que remigrar.
//
// A lista precisa incluir toda tabela que NÃO é alcançada por CASCADE a partir de
// `sources`. `categories` é o caso clássico: nada aponta dela para sources, então ela
// sobrevivia à limpeza e vazava de um teste para o outro — o que mascarou um bug de
// duplicação de categorias até ele aparecer num sync real.
func truncateAll(t *testing.T, pool *db.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		TRUNCATE sources, users, settings, events,
		         categories, contents, unresolved_items,
		         -- Explícita, e não pelo CASCADE: o acervo próprio não tem chave
		         -- estrangeira para variante nenhuma, então ele sobreviveria à limpeza de
		         -- sources e vazaria de um teste para o outro.
		         arquivos_guardados
		RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("limpando tabelas: %v", err)
	}
}

// TestHarnessLimpaTodasAsTabelas garante que nenhuma tabela nova escape da limpeza.
//
// Sem isso, uma tabela criada numa migração futura acumularia dados entre testes e os
// tornaria dependentes da ordem de execução — o pior tipo de teste instável.
func TestHarnessLimpaTodasAsTabelas(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	rows, err := env.Pool.Query(ctx, `
		SELECT c.relname, (SELECT count(*) FROM pg_class WHERE oid = c.oid)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname <> 'schema_migrations'
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("listando tabelas: %v", err)
	}
	var tabelas []string
	for rows.Next() {
		var nome string
		var ignorado int
		if err := rows.Scan(&nome, &ignorado); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		tabelas = append(tabelas, nome)
	}
	rows.Close()

	for _, tabela := range tabelas {
		var n int64
		if err := env.Pool.QueryRow(ctx, `SELECT count(*) FROM `+tabela).Scan(&n); err != nil {
			t.Errorf("contando %s: %v", tabela, err)
			continue
		}
		if n != 0 {
			t.Errorf("a tabela %q tem %d linha(s) após a limpeza — ela precisa entrar em truncateAll", tabela, n)
		}
	}
}

func databaseURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("VODM_TEST_DATABASE_URL"); url != "" {
		return url
	}
	sharedOnce.Do(startEmbedded)
	if sharedErr != nil {
		t.Skipf("Postgres embutido indisponível (%v). Defina VODM_TEST_DATABASE_URL para usar um Postgres próprio.", sharedErr)
	}
	return sharedURL
}

// startEmbedded sobe um Postgres real, uma vez por execução da suíte.
func startEmbedded() {
	port, err := freePort()
	if err != nil {
		sharedErr = fmt.Errorf("procurando porta livre: %w", err)
		return
	}
	runtimeDir := filepath.Join(os.TempDir(), fmt.Sprintf("vodm-pg-%d", os.Getpid()))

	pg := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Username("vodm").
			Password("vodm").
			Database("vodm_test").
			Port(uint32(port)).
			RuntimePath(runtimeDir).
			Logger(io.Discard).
			StartTimeout(90 * time.Second),
	)
	if err := pg.Start(); err != nil {
		sharedErr = err
		return
	}
	sharedURL = fmt.Sprintf("postgres://vodm:vodm@127.0.0.1:%d/vodm_test?sslmode=disable", port)
	sharedStop = func() { _ = pg.Stop() }
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedStop != nil {
		sharedStop()
	}
	os.Exit(code)
}
