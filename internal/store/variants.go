package store

import (
	"context"
	"encoding/json"
	"time"
)

// SourceVariant é uma origem concreta de um conteúdo.
//
// origin_url tem tag `json:"-"`: ela NUNCA sai numa resposta de API. O endpoint que a
// expõe ao administrador a lê por uma consulta dedicada e registra o acesso.
type SourceVariant struct {
	ID             int64           `json:"id"`
	SourceID       int64           `json:"source_id"`
	SourceName     string          `json:"source_name,omitempty"`
	SourcePriority int             `json:"source_priority,omitempty"`
	TargetKind     string          `json:"target_kind"`
	TargetID       int64           `json:"target_id"`
	ExternalID     string          `json:"external_id"`
	URLHash        string          `json:"-"`
	OriginURL      string          `json:"-"`
	StreamRef      json.RawMessage `json:"-"`
	ContainerExt   string          `json:"container_ext"`
	DeclaredTitle  string          `json:"declared_title"`
	DeclaredGroup  string          `json:"declared_group"`
	QualityTags    []string        `json:"quality_tags"`
	LanguageTags   []string        `json:"language_tags"`
	Digest         string          `json:"-"`
	Enabled        bool            `json:"enabled"`
	Available      bool            `json:"available"`
	FirstSeenAt    time.Time       `json:"first_seen_at"`
	LastSeenAt     time.Time       `json:"last_seen_at"`
	MissingSince   *time.Time      `json:"missing_since"`
	MissingCount   int             `json:"missing_count"`
}

const variantColumns = `v.id, v.source_id, v.target_kind, v.target_id, v.external_id, v.url_hash,
	v.origin_url, v.stream_ref, v.container_ext, v.declared_title, v.declared_group,
	v.quality_tags, v.language_tags, v.digest, v.enabled, v.available,
	v.first_seen_at, v.last_seen_at, v.missing_since, v.missing_count`

func scanVariant(row rowScanner) (*SourceVariant, error) {
	var v SourceVariant
	if err := row.Scan(&v.ID, &v.SourceID, &v.TargetKind, &v.TargetID, &v.ExternalID, &v.URLHash,
		&v.OriginURL, &v.StreamRef, &v.ContainerExt, &v.DeclaredTitle, &v.DeclaredGroup,
		&v.QualityTags, &v.LanguageTags, &v.Digest, &v.Enabled, &v.Available,
		&v.FirstSeenAt, &v.LastSeenAt, &v.MissingSince, &v.MissingCount); err != nil {
		return nil, err
	}
	if v.QualityTags == nil {
		v.QualityTags = []string{}
	}
	if v.LanguageTags == nil {
		v.LanguageTags = []string{}
	}
	return &v, nil
}

// NewVariant são os campos de uma variante nova.
type NewVariant struct {
	SourceID      int64
	TargetKind    string
	TargetID      int64
	ExternalID    string
	URLHash       string
	OriginURL     string
	StreamRef     json.RawMessage
	ContainerExt  string
	DeclaredTitle string
	DeclaredGroup string
	QualityTags   []string
	LanguageTags  []string
	Digest        string
	RawPayload    json.RawMessage
}

// FindVariantByIdentity localiza uma variante pela identidade estável.
//
// external_id tem precedência sobre url_hash (docs/07 §4.1): é por isso que a busca por
// id vem primeiro e a de hash só é tentada quando não há id.
func (s *Store) FindVariantByIdentity(ctx context.Context, sourceID int64, externalID, urlHash string) (*SourceVariant, error) {
	if externalID != "" {
		row := s.pool.QueryRow(ctx,
			`SELECT `+variantColumns+` FROM source_variants v WHERE v.source_id = $1 AND v.external_id = $2`,
			sourceID, externalID)
		v, err := scanVariant(row)
		return v, wrapErr("buscando variante", err)
	}
	row := s.pool.QueryRow(ctx,
		`SELECT `+variantColumns+` FROM source_variants v
		 WHERE v.source_id = $1 AND v.external_id = '' AND v.url_hash = $2`,
		sourceID, urlHash)
	v, err := scanVariant(row)
	return v, wrapErr("buscando variante", err)
}

