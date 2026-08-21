package store

import (
	"context"
	"time"
)

// StreamCredential é a credencial de SAÍDA: o que o XC_VM usa para pedir vídeo.
//
// A senha nunca aparece nesta struct — só o HMAC, e ele não é serializado.
type StreamCredential struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Username     string `json:"username"`
	PasswordHMAC []byte `json:"-"`
	// PasswordEnc é a senha cifrada. Nulo nas credenciais anteriores à migração 0006 —
	// para elas o painel não consegue montar o link pronto e oferece trocar a senha.
	PasswordEnc    []byte     `json:"-"`
	KeyVersion     int        `json:"-"`
	Enabled        bool       `json:"enabled"`
	RevokedAt      *time.Time `json:"revoked_at"`
	ExpiresAt      *time.Time `json:"expires_at"`
	MaxConnections *int       `json:"max_connections"`
	AllowedCIDRs   []string   `json:"allowed_cidrs"`
	LastUsedAt     *time.Time `json:"last_used_at"`
	UseCount       int64      `json:"use_count"`
	BytesServed    int64      `json:"bytes_served"`
	// BytesLimit é a cota de banda. Nulo = sem limite.
	BytesLimit *int64 `json:"bytes_limit"`
	// Ciclo diz se a cota renova: "nenhum" (balde único) ou "mensal".
	Ciclo string `json:"ciclo"`
	// BytesCiclo é o consumo do ciclo atual — é ele que a cota compara. BytesServed
	// continua sendo o total histórico, que diz se vale renovar o contrato.
	BytesCiclo  int64     `json:"bytes_ciclo"`
	CicloInicio time.Time `json:"ciclo_inicio"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Ativa informa se a credencial pode ser usada agora.
func (c *StreamCredential) Ativa(agora time.Time) bool {
	if !c.Enabled || c.RevokedAt != nil {
		return false
	}
	if c.ExpiresAt != nil && agora.After(*c.ExpiresAt) {
		return false
	}
	return !c.CotaEsgotada(agora)
}

// CotaEsgotada informa se o cliente já consumiu a banda contratada.
//
// A renovação é avaliada aqui, sem escrever nada: a checagem acontece no caminho de cada
// requisição de vídeo, e uma escrita ali custaria latência em toda reprodução. Quem zera
// o contador de verdade é a contabilidade, que já roda fora do caminho crítico.
func (c *StreamCredential) CotaEsgotada(agora time.Time) bool {
	if c.BytesLimit == nil {
		return false
	}
	if c.CicloExpirou(agora) {
		// Ciclo virou: o consumo do mês passado não conta mais.
		return false
	}
	return c.BytesCiclo >= *c.BytesLimit
}

// CicloExpirou informa se o ciclo mensal já virou desde o último registro.
func (c *StreamCredential) CicloExpirou(agora time.Time) bool {
	if c.Ciclo != "mensal" {
		return false
	}
	inicioDoMes := time.Date(agora.Year(), agora.Month(), 1, 0, 0, 0, 0, agora.Location())
	return c.CicloInicio.Before(inicioDoMes)
}

// BytesRestantes devolve quanto ainda cabe na cota. Negativo vira zero.
func (c *StreamCredential) BytesRestantes(agora time.Time) int64 {
	if c.BytesLimit == nil {
		return -1 // sem limite
	}
	usado := c.BytesCiclo
	if c.CicloExpirou(agora) {
		usado = 0
	}
	if restante := *c.BytesLimit - usado; restante > 0 {
		return restante
	}
	return 0
}

const streamCredentialColumns = `id, name, description, username, password_hmac, password_enc, key_version,
	enabled, revoked_at, expires_at, max_connections, allowed_cidrs,
	last_used_at, use_count, bytes_served, bytes_limit, ciclo, bytes_ciclo, ciclo_inicio,
	created_at, updated_at`

func scanStreamCredential(row rowScanner) (*StreamCredential, error) {
	var c StreamCredential
	if err := row.Scan(&c.ID, &c.Name, &c.Description, &c.Username, &c.PasswordHMAC,
		&c.PasswordEnc, &c.KeyVersion, &c.Enabled, &c.RevokedAt, &c.ExpiresAt, &c.MaxConnections,
		&c.AllowedCIDRs, &c.LastUsedAt, &c.UseCount, &c.BytesServed,
		&c.BytesLimit, &c.Ciclo, &c.BytesCiclo, &c.CicloInicio,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if c.AllowedCIDRs == nil {
		c.AllowedCIDRs = []string{}
	}
	return &c, nil
}

// CreateStreamCredential grava uma credencial de saída.
func (s *Store) CreateStreamCredential(ctx context.Context, nome, descricao, username string, hmac, senhaCifrada []byte, criadoPor int64, expiraEm *time.Time) (*StreamCredential, error) {
	var criador *int64
	if criadoPor > 0 {
		criador = &criadoPor
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO stream_credentials (name, description, username, password_hmac, password_enc, expires_at, created_by)
		VALUES ($1, coalesce($2,''), $3, $4, $5, $6, $7)
		RETURNING `+streamCredentialColumns,
		nome, descricao, username, hmac, senhaCifrada, expiraEm, criador)
	c, err := scanStreamCredential(row)
	return c, wrapErr("criando credencial de streaming", err)
}

// GetStreamCredentialByUsername busca uma credencial pelo nome de usuário.
//
// Devolve a credencial mesmo revogada ou expirada: quem decide é o chamador, que precisa
// distinguir "não existe" de "existe mas foi revogada" para dar a resposta certa.
func (s *Store) GetStreamCredentialByUsername(ctx context.Context, username string) (*StreamCredential, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+streamCredentialColumns+` FROM stream_credentials WHERE username = $1`, username)
	c, err := scanStreamCredential(row)
	return c, wrapErr("buscando credencial de streaming", err)
}

