package store

import (
	"context"
	"encoding/json"
	"time"
)

// Alvos possíveis de uma variante.
const (
	TargetContent = "content"
	TargetEpisode = "episode"
)

// Tipos de conteúdo.
const (
	ContentMovie  = "movie"
	ContentSeries = "series"
)

// Content é um conteúdo lógico do catálogo.
type Content struct {
	ID               int64      `json:"id"`
	Type             string     `json:"type"`
	Title            string     `json:"title"`
	NormalizedTitle  string     `json:"normalized_title"`
	LanguageKey      string     `json:"language_key"`
	Year             *int       `json:"year"`
	TMDBID           *string    `json:"tmdb_id"`
	IMDBID           *string    `json:"imdb_id"`
	PosterURL        string     `json:"poster_url"`
	BackdropURL      string     `json:"backdrop_url"`
	Plot             string     `json:"plot"`
	Rating           *float64   `json:"rating"`
	DurationSeconds  *int       `json:"duration_seconds"`
	CategoryID       *int64     `json:"category_id"`
	Status           string     `json:"status"`
	Preserved        bool       `json:"preserved"`
	PrimaryVariant   *int64     `json:"primary_variant_id"`
	SecondaryVariant *int64     `json:"secondary_variant_id"`
	TertiaryVariant  *int64     `json:"tertiary_variant_id"`
	AccessCount      int64      `json:"access_count"`
	LastAccessAt     *time.Time `json:"last_access_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	// VariantCount é preenchido nas listagens.
	VariantCount int `json:"variant_count,omitempty"`
}

const contentColumns = `id, type, title, normalized_title, language_key, year, tmdb_id, imdb_id,
	poster_url, backdrop_url, plot, rating, duration_seconds, category_id, status, preserved,
	primary_variant_id, secondary_variant_id, tertiary_variant_id,
	access_count, last_access_at, created_at, updated_at`

func scanContent(row rowScanner) (*Content, error) {
	var c Content
	if err := row.Scan(&c.ID, &c.Type, &c.Title, &c.NormalizedTitle, &c.LanguageKey, &c.Year, &c.TMDBID, &c.IMDBID,
		&c.PosterURL, &c.BackdropURL, &c.Plot, &c.Rating, &c.DurationSeconds, &c.CategoryID,
		&c.Status, &c.Preserved, &c.PrimaryVariant, &c.SecondaryVariant, &c.TertiaryVariant,
		&c.AccessCount, &c.LastAccessAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// NewContent são os campos aceitos ao criar um conteúdo.
type NewContent struct {
	Type            string
	Title           string
	NormalizedTitle string
	LanguageKey     string
	Year            *int
	TMDBID          *string
	IMDBID          *string
	PosterURL       string
	BackdropURL     string
	Plot            string
	Rating          *float64
	DurationSeconds *int
	CategoryID      *int64
}

// CreateContent insere um conteúdo.
func (s *Store) CreateContent(ctx context.Context, in NewContent) (*Content, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO contents (type, title, normalized_title, language_key, year, tmdb_id, imdb_id,
			poster_url, backdrop_url, plot, rating, duration_seconds, category_id)
		VALUES ($1,$2,$3,coalesce($4,''),$5,$6,$7,coalesce($8,''),coalesce($9,''),coalesce($10,''),$11,$12,$13)
		RETURNING `+contentColumns,
		in.Type, in.Title, in.NormalizedTitle, in.LanguageKey, in.Year, in.TMDBID, in.IMDBID,
		in.PosterURL, in.BackdropURL, in.Plot, in.Rating, in.DurationSeconds, in.CategoryID)
	c, err := scanContent(row)
	return c, wrapErr("criando conteúdo", err)
}

// GetContent busca por id.
func (s *Store) GetContent(ctx context.Context, id int64) (*Content, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+contentColumns+` FROM contents WHERE id = $1`, id)
	c, err := scanContent(row)
	return c, wrapErr("buscando conteúdo", err)
}

