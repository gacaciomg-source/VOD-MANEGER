package config

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"vodmanager/internal/cryptobox"
	"vodmanager/internal/roles"
)

func envFrom(m map[string]string) Getenv {
	return func(k string) string { return m[k] }
}

func baseEnv(t *testing.T) map[string]string {
	t.Helper()
	key, err := cryptobox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return map[string]string{
		"VODM_DATABASE_URL":   "postgres://vodm:secreta@localhost:5432/vodm",
		"VODM_ENCRYPTION_KEY": key,
	}
}

func TestLoadAplicaPadroes(t *testing.T) {
	cfg, err := LoadFrom(envFrom(baseEnv(t)))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Role != roles.RoleAll {
		t.Errorf("Role = %q, esperava all", cfg.Role)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.SessionTTL != 12*time.Hour {
		t.Errorf("SessionTTL = %v", cfg.SessionTTL)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
	if len(cfg.EncryptionKey) != cryptobox.KeySize {
		t.Errorf("EncryptionKey tem %d bytes", len(cfg.EncryptionKey))
	}
}

func TestLoadLeValoresExplicitos(t *testing.T) {
	env := baseEnv(t)
	env["VODM_ROLE"] = "node"
	env["VODM_NODE_ID"] = "edge-sp-01"
	env["VODM_HTTP_ADDR"] = "127.0.0.1:9000"
	env["VODM_SESSION_TTL"] = "45m"
	env["VODM_DB_MAX_CONNS"] = "25"
	env["VODM_COOKIE_SECURE"] = "true"
	env["VODM_LOG_LEVEL"] = "debug"
	env["VODM_LOG_FORMAT"] = "text"
	env["VODM_METRICS_ENABLED"] = "false"

	cfg, err := LoadFrom(envFrom(env))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Role != roles.RoleNode {
		t.Errorf("Role = %q", cfg.Role)
	}
	if cfg.NodeID != "edge-sp-01" {
		t.Errorf("NodeID = %q", cfg.NodeID)
	}
	if cfg.HTTPAddr != "127.0.0.1:9000" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.SessionTTL != 45*time.Minute {
		t.Errorf("SessionTTL = %v", cfg.SessionTTL)
	}
	if cfg.DBMaxConns != 25 {
		t.Errorf("DBMaxConns = %d", cfg.DBMaxConns)
	}
	if !cfg.CookieSecure {
		t.Error("CookieSecure deveria ser true")
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q", cfg.LogFormat)
	}
	if cfg.MetricsEnabled {
		t.Error("MetricsEnabled deveria ser false")
	}
}

func TestLoadExigeObrigatorios(t *testing.T) {
	_, err := LoadFrom(envFrom(map[string]string{}))
	if err == nil {
		t.Fatal("esperava erro sem DATABASE_URL nem ENCRYPTION_KEY")
	}
	msg := err.Error()
	for _, want := range []string{"VODM_DATABASE_URL", "VODM_ENCRYPTION_KEY"} {
		if !strings.Contains(msg, want) {
			t.Errorf("erro não menciona %s: %s", want, msg)
		}
	}
}

// A validação precisa listar TODOS os problemas de uma vez: corrigir um por deploy é
// inaceitável em operação.
func TestLoadAcumulaTodosOsErros(t *testing.T) {
	env := baseEnv(t)
	env["VODM_ROLE"] = "edge"
	env["VODM_SESSION_TTL"] = "não-é-duração"
	env["VODM_DB_MAX_CONNS"] = "0"
	env["VODM_COOKIE_SECURE"] = "talvez"
	env["VODM_LOG_LEVEL"] = "verboso"
	env["VODM_LOG_FORMAT"] = "xml"

	_, err := LoadFrom(envFrom(env))
	if err == nil {
		t.Fatal("esperava erro")
	}
	msg := err.Error()
	for _, want := range []string{"VODM_ROLE", "VODM_SESSION_TTL", "VODM_DB_MAX_CONNS", "VODM_COOKIE_SECURE", "VODM_LOG_LEVEL", "VODM_LOG_FORMAT"} {
		if !strings.Contains(msg, want) {
			t.Errorf("erro não menciona %s:\n%s", want, msg)
		}
	}
}

func TestLoadRejeitaChaveDeCifraInvalida(t *testing.T) {
	env := baseEnv(t)
	env["VODM_ENCRYPTION_KEY"] = "chave-curta"
	if _, err := LoadFrom(envFrom(env)); err == nil {
		t.Fatal("esperava erro com chave de cifra inválida")
	}
}

func TestLoadRejeitaSenhaDeBootstrapFraca(t *testing.T) {
	env := baseEnv(t)
	env["VODM_BOOTSTRAP_ADMIN_PASSWORD"] = "123"
	_, err := LoadFrom(envFrom(env))
	if err == nil || !strings.Contains(err.Error(), "BOOTSTRAP_ADMIN_PASSWORD") {
		t.Fatalf("esperava erro sobre senha fraca, obtive %v", err)
	}
}

func TestRedactedNaoVazaSegredos(t *testing.T) {
	env := baseEnv(t)
	env["VODM_BOOTSTRAP_ADMIN_PASSWORD"] = "uma-senha-bem-longa"
	cfg, err := LoadFrom(envFrom(env))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	dump := strings.ToLower(strings.TrimSpace(joinValues(cfg.Redacted())))
	for _, secret := range []string{"secreta", "uma-senha-bem-longa", env["VODM_ENCRYPTION_KEY"]} {
		if strings.Contains(dump, strings.ToLower(secret)) {
			t.Errorf("Redacted() vazou o segredo %q: %s", secret, dump)
		}
	}
	if !strings.Contains(dump, "***") {
		t.Errorf("a senha do DSN deveria virar ***: %s", dump)
	}
}

func TestRedactDSN(t *testing.T) {
	tests := map[string]string{
		"postgres://user:pass@host:5432/db": "postgres://user:***@host:5432/db",
		"postgres://user@host:5432/db":      "postgres://user@host:5432/db",
		"postgres://host:5432/db":           "postgres://host:5432/db",
		"sem-esquema":                       "(dsn)",
	}
	for in, want := range tests {
		if got := redactDSN(in); got != want {
			t.Errorf("redactDSN(%q) = %q, esperava %q", in, got, want)
		}
	}
}

func joinValues(m map[string]any) string {
	var sb strings.Builder
	for k, v := range m {
		fmt.Fprintf(&sb, "%s=%v ", k, v)
	}
	return sb.String()
}