// CreateVariant insere uma variante nova.
func (s *Store) CreateVariant(ctx context.Context, in NewVariant) (*SourceVariant, error) {
	if len(in.RawPayload) == 0 {
		in.RawPayload = json.RawMessage("{}")
	}
	row := s.pool.QueryRow(ctx, `
		WITH inserida AS (
			INSERT INTO source_variants (source_id, target_kind, target_id, external_id, url_hash,
				origin_url, stream_ref, container_ext, declared_title, declared_group,
				quality_tags, language_tags, digest, raw_payload)
			VALUES ($1,$2,$3,coalesce($4,''),coalesce($5,''),coalesce($6,''),$7,coalesce($8,''),
			        coalesce($9,''),coalesce($10,''),coalesce($11::text[],'{}'),coalesce($12::text[],'{}'),
			        coalesce($13,''),$14)
			RETURNING *
		)
		SELECT `+variantColumns+` FROM inserida v`,
		in.SourceID, in.TargetKind, in.TargetID, in.ExternalID, in.URLHash,
		in.OriginURL, in.StreamRef, in.ContainerExt, in.DeclaredTitle, in.DeclaredGroup,
		in.QualityTags, in.LanguageTags, in.Digest, in.RawPayload)
	v, err := scanVariant(row)
	return v, wrapErr("criando variante", err)
}

// UpdateVariantFromSync atualiza uma variante existente cujo conteúdo mudou.
//
// O ALVO (target_kind/target_id) não é alterado aqui de propósito: reagrupar um item já
// classificado desfaria decisões de matching a cada sincronização — inclusive as travadas
// pelo administrador.
func (s *Store) UpdateVariantFromSync(ctx context.Context, id int64, in NewVariant) error {
	if len(in.RawPayload) == 0 {
		in.RawPayload = json.RawMessage("{}")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE source_variants SET
			origin_url     = coalesce($2,''),
			stream_ref     = $3,
			container_ext  = coalesce($4,''),
			declared_title = coalesce($5,''),
			declared_group = coalesce($6,''),
			quality_tags   = coalesce($7::text[],'{}'),
			language_tags  = coalesce($8::text[],'{}'),
			digest         = coalesce($9,''),
			raw_payload    = $10,
			url_hash       = coalesce($11,''),
			available      = true,
			missing_since  = NULL,
			missing_count  = 0,
			last_seen_at   = now(),
			updated_at     = now()
		WHERE id = $1`,
		id, in.OriginURL, in.StreamRef, in.ContainerExt, in.DeclaredTitle, in.DeclaredGroup,
		in.QualityTags, in.LanguageTags, in.Digest, in.RawPayload, in.URLHash)
	return wrapErr("atualizando variante", err)
}

// TouchVariants marca em LOTE as variantes vistas nesta sincronização.
//
// Escrita em lote é o que mantém a sincronização barata quando nada mudou: um catálogo
// de 50 mil itens inalterados vira algumas dezenas de UPDATEs, não 50 mil.
func (s *Store) TouchVariants(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE source_variants
		SET last_seen_at = now(), available = true, missing_since = NULL, missing_count = 0
		WHERE id = ANY($1::bigint[])`, ids)
	return wrapErr("marcando variantes vistas", err)
}

// MarkMissingResult resume o efeito de uma passagem de ausências.
type MarkMissingResult struct {
	Marked      int64 `json:"marked"`
	Unavailable int64 `json:"unavailable"`
}

// MarkMissingVariants processa as variantes que NÃO apareceram na sincronização.
//
// Regra da doc 03 §7: nada é apagado. Incrementamos o contador de ausências e só depois
// do período de tolerância a variante é marcada indisponível.
func (s *Store) MarkMissingVariants(ctx context.Context, sourceID int64, runStart time.Time, tolerance int) (MarkMissingResult, error) {
	var res MarkMissingResult

	tag, err := s.pool.Exec(ctx, `
		UPDATE source_variants
		SET missing_count = missing_count + 1,
		    missing_since = coalesce(missing_since, now()),
		    updated_at    = now()
		WHERE source_id = $1 AND last_seen_at < $2 AND available`,
		sourceID, runStart)
	if err != nil {
		return res, wrapErr("marcando ausências", err)
	}
	res.Marked = tag.RowsAffected()

	tag, err = s.pool.Exec(ctx, `
		UPDATE source_variants
		SET available = false, updated_at = now()
		WHERE source_id = $1 AND available AND missing_count > $2`,
		sourceID, tolerance)
	if err != nil {
		return res, wrapErr("marcando indisponíveis", err)
	}
	res.Unavailable = tag.RowsAffected()
	return res, nil
}

