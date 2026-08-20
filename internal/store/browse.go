package store

import (
	"context"
	"strings"
)

// ContentFilter filtra a listagem do catálogo.
type ContentFilter struct {
	Type             string // "movie" | "series"
	Status           string
	CategoryID       *int64
	Search           string
	OnlyWithVariants bool
	Limit            int
	Offset           int
}

// ContentPage é uma página do catálogo.
type ContentPage struct {
	Items  []Content `json:"items"`
	Total  int64     `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
}

// ListContents devolve uma página do catálogo com a contagem de variantes.
func (s *Store) ListContents(ctx context.Context, f ContentFilter) (*ContentPage, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	busca := strings.TrimSpace(f.Search)

	var total int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM contents c
		WHERE ($1::text IS NULL OR c.type = $1)
		  AND ($2::text IS NULL OR c.status = $2)
		  AND ($3::bigint IS NULL OR c.category_id = $3)
		  AND ($4::text IS NULL OR c.normalized_title ILIKE '%' || $4 || '%' OR c.title ILIKE '%' || $4 || '%')
		  AND c.status <> 'deleted'`,
		nullIfEmpty(f.Type), nullIfEmpty(f.Status), f.CategoryID, nullIfEmpty(busca)).Scan(&total)
	if err != nil {
		return nil, wrapErr("contando conteúdos", err)
	}

	// A contagem de variantes é feita em dois ramos separados, e não numa condição OR.
	//
	// Com o OR, nenhum dos lados podia usar índice: o Postgres varria as 290 mil linhas de
	// source_variants uma vez POR ITEM da página. Cinquenta itens viravam 14 milhões de
	// linhas lidas, e a busca do painel levava 8 segundos.
	//
	// O CASE avalia só o ramo do tipo daquela linha, e cada ramo é indexável.
	rows, err := s.pool.Query(ctx, `
		SELECT `+contentColumns+`,
		       CASE WHEN c.type = 'series' THEN (
		           SELECT count(*)
		           FROM seasons se
		           JOIN episodes e ON e.season_id = se.id
		           JOIN source_variants v
		             ON v.target_kind = 'episode' AND v.target_id = e.id
		           WHERE se.series_content_id = c.id
		       ) ELSE (
		           SELECT count(*)
		           FROM source_variants v
		           WHERE v.target_kind = 'content' AND v.target_id = c.id
		       ) END AS variant_count
		FROM contents c
		WHERE ($1::text IS NULL OR c.type = $1)
		  AND ($2::text IS NULL OR c.status = $2)
		  AND ($3::bigint IS NULL OR c.category_id = $3)
		  AND ($4::text IS NULL OR c.normalized_title ILIKE '%' || $4 || '%' OR c.title ILIKE '%' || $4 || '%')
		  AND c.status <> 'deleted'
		ORDER BY c.title, c.id
		LIMIT $5 OFFSET $6`,
		nullIfEmpty(f.Type), nullIfEmpty(f.Status), f.CategoryID, nullIfEmpty(busca), limit, offset)
	if err != nil {
		return nil, wrapErr("listando conteúdos", err)
	}
	defer rows.Close()

	page := &ContentPage{Items: []Content{}, Total: total, Limit: limit, Offset: offset}
	for rows.Next() {
		var c Content
		if err := rows.Scan(&c.ID, &c.Type, &c.Title, &c.NormalizedTitle, &c.LanguageKey,
			&c.Year, &c.TMDBID, &c.IMDBID,
			&c.PosterURL, &c.BackdropURL, &c.Plot, &c.Rating, &c.DurationSeconds, &c.CategoryID,
			&c.Status, &c.Preserved, &c.PrimaryVariant, &c.SecondaryVariant, &c.TertiaryVariant,
			&c.AccessCount, &c.LastAccessAt, &c.CreatedAt, &c.UpdatedAt, &c.VariantCount); err != nil {
			return nil, wrapErr("listando conteúdos", err)
		}
		page.Items = append(page.Items, c)
	}
	return page, wrapErr("listando conteúdos", rows.Err())
}

// SeasonWithEpisodes é uma temporada com seus episódios.
type SeasonWithEpisodes struct {
	ID           int64     `json:"id"`
	SeasonNumber int       `json:"season_number"`
	Title        string    `json:"title"`
	Episodes     []Episode `json:"episodes"`
}

