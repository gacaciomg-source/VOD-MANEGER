package store

import (
	"context"
)

// Consultas de EXPORTAÇÃO do catálogo: alimentam a lista M3U e a API Xtream.
//
// São diferentes das consultas de navegação do painel em dois pontos que importam:
//
//  1. Não paginam. O cliente pede o catálogo inteiro — 16 mil filmes, 254 mil episódios —
//     e a resposta é montada enquanto as linhas chegam do banco. Nada é acumulado em
//     memória, nem no banco nem aqui: por isso a entrega é por callback, e não por slice.
//  2. Só devolvem o que é REPRODUZÍVEL. Um conteúdo sem variante habilitada e disponível
//     não pode ir para a lista — o cliente veria um item que dá erro ao abrir.
//
// A URL de origem nunca aparece aqui. A exportação monta links para o nosso próprio
// endereço; quem resolve a variante é a camada de transporte, no momento do play.

// ExportMovie é um filme reproduzível, no formato que a exportação precisa.
type ExportMovie struct {
	ID    int64
	Title string
	// LanguageKey distingue versões do MESMO filme: dublado e legendado são conteúdos
	// separados com título idêntico. Sem levar isso para a saída, o cliente vê dois
	// itens iguais e não sabe qual é qual.
	LanguageKey  string
	Year         *int
	CategoryID   int64
	CategoryName string
	PosterURL    string
	Plot         string
	Rating       *float64
	Duration     *int
	Extension    string
	AddedAt      int64 // epoch, exigido pelo formato Xtream
}

// ExportSeries é uma série com pelo menos um episódio reproduzível.
type ExportSeries struct {
	ID           int64
	Title        string
	LanguageKey  string
	Year         *int
	CategoryID   int64
	CategoryName string
	PosterURL    string
	BackdropURL  string
	Plot         string
	Rating       *float64
	EpisodeCount int
	AddedAt      int64
}

// ExportEpisode é um episódio reproduzível, já com o contexto da série.
//
// A série vem junto porque a lista M3U é plana: cada linha precisa carregar o nome da
// série e a categoria, sem uma segunda consulta.
type ExportEpisode struct {
	ID           int64
	SeriesID     int64
	SeriesTitle  string
	LanguageKey  string
	CategoryID   int64
	CategoryName string
	SeasonNumber int
	Number       int
	Title        string
	Plot         string
	PosterURL    string
	Duration     *int
	Extension    string
	AddedAt      int64
}

// ExportCategory é uma categoria com conteúdo reproduzível.
type ExportCategory struct {
	ID    int64
	Name  string
	Type  string
	Count int64
}

// extensaoPadrao é o container assumido quando a fonte não declarou nenhum. Os clientes
// Xtream exigem uma extensão na URL; sem ela o link não é aceito.
const extensaoPadrao = "mp4"

// ExportCategories lista as categorias que têm ao menos um item reproduzível.
//
// Categorias vazias são omitidas de propósito: numa lista de milhares de itens, uma pasta
// que abre em nada é indistinguível de um defeito.
func (s *Store) ExportCategories(ctx context.Context, tipo string) ([]ExportCategory, error) {
	// A verificação de "tem conteúdo reproduzível" é feita em dois ramos separados, e não
	// numa condição OR.
	//
	// Com o OR, nenhum dos lados podia usar índice: o Postgres varria as 290 mil linhas de
	// source_variants uma vez por conteúdo, e com 25 mil conteúdos a consulta passava dos
	// 20 segundos — o tempo em que o XUI Managers desiste ao pedir as categorias.
	//
	// É o mesmo defeito que deixava a busca do painel em 8 segundos, no mesmo formato.
	rows, err := s.pool.Query(ctx, `
		SELECT cat.id, cat.name, cat.content_type, count(c.id)
		FROM categories cat
		JOIN contents c ON c.category_id = cat.id
		WHERE c.type = $1 AND c.status = 'active'
		  AND CASE WHEN c.type = 'series' THEN EXISTS (
		          SELECT 1
		          FROM seasons se
		          JOIN episodes e ON e.season_id = se.id AND e.status = 'active'
		          JOIN source_variants v
		            ON v.target_kind = 'episode' AND v.target_id = e.id
		           AND v.enabled AND v.available
		          WHERE se.series_content_id = c.id
		      ) ELSE EXISTS (
		          SELECT 1 FROM source_variants v
		          WHERE v.target_kind = 'content' AND v.target_id = c.id
		            AND v.enabled AND v.available
		      ) END
		GROUP BY cat.id, cat.name, cat.content_type
		ORDER BY cat.name`, tipo)
	if err != nil {
		return nil, wrapErr("exportando categorias", err)
	}
	defer rows.Close()

	out := []ExportCategory{}
	for rows.Next() {
		var c ExportCategory
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Count); err != nil {
			return nil, wrapErr("exportando categorias", err)
		}
		out = append(out, c)
	}
	return out, wrapErr("exportando categorias", rows.Err())
}