// ListVariantsForTarget devolve as variantes de um conteúdo ou episódio, já na ordem de
// preferência: overrides manuais primeiro, depois a prioridade da fonte.
func (s *Store) ListVariantsForTarget(ctx context.Context, kind string, id int64) ([]SourceVariant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+variantColumns+`, s.name, s.priority
		FROM source_variants v
		JOIN sources s ON s.id = v.source_id
		WHERE v.target_kind = $1 AND v.target_id = $2
		ORDER BY s.priority, v.id`, kind, id)
	if err != nil {
		return nil, wrapErr("listando variantes", err)
	}
	defer rows.Close()

	out := []SourceVariant{}
	for rows.Next() {
		var v SourceVariant
		if err := rows.Scan(&v.ID, &v.SourceID, &v.TargetKind, &v.TargetID, &v.ExternalID, &v.URLHash,
			&v.OriginURL, &v.StreamRef, &v.ContainerExt, &v.DeclaredTitle, &v.DeclaredGroup,
			&v.QualityTags, &v.LanguageTags, &v.Digest, &v.Enabled, &v.Available,
			&v.FirstSeenAt, &v.LastSeenAt, &v.MissingSince, &v.MissingCount,
			&v.SourceName, &v.SourcePriority); err != nil {
			return nil, wrapErr("listando variantes", err)
		}
		if v.QualityTags == nil {
			v.QualityTags = []string{}
		}
		if v.LanguageTags == nil {
			v.LanguageTags = []string{}
		}
		out = append(out, v)
	}
	return out, wrapErr("listando variantes", rows.Err())
}

// RecordMatchDecision grava a decisão de agrupamento de uma variante.
//
// Uma decisão travada pelo administrador NUNCA é sobrescrita pelo algoritmo: o
// ON CONFLICT verifica `locked` antes de atualizar (docs/07 §7.5).
func (s *Store) RecordMatchDecision(ctx context.Context, variantID int64, kind string, targetID int64, actor, decision string, confidence int, signals any, locked bool, note string) error {
	payload, err := json.Marshal(signals)
	if err != nil {
		payload = []byte("{}")
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO match_decisions (variant_id, target_kind, target_id, actor, decision, confidence, signals, locked, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,coalesce($9,''))
		ON CONFLICT (variant_id) DO UPDATE SET
			target_kind = excluded.target_kind,
			target_id   = excluded.target_id,
			actor       = excluded.actor,
			decision    = excluded.decision,
			confidence  = excluded.confidence,
			signals     = excluded.signals,
			locked      = excluded.locked,
			note        = excluded.note,
			created_at  = now()
		WHERE NOT match_decisions.locked`,
		variantID, kind, targetID, actor, decision, confidence, payload, locked, note)
	return wrapErr("gravando decisão de matching", err)
}

// GetVariant busca uma variante por id.
func (s *Store) GetVariant(ctx context.Context, id int64) (*SourceVariant, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+variantColumns+` FROM source_variants v WHERE v.id = $1`, id)
	v, err := scanVariant(row)
	return v, wrapErr("buscando variante", err)
}

// IsMatchLocked informa se a variante tem decisão manual travada.
func (s *Store) IsMatchLocked(ctx context.Context, variantID int64) (bool, error) {
	var locked bool
	err := s.pool.QueryRow(ctx,
		`SELECT coalesce(bool_or(locked), false) FROM match_decisions WHERE variant_id = $1`,
		variantID).Scan(&locked)
	return locked, wrapErr("consultando trava de matching", err)
}