// EnrichContent completa campos vazios de um conteúdo com o que uma nova variante trouxe.
//
// Só preenche o que está faltando: nunca sobrescreve dado já existente, porque a primeira
// fonte a descrever um conteúdo costuma ser a que o administrador conferiu.
func (s *Store) EnrichContent(ctx context.Context, id int64, in NewContent) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE contents SET
			year             = coalesce(year, $2::int),
			tmdb_id          = coalesce(tmdb_id, $3::text),
			imdb_id          = coalesce(imdb_id, $4::text),
			poster_url       = CASE WHEN poster_url = '' THEN coalesce($5::text,'') ELSE poster_url END,
			backdrop_url     = CASE WHEN backdrop_url = '' THEN coalesce($6::text,'') ELSE backdrop_url END,
			plot             = CASE WHEN plot = '' THEN coalesce($7::text,'') ELSE plot END,
			rating           = coalesce(rating, $8::numeric),
			duration_seconds = coalesce(duration_seconds, $9::int),
			category_id      = coalesce(category_id, $10::bigint),
			updated_at       = now()
		WHERE id = $1`,
		id, in.Year, in.TMDBID, in.IMDBID, in.PosterURL, in.BackdropURL, in.Plot,
		in.Rating, in.DurationSeconds, in.CategoryID)
	return wrapErr("enriquecendo conteúdo", err)
}

// ContentCandidate é um candidato a agrupamento devolvido pelo banco.
type ContentCandidate struct {
	ID              int64
	Type            string
	NormalizedTitle string
	LanguageKey     string
	Year            *int
	TMDBID          *string
	IMDBID          *string
}

// FindContentCandidates gera candidatos baratos para o matching.
//
// A query é apenas o FILTRO: id externo igual, ou título parecido dentro de uma janela
// de anos. A DECISÃO é sempre tomada em Go, por ingest.Score, para que a mesma regra
// valha em todo lugar e seja testável sem banco.
func (s *Store) FindContentCandidates(ctx context.Context, tipo, normalizedTitle string, year *int, tmdbID, imdbID *string, limit int) ([]ContentCandidate, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, type, normalized_title, language_key, year, tmdb_id, imdb_id
		FROM contents
		WHERE type = $1
		  AND status <> 'deleted'
		  AND (
		        ($2::text IS NOT NULL AND tmdb_id = $2)
		     OR ($3::text IS NOT NULL AND imdb_id = $3)
		     OR (
		          normalized_title % $4
		          AND ($5::int IS NULL OR year IS NULL OR abs(year - $5) <= 1)
		        )
		  )
		ORDER BY similarity(normalized_title, $4) DESC
		LIMIT $6`,
		tipo, tmdbID, imdbID, normalizedTitle, year, limit)
	if err != nil {
		return nil, wrapErr("buscando candidatos", err)
	}
	defer rows.Close()

	out := []ContentCandidate{}
	for rows.Next() {
		var c ContentCandidate
		if err := rows.Scan(&c.ID, &c.Type, &c.NormalizedTitle, &c.LanguageKey,
			&c.Year, &c.TMDBID, &c.IMDBID); err != nil {
			return nil, wrapErr("buscando candidatos", err)
		}
		out = append(out, c)
	}
	return out, wrapErr("buscando candidatos", rows.Err())
}

// FindSeriesByTitle procura uma série pelo título normalizado exato.
//
// Séries usam correspondência exata de título porque o agrupamento errado de duas séries
// distintas propaga para todos os episódios de ambas.
func (s *Store) FindSeriesByTitle(ctx context.Context, normalizedTitle string, year *int) (*Content, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+contentColumns+`
		FROM contents
		WHERE type = 'series' AND normalized_title = $1 AND status <> 'deleted'
		ORDER BY (year IS NOT DISTINCT FROM $2::int) DESC, id
		LIMIT 1`, normalizedTitle, year)
	c, err := scanContent(row)
	return c, wrapErr("buscando série", err)
}

