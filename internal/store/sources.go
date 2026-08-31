package store

import (
	"context"
	"time"
)

// Tipos de fonte suportados na Fase 2. Cadastráveis já na Fase 1.
const (
	SourceKindM3U    = "m3u"
	SourceKindXtream = "xtream"
)

// ValidSourceKind informa se o tipo é aceito pelo schema.
func ValidSourceKind(kind string) bool {
	return kind == SourceKindM3U || kind == SourceKindXtream
}

// Source é uma fonte de catálogo cadastrada.
//
// Atenção: esta struct é serializada em JSON pela API. Ela NÃO contém, e não pode passar
// a conter, qualquer campo de credencial — credenciais vivem em source_credentials e
// nunca saem do servidor. Ver docs/01 §6 D7.
type Source struct {
	ID                     int64    `json:"id"`
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	Kind                   string   `json:"kind"`
	BaseURL                string   `json:"base_url"`
	Priority               int      `json:"priority"`
	Enabled                bool     `json:"enabled"`
	Status                 string   `json:"status"`
	SyncIntervalMinutes    int      `json:"sync_interval_minutes"`
	MaxConnections         int      `json:"max_connections"`
	MaxConcurrentDownloads int      `json:"max_concurrent_downloads"`
	MaxBandwidthBPS        *int64   `json:"max_bandwidth_bps"`
	RequestBudget          int      `json:"request_budget"`
	MissingTolerance       int      `json:"missing_tolerance"`
	AllowedCategories      []string `json:"allowed_categories"`
	IgnoredCategories      []string `json:"ignored_categories"`
	// CacheHabilitado autoriza copiar o conteudo desta fonte para o acervo. Exige
	// tambem a chave geral ligada: fontes nao sao iguais, e a decisao de gastar disco
	// com uma delas nao deve valer para todas.
	CacheHabilitado bool       `json:"cache_habilitado"`
	LastSyncAt      *time.Time `json:"last_sync_at"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	HasCredentials  bool       `json:"has_credentials"`
	// AssinaturaExpiraEm é quando a assinatura NESTA fonte vence, conforme ela informa.
	//
	// Nulo significa "não informado", e é legítimo: contas sem prazo existem, e fontes M3U
	// simples não dizem nada a respeito.
	AssinaturaExpiraEm *time.Time `json:"assinatura_expira_em"`
	AssinaturaStatus   string     `json:"assinatura_status"`
	// AssinaturaVistaEm diz quando isto foi lido. Sem ele, um vencimento antigo e um recente
	// pareceriam a mesma informação — e o antigo pode já ter sido renovado.
	AssinaturaVistaEm *time.Time `json:"assinatura_vista_em"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// NewSource são os campos aceitos na criação.
type NewSource struct {
	Name                   string
	Description            string
	Kind                   string
	BaseURL                string
	Priority               *int
	Enabled                *bool
	SyncIntervalMinutes    *int
	MaxConnections         *int
	MaxConcurrentDownloads *int
	MaxBandwidthBPS        *int64
	AllowedCategories      []string
	IgnoredCategories      []string
	CacheHabilitado        *bool
}

// SourcePatch são os campos alteráveis. Nulo = não alterar.
type SourcePatch struct {
	Name                   *string
	Description            *string
	BaseURL                *string
	Priority               *int
	Enabled                *bool
	SyncIntervalMinutes    *int
	MaxConnections         *int
	MaxConcurrentDownloads *int
	MaxBandwidthBPS        **int64 // ponteiro duplo: permite gravar NULL explicitamente
	AllowedCategories      *[]string
	IgnoredCategories      *[]string
	CacheHabilitado        *bool
}

// A subquery de credencial devolve apenas a EXISTÊNCIA da credencial, nunca o segredo.
const sourceColumns = `
	s.id, s.name, s.description, s.kind, s.base_url, s.priority, s.enabled, s.status,
	s.sync_interval_minutes, s.max_connections, s.max_concurrent_downloads, s.max_bandwidth_bps,
	s.request_budget, s.missing_tolerance,
	s.allowed_categories, s.ignored_categories, s.cache_habilitado,
	s.last_sync_at, s.last_success_at,
	EXISTS (SELECT 1 FROM source_credentials c WHERE c.source_id = s.id) AS has_credentials,
	s.assinatura_expira_em, s.assinatura_status, s.assinatura_vista_em,
	s.created_at, s.updated_at`

// CreateSource insere uma fonte, aplicando os padrões do schema quando o campo é nulo.
func (s *Store) CreateSource(ctx context.Context, in NewSource) (*Source, error) {
	row := s.pool.QueryRow(ctx, `
		WITH inserido AS (
			INSERT INTO sources (
				name, description, kind, base_url, priority, enabled,
				sync_interval_minutes, max_connections, max_concurrent_downloads,
				max_bandwidth_bps, allowed_categories, ignored_categories, cache_habilitado
			) VALUES (
				$1, $2, $3, $4,
				coalesce($5::int, (SELECT coalesce(max(priority), 0) + 1 FROM sources)),
				coalesce($6::boolean, true),
				coalesce($7::int, 1440), coalesce($8::int, 4), coalesce($9::int, 2),
				$10::bigint, coalesce($11::text[], '{}'::text[]), coalesce($12::text[], '{}'::text[]),
				coalesce($13::boolean, false)
			)
			RETURNING *
		)
		SELECT `+sourceColumns+` FROM inserido s`,
		in.Name, in.Description, in.Kind, in.BaseURL, in.Priority, in.Enabled,
		in.SyncIntervalMinutes, in.MaxConnections, in.MaxConcurrentDownloads,
		in.MaxBandwidthBPS, in.AllowedCategories, in.IgnoredCategories, in.CacheHabilitado)
	src, err := scanSource(row)
	return src, wrapErr("criando fonte", err)
}

// GetSource devolve uma fonte por id.
func (s *Store) GetSource(ctx context.Context, id int64) (*Source, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+sourceColumns+` FROM sources s WHERE s.id = $1`, id)
	src, err := scanSource(row)
	return src, wrapErr("buscando fonte", err)
}