// ListSeasons devolve as temporadas de uma série com seus episódios.
func (s *Store) ListSeasons(ctx context.Context, seriesID int64) ([]SeasonWithEpisodes, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT se.id, se.season_number, se.title,
		       e.id, e.season_id, e.episode_number, e.title, e.plot, e.duration_seconds,
		       e.poster_url, e.status, e.preserved,
		       e.primary_variant_id, e.secondary_variant_id, e.tertiary_variant_id,
		       e.access_count, e.last_access_at,
		       (SELECT count(*) FROM source_variants v
		        WHERE v.target_kind = 'episode' AND v.target_id = e.id) AS variant_count
		FROM seasons se
		LEFT JOIN episodes e ON e.season_id = se.id
		WHERE se.series_content_id = $1
		ORDER BY se.season_number, e.episode_number`, seriesID)
	if err != nil {
		return nil, wrapErr("listando temporadas", err)
	}
	defer rows.Close()

	var out []SeasonWithEpisodes
	indice := map[int64]int{}
	for rows.Next() {
		var (
			seasonID   int64
			seasonNum  int
			seasonName string
			ep         Episode
			epID       *int64
			epSeason   *int64
			epNum      *int
			epTitle    *string
			epPlot     *string
			epPoster   *string
			epStatus   *string
			epPres     *bool
			epCount    *int64
			epVariants *int
		)
		if err := rows.Scan(&seasonID, &seasonNum, &seasonName,
			&epID, &epSeason, &epNum, &epTitle, &epPlot, &ep.DurationSeconds,
			&epPoster, &epStatus, &epPres,
			&ep.PrimaryVariant, &ep.SecondaryVariant, &ep.TertiaryVariant,
			&epCount, &ep.LastAccessAt, &epVariants); err != nil {
			return nil, wrapErr("listando temporadas", err)
		}

		pos, ok := indice[seasonID]
		if !ok {
			out = append(out, SeasonWithEpisodes{
				ID: seasonID, SeasonNumber: seasonNum, Title: seasonName, Episodes: []Episode{},
			})
			pos = len(out) - 1
			indice[seasonID] = pos
		}
		// LEFT JOIN: uma temporada sem episódios devolve uma linha com colunas nulas.
		if epID == nil {
			continue
		}
		ep.ID, ep.SeasonID = *epID, *epSeason
		ep.EpisodeNumber = derefInt(epNum)
		ep.Title = derefStr(epTitle)
		ep.Plot = derefStr(epPlot)
		ep.PosterURL = derefStr(epPoster)
		ep.Status = derefStr(epStatus)
		ep.Preserved = epPres != nil && *epPres
		ep.AccessCount = derefInt64(epCount)
		ep.VariantCount = derefInt(epVariants)
		ep.SeasonNumber = seasonNum
		out[pos].Episodes = append(out[pos].Episodes, ep)
	}
	if out == nil {
		out = []SeasonWithEpisodes{}
	}
	return out, wrapErr("listando temporadas", rows.Err())
}

// GetEpisode busca um episódio com o contexto da série.
func (s *Store) GetEpisode(ctx context.Context, id int64) (*Episode, error) {
	var e Episode
	err := s.pool.QueryRow(ctx, `
		SELECT e.id, e.season_id, e.episode_number, e.title, e.plot, e.duration_seconds,
		       e.poster_url, e.status, e.preserved,
		       e.primary_variant_id, e.secondary_variant_id, e.tertiary_variant_id,
		       e.access_count, e.last_access_at, se.season_number, c.title
		FROM episodes e
		JOIN seasons se ON se.id = e.season_id
		JOIN contents c ON c.id = se.series_content_id
		WHERE e.id = $1`, id).Scan(
		&e.ID, &e.SeasonID, &e.EpisodeNumber, &e.Title, &e.Plot, &e.DurationSeconds,
		&e.PosterURL, &e.Status, &e.Preserved,
		&e.PrimaryVariant, &e.SecondaryVariant, &e.TertiaryVariant,
		&e.AccessCount, &e.LastAccessAt, &e.SeasonNumber, &e.SeriesTitle)
	if err != nil {
		return nil, wrapErr("buscando episódio", err)
	}
	return &e, nil
}

// CatalogStats são os números do dashboard.
type CatalogStats struct {
	Movies      int64 `json:"movies"`
	Series      int64 `json:"series"`
	Episodes    int64 `json:"episodes"`
	Categories  int64 `json:"categories"`
	Variants    int64 `json:"variants"`
	Unavailable int64 `json:"unavailable_variants"`
	Unresolved  int64 `json:"unresolved_items"`
	Sources     int64 `json:"sources"`
	SourcesOK   int64 `json:"sources_ok"`
}

// GetCatalogStats devolve os contadores do catálogo.
func (s *Store) GetCatalogStats(ctx context.Context) (*CatalogStats, error) {
	var st CatalogStats
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM contents WHERE type = 'movie'  AND status <> 'deleted'),
			(SELECT count(*) FROM contents WHERE type = 'series' AND status <> 'deleted'),
			(SELECT count(*) FROM episodes WHERE status <> 'deleted'),
			(SELECT count(*) FROM categories),
			(SELECT count(*) FROM source_variants),
			(SELECT count(*) FROM source_variants WHERE NOT available),
			(SELECT count(*) FROM unresolved_items WHERE resolved_at IS NULL),
			(SELECT count(*) FROM sources),
			(SELECT count(*) FROM sources WHERE status = 'ok')`).Scan(
		&st.Movies, &st.Series, &st.Episodes, &st.Categories, &st.Variants,
		&st.Unavailable, &st.Unresolved, &st.Sources, &st.SourcesOK)
	if err != nil {
		return nil, wrapErr("consultando estatísticas", err)
	}
	return &st, nil
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// TamanhoBanco devolve quanto o banco ocupa em disco.
//
// É o número que responde "quanto do meu disco é o VOD Manager": o resto do consumo é do
// sistema operacional e de outros serviços, e confundir os dois leva a trocar de plano
// pelo motivo errado.
func (s *Store) TamanhoBanco(ctx context.Context) (uint64, error) {
	var tamanho int64
	err := s.pool.QueryRow(ctx,
		`SELECT pg_database_size(current_database())`).Scan(&tamanho)
	if err != nil {
		return 0, wrapErr("consultando tamanho do banco", err)
	}
	return uint64(tamanho), nil
}