// EnsureSeason cria a temporada se ela não existir.
func (s *Store) EnsureSeason(ctx context.Context, seriesID int64, number int) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO seasons (series_content_id, season_number)
		VALUES ($1, $2)
		ON CONFLICT (series_content_id, season_number) DO UPDATE SET updated_at = now()
		RETURNING id`, seriesID, number).Scan(&id)
	return id, wrapErr("garantindo temporada", err)
}

// Episode é um episódio do catálogo.
type Episode struct {
	ID               int64      `json:"id"`
	SeasonID         int64      `json:"season_id"`
	EpisodeNumber    int        `json:"episode_number"`
	Title            string     `json:"title"`
	Plot             string     `json:"plot"`
	DurationSeconds  *int       `json:"duration_seconds"`
	PosterURL        string     `json:"poster_url"`
	Status           string     `json:"status"`
	Preserved        bool       `json:"preserved"`
	PrimaryVariant   *int64     `json:"primary_variant_id"`
	SecondaryVariant *int64     `json:"secondary_variant_id"`
	TertiaryVariant  *int64     `json:"tertiary_variant_id"`
	AccessCount      int64      `json:"access_count"`
	LastAccessAt     *time.Time `json:"last_access_at"`
	// SeasonNumber e SeriesTitle são preenchidos nas listagens.
	SeasonNumber int    `json:"season_number,omitempty"`
	SeriesTitle  string `json:"series_title,omitempty"`
	VariantCount int    `json:"variant_count,omitempty"`
}

// EnsureEpisode cria o episódio se não existir, e completa campos vazios se já existir.
func (s *Store) EnsureEpisode(ctx context.Context, seasonID int64, number int, title, plot, poster string, duration *int) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO episodes (season_id, episode_number, title, plot, poster_url, duration_seconds)
		VALUES ($1, $2, coalesce($3,''), coalesce($4,''), coalesce($5,''), $6)
		ON CONFLICT (season_id, episode_number) DO UPDATE SET
			title            = CASE WHEN episodes.title = '' THEN excluded.title ELSE episodes.title END,
			plot             = CASE WHEN episodes.plot  = '' THEN excluded.plot  ELSE episodes.plot  END,
			poster_url       = CASE WHEN episodes.poster_url = '' THEN excluded.poster_url ELSE episodes.poster_url END,
			duration_seconds = coalesce(episodes.duration_seconds, excluded.duration_seconds),
			updated_at       = now()
		RETURNING id`, seasonID, number, title, plot, poster, duration).Scan(&id)
	return id, wrapErr("garantindo episódio", err)
}

// --- Categorias --------------------------------------------------------------