// ListSources devolve todas as fontes na ordem de prioridade (a ordem do drag-and-drop).
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sourceColumns+` FROM sources s ORDER BY s.priority, s.id`)
	if err != nil {
		return nil, wrapErr("listando fontes", err)
	}
	defer rows.Close()

	out := []Source{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, wrapErr("listando fontes", err)
		}
		out = append(out, *src)
	}
	return out, wrapErr("listando fontes", rows.Err())
}

// UpdateSource aplica um patch parcial. Campos nulos ficam como estão.
//
// `kind` é deliberadamente imutável: trocar o tipo de uma fonte já sincronizada
// invalidaria todas as variantes derivadas dela. Para trocar, crie outra fonte.
func (s *Store) UpdateSource(ctx context.Context, id int64, p SourcePatch) (*Source, error) {
	var bandwidth *int64
	bandwidthSet := p.MaxBandwidthBPS != nil
	if bandwidthSet {
		bandwidth = *p.MaxBandwidthBPS
	}

	row := s.pool.QueryRow(ctx, `
		WITH atualizado AS (
			UPDATE sources SET
				name                     = coalesce($2::text, name),
				description              = coalesce($3::text, description),
				base_url                 = coalesce($4::text, base_url),
				priority                 = coalesce($5::int, priority),
				enabled                  = coalesce($6::boolean, enabled),
				sync_interval_minutes    = coalesce($7::int, sync_interval_minutes),
				max_connections          = coalesce($8::int, max_connections),
				max_concurrent_downloads = coalesce($9::int, max_concurrent_downloads),
				max_bandwidth_bps        = CASE WHEN $10::boolean THEN $11::bigint ELSE max_bandwidth_bps END,
				allowed_categories       = coalesce($12::text[], allowed_categories),
				ignored_categories       = coalesce($13::text[], ignored_categories),
				cache_habilitado         = coalesce($14::boolean, cache_habilitado),
				updated_at               = now()
			WHERE id = $1
			RETURNING *
		)
		SELECT `+sourceColumns+` FROM atualizado s`,
		id, p.Name, p.Description, p.BaseURL, p.Priority, p.Enabled,
		p.SyncIntervalMinutes, p.MaxConnections, p.MaxConcurrentDownloads,
		bandwidthSet, bandwidth, p.AllowedCategories, p.IgnoredCategories, p.CacheHabilitado)
	src, err := scanSource(row)
	return src, wrapErr("atualizando fonte", err)
}

