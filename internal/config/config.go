// Package config carrega e valida a configuração no boot.
//
// Princípio: falha rápido. Configuração inválida derruba o processo na partida, com
// TODOS os problemas listados de uma vez — nunca vira erro obscuro em runtime.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"vodmanager/internal/cryptobox"
	"vodmanager/internal/roles"
)

// Prefix é o prefixo de todas as variáveis de ambiente do sistema.
const Prefix = "VODM_"

// Config é a configuração validada do processo.
type Config struct {
	// Identidade
	NodeID string
	Role   roles.Role

	// HTTP
	HTTPAddr        string
	ShutdownTimeout time.Duration

	// Banco
	DatabaseURL      string
	DBMaxConns       int32
	DBConnectTimeout time.Duration

	// Segredos
	EncryptionKey []byte

	// Sessão
	SessionTTL       time.Duration
	CookieSecure     bool
	CookieName       string
	LoginMaxAttempts int
	LoginWindow      time.Duration
	// TrustProxy autoriza o uso de X-Forwarded-For para identificar o cliente.
	// Só habilite quando houver um proxy reverso confiável à frente: o cabeçalho é
	// forjável e, sem proxy, tornaria o rate limit por IP inútil.
	TrustProxy bool
	// PublicBaseURL é o endereço pelo qual o mundo alcança este servidor. Necessário
	// quando há proxy reverso na frente, para montar os links que vão para o XC_VM.
	PublicBaseURL string

	// Armazenamento de mídia
	//
	// Fica no ambiente, e não nas configurações do painel, porque é uma propriedade da
	// MÁQUINA: qual disco, qual pasta, quanto reservar. Numa migração para outro servidor
	// esses valores não devem viajar junto com os dados — o disco de lá é outro.
	//
	// O que é decisão de operação (ligar o cache, o destino padrão, o limite) fica nas
	// configurações, editável pelo painel e preservado no backup.
	ArmazenamentoLocal string
	// ArmazenamentoReservaGB é o espaço que o cache nunca ocupa.
	//
	// Um disco 100% cheio não deixa o Postgres escrever, e o sintoma disso não é "o cache
	// encheu" — é o sistema inteiro parando. A reserva é o que separa "o cache atingiu o
	// limite", que é rotina, de "a máquina travou", que é madrugada.
	ArmazenamentoReservaGB int

	// VideoMinimoMB e o tamanho abaixo do qual uma resposta da fonte e tratada como aviso
	// de manutencao em vez de conteudo, e a proxima origem e tentada.
	//
	// 0 usa o padrao (20 MB). -1 desliga a deteccao.
	VideoMinimoMB int

	// TMDBAPIKey habilita a classificacao automatica por genero.
	//
	// Vazia desliga o recurso, e isso e um estado legitimo: o sistema inteiro funciona sem
	// ele. Fica em variavel de ambiente, e nao no banco, porque e credencial de terceiro —
	// o mesmo tratamento que a chave mestra recebe.
	TMDBAPIKey string
	// TMDBIdioma escolhe em que lingua os generos vem. As pastas herdam esse nome.
	TMDBIdioma string

	// Bootstrap do primeiro administrador
	BootstrapAdminUsername string
	BootstrapAdminPassword string

	// Observabilidade
	LogLevel       slog.Level
	LogFormat      string // "json" | "text"
	MetricsEnabled bool
}

// Getenv é a fonte das variáveis. Injetável para teste.
type Getenv func(string) string