// EnsureCategory cria (ou reaproveita) a categoria canônica.
func (s *Store) EnsureCategory(ctx context.Context, name, normalized, contentType string) (int64, error) {
	if normalized == "" {
		return 0, wrapErr("garantindo categoria", ErrInvalid)
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO categories (name, normalized_name, content_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (normalized_name, content_type) DO UPDATE SET name = categories.name
		RETURNING id`, name, normalized, contentType).Scan(&id)
	return id, wrapErr("garantindo categoria", err)
}

// UpsertSourceCategory registra a categoria como a fonte a declara e a vincula à canônica.
//
// categoryID igual a zero grava NULL: é o caso em que a fonte não informa se a categoria
// é de filme ou de série, e o vínculo canônico só será possível quando os itens
// definirem o tipo.
func (s *Store) UpsertSourceCategory(ctx context.Context, sourceID int64, externalID, declared, normalized, contentType string, categoryID int64) error {
	var canonica *int64
	if categoryID > 0 {
		canonica = &categoryID
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO source_categories (source_id, external_id, declared_name, normalized_name, content_type, category_id)
		VALUES ($1, coalesce($2,''), $3, $4, $5, $6)
		ON CONFLICT (source_id, content_type, normalized_name) DO UPDATE SET
			external_id  = excluded.external_id,
			declared_name = excluded.declared_name,
			category_id  = coalesce(source_categories.category_id, excluded.category_id),
			last_seen_at = now()`,
		sourceID, externalID, declared, normalized, contentType, canonica)
	return wrapErr("registrando categoria da fonte", err)
}

// ListCategories devolve as categorias canônicas com a contagem de conteúdos.
func (s *Store) ListCategories(ctx context.Context) ([]CategoryWithCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.name, c.normalized_name, c.content_type, c.principal,
		       count(ct.id) FILTER (WHERE ct.status <> 'deleted')
		FROM categories c
		LEFT JOIN contents ct ON ct.category_id = c.id
		GROUP BY c.id
		ORDER BY c.principal DESC, c.content_type, c.name`)
	if err != nil {
		return nil, wrapErr("listando categorias", err)
	}
	defer rows.Close()

	out := []CategoryWithCount{}
	for rows.Next() {
		var c CategoryWithCount
		if err := rows.Scan(&c.ID, &c.Name, &c.NormalizedName, &c.ContentType,
			&c.Principal, &c.ContentCount); err != nil {
			return nil, wrapErr("listando categorias", err)
		}
		out = append(out, c)
	}
	return out, wrapErr("listando categorias", rows.Err())
}

// SourceCategory é uma categoria como uma fonte a declara, com seu vínculo canônico.
type SourceCategory struct {
	ID             int64  `json:"id"`
	SourceID       int64  `json:"source_id"`
	SourceName     string `json:"source_name"`
	DeclaredName   string `json:"declared_name"`
	NormalizedName string `json:"normalized_name"`
	ContentType    string `json:"content_type"`
	CategoryID     *int64 `json:"category_id"`
	CategoryName   string `json:"category_name"`
	ItemCount      int64  `json:"item_count"`
	// Suggestions são categorias canônicas parecidas, ordenadas da mais parecida para a
	// menos. Serve para o painel propor a unificação em vez de deixar o administrador
	// caçar duplicata a olho.
	Suggestions []CategorySuggestion `json:"suggestions"`
}

// CategorySuggestion é uma categoria canônica candidata a receber uma categoria de fonte.
type CategorySuggestion struct {
	CategoryID int64   `json:"category_id"`
	Name       string  `json:"name"`
	Similarity float64 `json:"similarity"`
	ItemCount  int64   `json:"item_count"`
}

// ListSourceCategories devolve as categorias declaradas por todas as fontes.
func (s *Store) ListSourceCategories(ctx context.Context) ([]SourceCategory, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sc.id, sc.source_id, s.name, sc.declared_name, sc.normalized_name,
		       sc.content_type, sc.category_id, coalesce(c.name, ''),
		       (SELECT count(*) FROM source_variants v
		        WHERE v.source_id = sc.source_id AND v.declared_group = sc.declared_name)
		FROM source_categories sc
		JOIN sources s ON s.id = sc.source_id
		LEFT JOIN categories c ON c.id = sc.category_id
		ORDER BY s.priority, sc.declared_name`)
	if err != nil {
		return nil, wrapErr("listando categorias das fontes", err)
	}
	defer rows.Close()

	out := []SourceCategory{}
	for rows.Next() {
		var sc SourceCategory
		if err := rows.Scan(&sc.ID, &sc.SourceID, &sc.SourceName, &sc.DeclaredName,
			&sc.NormalizedName, &sc.ContentType, &sc.CategoryID, &sc.CategoryName,
			&sc.ItemCount); err != nil {
			return nil, wrapErr("listando categorias das fontes", err)
		}
		sc.Suggestions = []CategorySuggestion{}
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapErr("listando categorias das fontes", err)
	}

	for i := range out {
		sug, err := s.SuggestCategories(ctx, out[i].NormalizedName, out[i].CategoryID, 4)
		if err != nil {
			return nil, err
		}
		out[i].Suggestions = sug
	}
	return out, nil
}