// DeleteSource remove a fonte. A credencial cai junto por ON DELETE CASCADE.
func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sources WHERE id = $1`, id)
	if err != nil {
		return wrapErr("removendo fonte", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("removendo fonte", ErrNotFound)
	}
	return nil
}

// ReorderSources reescreve as prioridades na ordem informada (drag-and-drop do painel).
//
// Roda em transação e exige que a lista contenha exatamente todas as fontes: uma lista
// parcial deixaria prioridades duplicadas e a ordem de failover ambígua.
func (s *Store) ReorderSources(ctx context.Context, orderedIDs []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return wrapErr("reordenando fontes", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var total int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM sources`).Scan(&total); err != nil {
		return wrapErr("reordenando fontes", err)
	}
	if int64(len(orderedIDs)) != total {
		return wrapErr("reordenando fontes",
			ErrInvalid) // a API traduz isso numa mensagem explicativa
	}

	tag, err := tx.Exec(ctx, `
		UPDATE sources s
		SET priority = o.pos, updated_at = now()
		FROM (SELECT id, ordinality::int AS pos FROM unnest($1::bigint[]) WITH ORDINALITY AS t(id, ordinality)) o
		WHERE s.id = o.id`, orderedIDs)
	if err != nil {
		return wrapErr("reordenando fontes", err)
	}
	if tag.RowsAffected() != total {
		return wrapErr("reordenando fontes", ErrNotFound)
	}
	return wrapErr("reordenando fontes", tx.Commit(ctx))
}

func scanSource(row rowScanner) (*Source, error) {
	var s Source
	if err := row.Scan(&s.ID, &s.Name, &s.Description, &s.Kind, &s.BaseURL, &s.Priority,
		&s.Enabled, &s.Status, &s.SyncIntervalMinutes, &s.MaxConnections,
		&s.MaxConcurrentDownloads, &s.MaxBandwidthBPS, &s.RequestBudget, &s.MissingTolerance,
		&s.AllowedCategories,
		&s.IgnoredCategories, &s.CacheHabilitado, &s.LastSyncAt, &s.LastSuccessAt, &s.HasCredentials,
		&s.AssinaturaExpiraEm, &s.AssinaturaStatus, &s.AssinaturaVistaEm,
		&s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	if s.AllowedCategories == nil {
		s.AllowedCategories = []string{}
	}
	if s.IgnoredCategories == nil {
		s.IgnoredCategories = []string{}
	}
	return &s, nil
}

// AnotarAssinaturaDaFonte guarda o que a fonte informou sobre a própria assinatura.
//
// Chamada a cada sincronização, porque a resposta muda: uma conta renovada volta a ter data
// futura, e o aviso precisa sumir sozinho quando o problema for resolvido — sem obrigar
// ninguém a limpá-lo à mão.
func (s *Store) AnotarAssinaturaDaFonte(ctx context.Context, id int64, expira *time.Time, status string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sources
		SET assinatura_expira_em = $2, assinatura_status = $3, assinatura_vista_em = now()
		WHERE id = $1`, id, expira, status)
	return wrapErr("anotando a validade da fonte", err)
}