// Load lê a configuração do ambiente do processo.
func Load() (*Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom lê a configuração de uma fonte arbitrária e acumula todos os erros.
func LoadFrom(get Getenv) (*Config, error) {
	l := &loader{get: get}
	cfg := &Config{
		NodeID:          l.str("NODE_ID", "node-1"),
		HTTPAddr:        l.str("HTTP_ADDR", ":8080"),
		ShutdownTimeout: l.duration("SHUTDOWN_TIMEOUT", 20*time.Second),

		DatabaseURL:      l.required("DATABASE_URL"),
		DBMaxConns:       int32(l.intRange("DB_MAX_CONNS", 10, 1, 1000)),
		DBConnectTimeout: l.duration("DB_CONNECT_TIMEOUT", 10*time.Second),

		SessionTTL:       l.duration("SESSION_TTL", 12*time.Hour),
		CookieSecure:     l.boolean("COOKIE_SECURE", false),
		CookieName:       l.str("COOKIE_NAME", "vodm_session"),
		LoginMaxAttempts: l.intRange("LOGIN_MAX_ATTEMPTS", 5, 1, 1000),
		LoginWindow:      l.duration("LOGIN_WINDOW", 15*time.Minute),
		TrustProxy:       l.boolean("TRUST_PROXY", false),
		PublicBaseURL:    l.str("PUBLIC_BASE_URL", ""),

		ArmazenamentoLocal:     l.str("ARMAZENAMENTO_LOCAL", "/opt/vodmanager/acervo"),
		TMDBAPIKey:             l.str("TMDB_API_KEY", ""),
		TMDBIdioma:             l.str("TMDB_IDIOMA", "pt-BR"),
		ArmazenamentoReservaGB: l.intRange("ARMAZENAMENTO_RESERVA_GB", 5, 0, 100000),
		VideoMinimoMB:          l.intRange("VIDEO_MINIMO_MB", 0, -1, 100000),

		BootstrapAdminUsername: l.str("BOOTSTRAP_ADMIN_USERNAME", "admin"),
		BootstrapAdminPassword: l.str("BOOTSTRAP_ADMIN_PASSWORD", ""),

		LogFormat:      l.enum("LOG_FORMAT", "json", "json", "text"),
		MetricsEnabled: l.boolean("METRICS_ENABLED", true),
	}
	cfg.Role = l.role("ROLE", roles.RoleAll)
	cfg.LogLevel = l.logLevel("LOG_LEVEL", slog.LevelInfo)
	cfg.EncryptionKey = l.encryptionKey("ENCRYPTION_KEY")

	if cfg.NodeID == "" {
		l.errf("NODE_ID não pode ser vazio")
	}
	if cfg.SessionTTL < time.Minute {
		l.errf("SESSION_TTL precisa ser de pelo menos 1m (recebido: %s)", cfg.SessionTTL)
	}
	if p := cfg.BootstrapAdminPassword; p != "" && len([]rune(p)) < 12 {
		l.errf("BOOTSTRAP_ADMIN_PASSWORD precisa ter ao menos 12 caracteres")
	}

	if err := l.err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Redacted devolve uma visão da configuração segura para log: sem chave nem senha.
func (c *Config) Redacted() map[string]any {
	return map[string]any{
		"node_id":         c.NodeID,
		"role":            string(c.Role),
		"http_addr":       c.HTTPAddr,
		"database":        redactDSN(c.DatabaseURL),
		"db_max_conns":    c.DBMaxConns,
		"session_ttl":     c.SessionTTL.String(),
		"cookie_secure":   c.CookieSecure,
		"log_level":       c.LogLevel.String(),
		"log_format":      c.LogFormat,
		"metrics_enabled": c.MetricsEnabled,
		// A chave em si NUNCA sai daqui — so se ela existe, que e o que explica o recurso
		// estar ligado ou nao.
		"tmdb": c.TMDBAPIKey != "",
	}
}

// redactDSN remove a senha de uma URL de conexão antes de logar.
func redactDSN(dsn string) string {
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return "(dsn)"
	}
	creds, hostpart, ok := strings.Cut(rest, "@")
	if !ok {
		// Sem "@": não há credencial embutida, nada a mascarar.
		return scheme + "://" + rest
	}
	user, _, hasPass := strings.Cut(creds, ":")
	if !hasPass {
		return scheme + "://" + creds + "@" + hostpart
	}
	return scheme + "://" + user + ":***@" + hostpart
}

// ---------------------------------------------------------------------------

type loader struct {
	get  Getenv
	errs []string
}

func (l *loader) raw(key string) string { return strings.TrimSpace(l.get(Prefix + key)) }

func (l *loader) errf(format string, args ...any) {
	l.errs = append(l.errs, fmt.Sprintf(format, args...))
}

func (l *loader) err() error {
	if len(l.errs) == 0 {
		return nil
	}
	return fmt.Errorf("configuração inválida:\n  - %s", strings.Join(l.errs, "\n  - "))
}

func (l *loader) str(key, def string) string {
	if v := l.raw(key); v != "" {
		return v
	}
	return def
}

func (l *loader) required(key string) string {
	v := l.raw(key)
	if v == "" {
		l.errf("%s%s é obrigatório", Prefix, key)
	}
	return v
}

func (l *loader) intRange(key string, def, min, max int) int {
	v := l.raw(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errf("%s%s precisa ser um número inteiro (recebido: %q)", Prefix, key, v)
		return def
	}
	if n < min || n > max {
		l.errf("%s%s precisa estar entre %d e %d (recebido: %d)", Prefix, key, min, max, n)
		return def
	}
	return n
}

func (l *loader) boolean(key string, def bool) bool {
	v := l.raw(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.errf("%s%s precisa ser booleano: true/false/1/0 (recebido: %q)", Prefix, key, v)
		return def
	}
	return b
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	v := l.raw(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.errf("%s%s precisa ser uma duração como 30s, 15m, 12h (recebido: %q)", Prefix, key, v)
		return def
	}
	if d <= 0 {
		l.errf("%s%s precisa ser positivo (recebido: %q)", Prefix, key, v)
		return def
	}
	return d
}

func (l *loader) enum(key, def string, allowed ...string) string {
	v := strings.ToLower(l.raw(key))
	if v == "" {
		return def
	}
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	l.errf("%s%s precisa ser um de [%s] (recebido: %q)", Prefix, key, strings.Join(allowed, ", "), v)
	return def
}

func (l *loader) role(key string, def roles.Role) roles.Role {
	v := l.raw(key)
	if v == "" {
		return def
	}
	r, err := roles.ParseRole(v)
	if err != nil {
		l.errf("%s%s: %v", Prefix, key, err)
		return def
	}
	return r
}

func (l *loader) logLevel(key string, def slog.Level) slog.Level {
	v := l.raw(key)
	if v == "" {
		return def
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(v)); err != nil {
		l.errf("%s%s precisa ser debug, info, warn ou error (recebido: %q)", Prefix, key, v)
		return def
	}
	return lvl
}

func (l *loader) encryptionKey(key string) []byte {
	v := l.raw(key)
	if v == "" {
		l.errf("%s%s é obrigatório: gere uma com `vodmanager genkey` e guarde fora do repositório", Prefix, key)
		return nil
	}
	parsed, err := cryptobox.ParseKey(v)
	if err != nil {
		if errors.Is(err, cryptobox.ErrKeySize) {
			l.errf("%s%s: %v", Prefix, key, err)
		} else {
			l.errf("%s%s: %v", Prefix, key, err)
		}
		return nil
	}
	return parsed
}