// SuggestCategories procura categorias canônicas com nome parecido.
//
// Usa similaridade por trigramas do Postgres, o mesmo mecanismo do matching de títulos.
// O limiar é baixo de propósito: sugerir demais custa uma linha na tela, sugerir de menos
// deixa o administrador sem a informação que ele precisa para unificar.
func (s *Store) SuggestCategories(ctx context.Context, normalizado string, excluir *int64, limite int) ([]CategorySuggestion, error) {
	if normalizado == "" {
		return []CategorySuggestion{}, nil
	}
	if limite <= 0 {
		limite = 5
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.name, similarity(c.normalized_name, $1),
		       (SELECT count(*) FROM contents ct WHERE ct.category_id = c.id)
		FROM categories c
		WHERE ($2::bigint IS NULL OR c.id <> $2)
		  AND similarity(c.normalized_name, $1) >= 0.3
		ORDER BY similarity(c.normalized_name, $1) DESC, c.name
		LIMIT $3`, normalizado, excluir, limite)
	if err != nil {
		return nil, wrapErr("sugerindo categorias", err)
	}
	defer rows.Close()

	out := []CategorySuggestion{}
	for rows.Next() {
		var sug CategorySuggestion
		if err := rows.Scan(&sug.CategoryID, &sug.Name, &sug.Similarity, &sug.ItemCount); err != nil {
			return nil, wrapErr("sugerindo categorias", err)
		}
		out = append(out, sug)
	}
	return out, wrapErr("sugerindo categorias", rows.Err())
}

// MapSourceCategory vincula uma categoria de fonte a uma categoria canônica e move os
// conteúdos correspondentes.
//
// Tudo numa transação: um remapeamento pela metade deixaria conteúdos apontando para uma
// categoria e a fonte para outra.
func (s *Store) MapSourceCategory(ctx context.Context, sourceCategoryID, categoryID int64) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, wrapErr("remapeando categoria", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var sourceID int64
	var declarada string
	err = tx.QueryRow(ctx, `
		UPDATE source_categories SET category_id = $2 WHERE id = $1
		RETURNING source_id, declared_name`, sourceCategoryID, categoryID).
		Scan(&sourceID, &declarada)
	if err != nil {
		return 0, wrapErr("remapeando categoria", err)
	}

	// Move os conteúdos que chegaram por esta fonte com esta categoria declarada.
	// Um conteúdo com variantes de várias fontes fica com a categoria da última
	// remapeada — o administrador tem a palavra final, e é ele quem está agindo aqui.
	tag, err := tx.Exec(ctx, `
		UPDATE contents SET category_id = $1, updated_at = now()
		WHERE id IN (
			SELECT DISTINCT v.target_id FROM source_variants v
			WHERE v.source_id = $2 AND v.declared_group = $3 AND v.target_kind = 'content'
			UNION
			SELECT DISTINCT se.series_content_id
			FROM source_variants v
			JOIN episodes e ON e.id = v.target_id
			JOIN seasons se ON se.id = e.season_id
			WHERE v.source_id = $2 AND v.declared_group = $3 AND v.target_kind = 'episode'
		)`, categoryID, sourceID, declarada)
	if err != nil {
		return 0, wrapErr("movendo conteúdos", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, wrapErr("remapeando categoria", err)
	}
	return tag.RowsAffected(), nil
}

// SourceCategoryContentType devolve o tipo (movie/series) de uma categoria de fonte.
func (s *Store) SourceCategoryContentType(ctx context.Context, id int64) (string, error) {
	var tipo string
	err := s.pool.QueryRow(ctx, `SELECT content_type FROM source_categories WHERE id = $1`, id).Scan(&tipo)
	if err != nil {
		return "", wrapErr("buscando categoria da fonte", err)
	}
	// "unknown" não pode virar categoria canônica: o schema separa filmes de séries.
	if tipo != ContentMovie && tipo != ContentSeries {
		tipo = ContentMovie
	}
	return tipo, nil
}

// RenameCategory troca o nome de exibição de uma categoria canônica.
func (s *Store) RenameCategory(ctx context.Context, id int64, nome string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE categories SET name = $2 WHERE id = $1`, id, nome)
	if err != nil {
		return wrapErr("renomeando categoria", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("renomeando categoria", ErrNotFound)
	}
	return nil
}

// CategoryWithCount é uma categoria com quantos conteúdos ela tem.
type CategoryWithCount struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	NormalizedName string `json:"normalized_name"`
	ContentType    string `json:"content_type"`
	ContentCount   int64  `json:"content_count"`
	// Principal diz se esta é uma das pastas que o administrador escolheu manter. Sem
	// este campo na listagem, o painel não tem como saber que a marcação foi aplicada —
	// o botão gravaria no banco e a tela continuaria mostrando o estado antigo.
	Principal bool `json:"principal"`
}