// ListStreamCredentials devolve todas as credenciais de saída.
func (s *Store) ListStreamCredentials(ctx context.Context) ([]StreamCredential, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+streamCredentialColumns+` FROM stream_credentials ORDER BY created_at DESC`)
	if err != nil {
		return nil, wrapErr("listando credenciais de streaming", err)
	}
	defer rows.Close()

	out := []StreamCredential{}
	for rows.Next() {
		c, err := scanStreamCredential(rows)
		if err != nil {
			return nil, wrapErr("listando credenciais de streaming", err)
		}
		out = append(out, *c)
	}
	return out, wrapErr("listando credenciais de streaming", rows.Err())
}

// RotateStreamCredentialPassword troca a senha de uma credencial existente.
//
// O usuário permanece o mesmo — os links já cadastrados continuam válidos, só a senha
// muda. É o caminho para quando a senha foi perdida (ela não é recuperável) ou quando um
// cliente compartilhou o acesso e você quer cortar sem trocar o link inteiro.
func (s *Store) RotateStreamCredentialPassword(ctx context.Context, id int64, hmac, senhaCifrada []byte) (*StreamCredential, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE stream_credentials
		SET password_hmac = $2, password_enc = $3, updated_at = now()
		WHERE id = $1
		RETURNING `+streamCredentialColumns, id, hmac, senhaCifrada)
	c, err := scanStreamCredential(row)
	return c, wrapErr("trocando senha da credencial", err)
}

// UpdateStreamCredential ajusta os limites de uma credencial.
type StreamCredentialPatch struct {
	Name           *string
	Description    *string
	Enabled        *bool
	MaxConnections **int
	AllowedCIDRs   *[]string
	// BytesLimit duplamente apontado: nulo = não mexer, apontando para nulo = remover a
	// cota. Sem essa distinção não haveria como voltar uma credencial para ilimitada.
	BytesLimit **int64
	Ciclo      *string
	// ZerarCiclo recomeça a contagem sem esperar a virada do mês. É o que o administrador
	// faz quando o cliente paga um pacote extra no meio do período.
	ZerarCiclo bool
}

// UpdateStreamCredential aplica um patch parcial.
func (s *Store) UpdateStreamCredential(ctx context.Context, id int64, p StreamCredentialPatch) (*StreamCredential, error) {
	var maxConns *int
	maxSet := p.MaxConnections != nil
	if maxSet {
		maxConns = *p.MaxConnections
	}

	var cota *int64
	cotaSet := p.BytesLimit != nil
	if cotaSet {
		cota = *p.BytesLimit
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE stream_credentials SET
			name            = coalesce($2::text, name),
			description     = coalesce($3::text, description),
			enabled         = coalesce($4::boolean, enabled),
			max_connections = CASE WHEN $5::boolean THEN $6::int ELSE max_connections END,
			allowed_cidrs   = coalesce($7::text[], allowed_cidrs),
			bytes_limit     = CASE WHEN $8::boolean THEN $9::bigint ELSE bytes_limit END,
			ciclo           = coalesce($10::text, ciclo),
			-- Zerar recomeça a contagem AGORA, sem esperar a virada do mês: é o caminho
			-- de "o cliente pagou um pacote extra no meio do período".
			bytes_ciclo     = CASE WHEN $11::boolean THEN 0 ELSE bytes_ciclo END,
			ciclo_inicio    = CASE WHEN $11::boolean THEN now() ELSE ciclo_inicio END,
			updated_at      = now()
		WHERE id = $1
		RETURNING `+streamCredentialColumns,
		id, p.Name, p.Description, p.Enabled, maxSet, maxConns, p.AllowedCIDRs,
		cotaSet, cota, p.Ciclo, p.ZerarCiclo)
	c, err := scanStreamCredential(row)
	return c, wrapErr("atualizando credencial", err)
}

// RevokeStreamCredential revoga uma credencial. O efeito é imediato.
func (s *Store) RevokeStreamCredential(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE stream_credentials
		SET revoked_at = now(), enabled = false, updated_at = now()
		WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return wrapErr("revogando credencial", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("revogando credencial", ErrNotFound)
	}
	return nil
}

// DeleteStreamCredential remove uma credencial.
func (s *Store) DeleteStreamCredential(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM stream_credentials WHERE id = $1`, id)
	if err != nil {
		return wrapErr("removendo credencial", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("removendo credencial", ErrNotFound)
	}
	return nil
}

// TouchStreamCredential registra o uso. Escrita em lote pelo chamador; nunca no caminho
// dos bytes.
// TouchStreamCredential registra o uso. Escrita em lote pelo chamador; nunca no caminho
// dos bytes.
//
// É aqui que o ciclo mensal vira de fato. A verificação da cota, no caminho crítico, só
// COMPARA datas; quem zera o contador é esta escrita, que já acontece fora dele.
func (s *Store) TouchStreamCredential(ctx context.Context, id int64, usos int, bytes int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE stream_credentials SET
			last_used_at = now(),
			use_count    = use_count + $2,
			bytes_served = bytes_served + $3,
			bytes_ciclo = CASE
				WHEN ciclo = 'mensal' AND ciclo_inicio < date_trunc('month', now())
				THEN $3
				ELSE bytes_ciclo + $3
			END,
			ciclo_inicio = CASE
				WHEN ciclo = 'mensal' AND ciclo_inicio < date_trunc('month', now())
				THEN date_trunc('month', now())
				ELSE ciclo_inicio
			END
		WHERE id = $1`, id, usos, bytes)
	return wrapErr("registrando uso da credencial", err)
}