// EachExportMovie percorre os filmes reproduzíveis, um por vez.
//
// O callback recebe cada filme assim que ele chega do banco. Devolver erro interrompe a
// varredura — é o que acontece quando o cliente desconecta no meio do download da lista.
func (s *Store) EachExportMovie(ctx context.Context, categoryID *int64, fn func(ExportMovie) error) error {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.title, coalesce(c.language_key, ''), c.year, coalesce(c.category_id, 0), coalesce(cat.name, ''),
		       c.poster_url, c.plot, c.rating, c.duration_seconds,
		       coalesce(nullif(v.container_ext, ''), $2),
		       extract(epoch FROM c.created_at)::bigint
		FROM contents c
		LEFT JOIN categories cat ON cat.id = c.category_id
		JOIN LATERAL (
		    SELECT sv.container_ext
		    FROM source_variants sv
		    WHERE sv.target_kind = 'content' AND sv.target_id = c.id
		      AND sv.enabled AND sv.available
		    ORDER BY sv.id
		    LIMIT 1
		) v ON true
		WHERE c.type = 'movie' AND c.status = 'active'
		  AND ($1::bigint IS NULL OR c.category_id = $1)
		ORDER BY c.title, c.id`, categoryID, extensaoPadrao)
	if err != nil {
		return wrapErr("exportando filmes", err)
	}
	defer rows.Close()

	for rows.Next() {
		var m ExportMovie
		if err := rows.Scan(&m.ID, &m.Title, &m.LanguageKey, &m.Year, &m.CategoryID, &m.CategoryName,
			&m.PosterURL, &m.Plot, &m.Rating, &m.Duration, &m.Extension, &m.AddedAt); err != nil {
			return wrapErr("exportando filmes", err)
		}
		if err := fn(m); err != nil {
			return err
		}
	}
	return wrapErr("exportando filmes", rows.Err())
}

// EachExportSeries percorre as séries que têm episódio reproduzível.
func (s *Store) EachExportSeries(ctx context.Context, categoryID *int64, fn func(ExportSeries) error) error {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.title, coalesce(c.language_key, ''), c.year, coalesce(c.category_id, 0), coalesce(cat.name, ''),
		       c.poster_url, c.backdrop_url, c.plot, c.rating, v.total,
		       extract(epoch FROM c.created_at)::bigint
		FROM contents c
		LEFT JOIN categories cat ON cat.id = c.category_id
		JOIN LATERAL (
		    SELECT count(*)::int AS total
		    FROM episodes e
		    JOIN seasons se ON se.id = e.season_id
		    WHERE se.series_content_id = c.id AND e.status = 'active'
		      AND EXISTS (
		          SELECT 1 FROM source_variants sv
		          WHERE sv.target_kind = 'episode' AND sv.target_id = e.id
		            AND sv.enabled AND sv.available
		      )
		) v ON v.total > 0
		WHERE c.type = 'series' AND c.status = 'active'
		  AND ($1::bigint IS NULL OR c.category_id = $1)
		ORDER BY c.title, c.id`, categoryID)
	if err != nil {
		return wrapErr("exportando séries", err)
	}
	defer rows.Close()

	for rows.Next() {
		var e ExportSeries
		if err := rows.Scan(&e.ID, &e.Title, &e.LanguageKey, &e.Year, &e.CategoryID, &e.CategoryName,
			&e.PosterURL, &e.BackdropURL, &e.Plot, &e.Rating, &e.EpisodeCount, &e.AddedAt); err != nil {
			return wrapErr("exportando séries", err)
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return wrapErr("exportando séries", rows.Err())
}

// EachExportEpisode percorre os episódios reproduzíveis.
//
// seriesID nulo percorre o catálogo inteiro — é o caso da lista M3U, que pode passar de
// 250 mil linhas. Por isso a entrega é por callback e a ordenação é estável: série,
// temporada, episódio.
func (s *Store) EachExportEpisode(ctx context.Context, seriesID *int64, fn func(ExportEpisode) error) error {
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, c.id, c.title, coalesce(c.language_key, ''), coalesce(c.category_id, 0), coalesce(cat.name, ''),
		       se.season_number, e.episode_number, e.title, e.plot, e.poster_url,
		       e.duration_seconds, coalesce(nullif(v.container_ext, ''), $2),
		       extract(epoch FROM e.created_at)::bigint
		FROM episodes e
		JOIN seasons se ON se.id = e.season_id
		JOIN contents c ON c.id = se.series_content_id
		LEFT JOIN categories cat ON cat.id = c.category_id
		JOIN LATERAL (
		    SELECT sv.container_ext
		    FROM source_variants sv
		    WHERE sv.target_kind = 'episode' AND sv.target_id = e.id
		      AND sv.enabled AND sv.available
		    ORDER BY sv.id
		    LIMIT 1
		) v ON true
		WHERE e.status = 'active' AND c.status = 'active'
		  AND ($1::bigint IS NULL OR c.id = $1)
		ORDER BY c.title, c.id, se.season_number, e.episode_number`, seriesID, extensaoPadrao)
	if err != nil {
		return wrapErr("exportando episódios", err)
	}
	defer rows.Close()

	for rows.Next() {
		var e ExportEpisode
		if err := rows.Scan(&e.ID, &e.SeriesID, &e.SeriesTitle, &e.LanguageKey, &e.CategoryID, &e.CategoryName,
			&e.SeasonNumber, &e.Number, &e.Title, &e.Plot, &e.PosterURL,
			&e.Duration, &e.Extension, &e.AddedAt); err != nil {
			return wrapErr("exportando episódios", err)
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return wrapErr("exportando episódios", rows.Err())
}

// GetExportSeries busca uma série específica para o detalhamento da API Xtream.
func (s *Store) GetExportSeries(ctx context.Context, id int64) (*ExportSeries, error) {
	var e ExportSeries
	err := s.pool.QueryRow(ctx, `
		SELECT c.id, c.title, coalesce(c.language_key, ''), c.year, coalesce(c.category_id, 0), coalesce(cat.name, ''),
		       c.poster_url, c.backdrop_url, c.plot, c.rating,
		       extract(epoch FROM c.created_at)::bigint
		FROM contents c
		LEFT JOIN categories cat ON cat.id = c.category_id
		WHERE c.id = $1 AND c.type = 'series' AND c.status = 'active'`, id).
		Scan(&e.ID, &e.Title, &e.LanguageKey, &e.Year, &e.CategoryID, &e.CategoryName,
			&e.PosterURL, &e.BackdropURL, &e.Plot, &e.Rating, &e.AddedAt)
	if err != nil {
		return nil, wrapErr("buscando série para exportação", err)
	}
	return &e, nil
}

// GetExportMovie busca um filme específico para o detalhamento da API Xtream.
func (s *Store) GetExportMovie(ctx context.Context, id int64) (*ExportMovie, error) {
	var m ExportMovie
	err := s.pool.QueryRow(ctx, `
		SELECT c.id, c.title, coalesce(c.language_key, ''), c.year, coalesce(c.category_id, 0), coalesce(cat.name, ''),
		       c.poster_url, c.plot, c.rating, c.duration_seconds,
		       coalesce(nullif((
		           SELECT sv.container_ext FROM source_variants sv
		           WHERE sv.target_kind = 'content' AND sv.target_id = c.id
		             AND sv.enabled AND sv.available
		           ORDER BY sv.id LIMIT 1
		       ), ''), $2),
		       extract(epoch FROM c.created_at)::bigint
		FROM contents c
		LEFT JOIN categories cat ON cat.id = c.category_id
		WHERE c.id = $1 AND c.type = 'movie' AND c.status = 'active'`, id, extensaoPadrao).
		Scan(&m.ID, &m.Title, &m.LanguageKey, &m.Year, &m.CategoryID, &m.CategoryName,
			&m.PosterURL, &m.Plot, &m.Rating, &m.Duration, &m.Extension, &m.AddedAt)
	if err != nil {
		return nil, wrapErr("buscando filme para exportação", err)
	}
	return &m, nil
}