// --- Itens não resolvidos ----------------------------------------------------

// UpsertUnresolved registra (ou recontabiliza) um item que não pôde ser classificado.
func (s *Store) UpsertUnresolved(ctx context.Context, sourceID int64, identityKey, title, group, reason, detail string, payload json.RawMessage) error {
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO unresolved_items (source_id, identity_key, declared_title, declared_group, reason, detail, raw_payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (source_id, identity_key) DO UPDATE SET
			declared_title = excluded.declared_title,
			declared_group = excluded.declared_group,
			reason         = excluded.reason,
			detail         = excluded.detail,
			raw_payload    = excluded.raw_payload,
			occurrences    = unresolved_items.occurrences + 1,
			last_seen_at   = now(),
			resolved_at    = NULL,
			resolution     = NULL`,
		sourceID, identityKey, title, group, reason, detail, payload)
	return wrapErr("registrando item não resolvido", err)
}

// UnresolvedItem é uma entrada da fila de revisão.
type UnresolvedItem struct {
	ID            int64     `json:"id"`
	SourceID      int64     `json:"source_id"`
	SourceName    string    `json:"source_name"`
	DeclaredTitle string    `json:"declared_title"`
	DeclaredGroup string    `json:"declared_group"`
	Reason        string    `json:"reason"`
	Detail        string    `json:"detail"`
	Occurrences   int       `json:"occurrences"`
	FirstSeenAt   time.Time `json:"first_seen_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
}

// ListUnresolved devolve a fila de itens pendentes de revisão.
func (s *Store) ListUnresolved(ctx context.Context, sourceID *int64, limit int) ([]UnresolvedItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.source_id, s.name, u.declared_title, u.declared_group,
		       u.reason, u.detail, u.occurrences, u.first_seen_at, u.last_seen_at
		FROM unresolved_items u
		JOIN sources s ON s.id = u.source_id
		WHERE u.resolved_at IS NULL AND ($1::bigint IS NULL OR u.source_id = $1)
		ORDER BY u.last_seen_at DESC
		LIMIT $2`, sourceID, limit)
	if err != nil {
		return nil, wrapErr("listando não resolvidos", err)
	}
	defer rows.Close()

	out := []UnresolvedItem{}
	for rows.Next() {
		var u UnresolvedItem
		if err := rows.Scan(&u.ID, &u.SourceID, &u.SourceName, &u.DeclaredTitle, &u.DeclaredGroup,
			&u.Reason, &u.Detail, &u.Occurrences, &u.FirstSeenAt, &u.LastSeenAt); err != nil {
			return nil, wrapErr("listando não resolvidos", err)
		}
		out = append(out, u)
	}
	return out, wrapErr("listando não resolvidos", rows.Err())
}

// OrphanCleanupPreview resume o que uma limpeza removeria.
type OrphanCleanupPreview struct {
	Movies     int64 `json:"movies"`
	Series     int64 `json:"series"`
	Episodes   int64 `json:"episodes"`
	Categories int64 `json:"empty_categories"`
}

// contentsSemOrigem é a definição de "conteúdo sem nenhuma origem": nenhuma variante
// aponta para ele (nem para seus episódios, no caso de série) e ele não está protegido.
const contentsSemOrigem = `
	SELECT c.id FROM contents c
	WHERE NOT c.preserved
	  AND NOT EXISTS (
	        SELECT 1 FROM source_variants v
	        WHERE v.target_kind = 'content' AND v.target_id = c.id)
	  AND NOT EXISTS (
	        SELECT 1 FROM source_variants v
	        JOIN episodes e ON e.id = v.target_id
	        JOIN seasons se ON se.id = e.season_id
	        WHERE v.target_kind = 'episode' AND se.series_content_id = c.id)`

// PreviewOrphanCleanup conta o que seria removido, sem remover nada.
//
// Uma exclusão em massa nunca deve ser feita às cegas: o administrador precisa ver o
// tamanho do estrago antes de confirmar.
func (s *Store) PreviewOrphanCleanup(ctx context.Context) (*OrphanCleanupPreview, error) {
	var p OrphanCleanupPreview
	err := s.pool.QueryRow(ctx, `
		WITH sem_origem AS (`+contentsSemOrigem+`)
		SELECT
			(SELECT count(*) FROM contents WHERE id IN (SELECT id FROM sem_origem) AND type = 'movie'),
			(SELECT count(*) FROM contents WHERE id IN (SELECT id FROM sem_origem) AND type = 'series'),
			(SELECT count(*) FROM episodes e
			 JOIN seasons se ON se.id = e.season_id
			 WHERE se.series_content_id IN (SELECT id FROM sem_origem)),
			(SELECT count(*) FROM categories c
			 WHERE NOT EXISTS (
			     SELECT 1 FROM contents ct
			     WHERE ct.category_id = c.id AND ct.id NOT IN (SELECT id FROM sem_origem)))`).
		Scan(&p.Movies, &p.Series, &p.Episodes, &p.Categories)
	if err != nil {
		return nil, wrapErr("prevendo limpeza", err)
	}
	return &p, nil
}

// PurgeOrphanContents remove conteúdos que ficaram sem nenhuma origem.
//
// Só é executada por ação EXPLÍCITA do administrador. O sistema jamais apaga conteúdo
// sozinho — a preservação é um princípio do projeto (docs/04 §49). Conteúdos marcados
// como preservados nunca são tocados, mesmo aqui.
func (s *Store) PurgeOrphanContents(ctx context.Context) (*OrphanCleanupPreview, error) {
	previa, err := s.PreviewOrphanCleanup(ctx)
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrapErr("limpando órfãos", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// Seasons e episodes caem por ON DELETE CASCADE a partir de contents.
	if _, err := tx.Exec(ctx, `DELETE FROM contents WHERE id IN (`+contentsSemOrigem+`)`); err != nil {
		return nil, wrapErr("limpando órfãos", err)
	}
	// Categorias que ficaram sem nenhum conteúdo não têm mais razão de existir.
	if _, err := tx.Exec(ctx, `
		DELETE FROM categories c
		WHERE NOT EXISTS (SELECT 1 FROM contents ct WHERE ct.category_id = c.id)
		  AND NOT EXISTS (SELECT 1 FROM source_categories sc WHERE sc.category_id = c.id)`); err != nil {
		return nil, wrapErr("limpando categorias vazias", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, wrapErr("limpando órfãos", err)
	}
	return previa, nil
}

// CountUnresolved conta os itens pendentes.
func (s *Store) CountUnresolved(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM unresolved_items WHERE resolved_at IS NULL`).Scan(&n)
	return n, wrapErr("contando não resolvidos", err)
}

// SemCategoria é um conteúdo esperando classificação.
type SemCategoria struct {
	ID     int64
	Tipo   string
	Titulo string
	Ano    *int
	TMDBID *string
}

// ConteudosSemCategoria lista o que está sem pasta.
//
// É a fila da classificação automática. Numa instalação com quatro fontes que declaram tudo
// como "Filmes", isso são milhares de títulos numa pasta só — impossível de navegar, e a
// razão de existir a classificação por gênero.
//
// Ordenado por id para a retomada ser previsível: uma passagem interrompida na metade
// continua de onde parou em vez de reprocessar o começo.
// `deCategoria` zero significa "os que não têm pasta nenhuma".
//
// Qualquer outro valor significa "os que estão NESTA pasta" — e esse é o caso que importa na
// prática. Uma fonte entrega milhares de filmes declarando todos como "Filmes", e eles TÊM
// categoria: uma inútil. A primeira versão disto só enxergava `category_id IS NULL`, e por
// isso não via justamente o problema que veio resolver.
func (s *Store) ConteudosSemCategoria(ctx context.Context, tipo string, deCategoria int64, limite int) ([]SemCategoria, error) {
	if limite <= 0 || limite > 5000 {
		limite = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, type, title, year, tmdb_id
		FROM contents
		WHERE status = 'active'
		  AND (CASE WHEN $3::bigint = 0 THEN category_id IS NULL
		            ELSE category_id = $3::bigint END)
		  AND ($1 = '' OR type = $1)
		ORDER BY id
		LIMIT $2`, tipo, limite, deCategoria)
	if err != nil {
		return nil, wrapErr("listando conteúdos sem categoria", err)
	}
	defer rows.Close()

	out := []SemCategoria{}
	for rows.Next() {
		var c SemCategoria
		if err := rows.Scan(&c.ID, &c.Tipo, &c.Titulo, &c.Ano, &c.TMDBID); err != nil {
			return nil, wrapErr("listando conteúdos sem categoria", err)
		}
		out = append(out, c)
	}
	return out, wrapErr("listando conteúdos sem categoria", rows.Err())
}

// ContarSemCategoria diz quantos títulos esperam classificação.
func (s *Store) ContarSemCategoria(ctx context.Context) (filmes, series int64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE type = 'movie'),
		       count(*) FILTER (WHERE type = 'series')
		FROM contents
		WHERE status = 'active' AND category_id IS NULL`).Scan(&filmes, &series)
	return filmes, series, wrapErr("contando conteúdos sem categoria", err)
}

// DefinirCategoriaDoConteudo põe um título numa pasta.
//
// `de` é de ONDE ele pode sair: zero significa "só se não tiver pasta nenhuma", e um id
// significa "só se estiver exatamente nesta".
//
// Essa condição é o que impede a classificação de sobrepor decisão humana. Ela nunca move um
// título de uma pasta qualquer: move do lugar que quem administra APONTOU — a pasta genérica
// que ele mandou reorganizar — e nada mais. Um filme que alguém já pôs em "Clássicos" fica lá,
// mesmo que a reorganização esteja rodando.
//
// Sem a condição, um segundo trabalhador poderia mover um título que o primeiro acabou de
// classificar, e o resultado dependeria da ordem — que é o tipo de defeito que só aparece com
// carga e nunca se reproduz.
func (s *Store) DefinirCategoriaDoConteudo(ctx context.Context, id, de, para int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE contents SET category_id = $3, updated_at = now()
		WHERE id = $1
		  AND (CASE WHEN $2::bigint = 0 THEN category_id IS NULL
		            ELSE category_id = $2::bigint END)`, id, de, para)
	return wrapErr("definindo a categoria do conteúdo", err)
}
