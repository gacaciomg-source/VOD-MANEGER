package store

import (
	"context"
	"strconv"
	"strings"
)

// Os índices deste arquivo são carregados UMA vez no início de uma sincronização e
// consultados em memória durante ela.
//
// Motivo: o caminho por item fazia seis idas ao banco (buscar variante, buscar
// candidatos por similaridade, garantir categoria, criar conteúdo, criar variante,
// gravar decisão). Com 60 mil itens isso vira centenas de milhares de round-trips
// sequenciais — o custo é dominado pela latência, não pelo trabalho.
//
// Carregar tudo de uma vez troca N consultas por 3, ao custo de alguns MB de memória.
// Um catálogo de 60 mil conteúdos ocupa por volta de 10 MB nestes mapas.

// VariantRef é o que precisamos saber de uma variante já existente para decidir se ela
// mudou — sem buscá-la no banco item a item.
type VariantRef struct {
	ID         int64
	TargetKind string
	TargetID   int64
	Digest     string
}

// VariantIndex mapeia a identidade de uma variante para os seus dados.
type VariantIndex map[string]VariantRef

// ChaveExterna monta a chave de um id externo.
func ChaveExterna(externalID string) string { return "id:" + externalID }

// ChaveURL monta a chave de um hash de URL.
func ChaveURL(urlHash string) string { return "url:" + urlHash }

// Lookup encontra uma variante pela identidade, respeitando a precedência do external_id.
func (idx VariantIndex) Lookup(externalID, urlHash string) (VariantRef, bool) {
	if externalID != "" {
		v, ok := idx[ChaveExterna(externalID)]
		return v, ok
	}
	if urlHash != "" {
		v, ok := idx[ChaveURL(urlHash)]
		return v, ok
	}
	return VariantRef{}, false
}

// LoadVariantIndex carrega todas as variantes de uma fonte.
func (s *Store) LoadVariantIndex(ctx context.Context, sourceID int64) (VariantIndex, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, external_id, url_hash, target_kind, target_id, digest
		 FROM source_variants WHERE source_id = $1`, sourceID)
	if err != nil {
		return nil, wrapErr("carregando índice de variantes", err)
	}
	defer rows.Close()

	idx := make(VariantIndex, 4096)
	for rows.Next() {
		var (
			ref                 VariantRef
			externalID, urlHash string
		)
		if err := rows.Scan(&ref.ID, &externalID, &urlHash, &ref.TargetKind, &ref.TargetID, &ref.Digest); err != nil {
			return nil, wrapErr("carregando índice de variantes", err)
		}
		if externalID != "" {
			idx[ChaveExterna(externalID)] = ref
		} else if urlHash != "" {
			idx[ChaveURL(urlHash)] = ref
		}
	}
	return idx, wrapErr("carregando índice de variantes", rows.Err())
}

// ContentIndex agrupa os conteúdos por título normalizado, para o matching resolver o
// caso comum sem consultar o banco.
type ContentIndex struct {
	// porTitulo: chave "tipo|titulo|idioma" → candidatos com esse título.
	porTitulo map[string][]ContentCandidate
	// porTMDB e porIMDB resolvem o caso mais forte direto.
	porTMDB map[string]ContentCandidate
	porIMDB map[string]ContentCandidate
}

func chaveTitulo(tipo, titulo, idioma string) string {
	return tipo + "|" + titulo + "|" + idioma
}

// LoadContentIndex carrega o catálogo inteiro num índice de memória.
func (s *Store) LoadContentIndex(ctx context.Context) (*ContentIndex, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, type, normalized_title, language_key, year, tmdb_id, imdb_id
		FROM contents WHERE status <> 'deleted'`)
	if err != nil {
		return nil, wrapErr("carregando índice de conteúdos", err)
	}
	defer rows.Close()

	idx := &ContentIndex{
		porTitulo: make(map[string][]ContentCandidate, 4096),
		porTMDB:   map[string]ContentCandidate{},
		porIMDB:   map[string]ContentCandidate{},
	}
	for rows.Next() {
		var c ContentCandidate
		if err := rows.Scan(&c.ID, &c.Type, &c.NormalizedTitle, &c.LanguageKey,
			&c.Year, &c.TMDBID, &c.IMDBID); err != nil {
			return nil, wrapErr("carregando índice de conteúdos", err)
		}
		idx.Add(c)
	}
	return idx, wrapErr("carregando índice de conteúdos", rows.Err())
}

// Add insere um conteúdo no índice. Chamado também ao criar conteúdo durante a
// sincronização, para que o próximo item já o encontre.
func (idx *ContentIndex) Add(c ContentCandidate) {
	k := chaveTitulo(c.Type, c.NormalizedTitle, c.LanguageKey)
	idx.porTitulo[k] = append(idx.porTitulo[k], c)
	if c.TMDBID != nil && *c.TMDBID != "" {
		idx.porTMDB[*c.TMDBID] = c
	}
	if c.IMDBID != nil && *c.IMDBID != "" {
		idx.porIMDB[*c.IMDBID] = c
	}
}

// Candidatos devolve os candidatos plausíveis para um item, sem tocar no banco.
//
// Cobre os casos que decidem a esmagadora maioria do catálogo: id externo igual, ou
// título normalizado idêntico na mesma versão de idioma. Quando não há nenhum, o
// chamador cai na busca por similaridade no banco — que é cara, mas rara.
func (idx *ContentIndex) Candidatos(tipo, titulo, idioma, tmdbID, imdbID string) []ContentCandidate {
	if tmdbID != "" {
		if c, ok := idx.porTMDB[tmdbID]; ok {
			return []ContentCandidate{c}
		}
	}
	if imdbID != "" {
		if c, ok := idx.porIMDB[imdbID]; ok {
			return []ContentCandidate{c}
		}
	}
	return idx.porTitulo[chaveTitulo(tipo, titulo, idioma)]
}

// SeriesIndex resolve séries e episódios sem consultar o banco a cada item.
type SeriesIndex struct {
	// series: "titulo normalizado" → id da série.
	series map[string]int64
	// temporadas: "serieID|numero" → id da temporada.
	temporadas map[string]int64
	// episodios: "temporadaID|numero" → id do episódio.
	episodios map[string]int64
}

// LoadSeriesIndex carrega séries, temporadas e episódios existentes.
func (s *Store) LoadSeriesIndex(ctx context.Context) (*SeriesIndex, error) {
	idx := &SeriesIndex{
		series:     map[string]int64{},
		temporadas: map[string]int64{},
		episodios:  map[string]int64{},
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, normalized_title FROM contents WHERE type = 'series' AND status <> 'deleted'`)
	if err != nil {
		return nil, wrapErr("carregando índice de séries", err)
	}
	for rows.Next() {
		var id int64
		var titulo string
		if err := rows.Scan(&id, &titulo); err != nil {
			rows.Close()
			return nil, wrapErr("carregando índice de séries", err)
		}
		if _, existe := idx.series[titulo]; !existe {
			idx.series[titulo] = id
		}
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT id, series_content_id, season_number FROM seasons`)
	if err != nil {
		return nil, wrapErr("carregando índice de temporadas", err)
	}
	for rows.Next() {
		var id, serieID int64
		var numero int
		if err := rows.Scan(&id, &serieID, &numero); err != nil {
			rows.Close()
			return nil, wrapErr("carregando índice de temporadas", err)
		}
		idx.temporadas[ChaveTemporada(serieID, numero)] = id
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT id, season_id, episode_number FROM episodes`)
	if err != nil {
		return nil, wrapErr("carregando índice de episódios", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, temporadaID int64
		var numero int
		if err := rows.Scan(&id, &temporadaID, &numero); err != nil {
			return nil, wrapErr("carregando índice de episódios", err)
		}
		idx.episodios[ChaveEpisodio(temporadaID, numero)] = id
	}
	return idx, wrapErr("carregando índice de episódios", rows.Err())
}

// ChaveTemporada monta a chave de uma temporada.
func ChaveTemporada(serieID int64, numero int) string {
	return strconv.FormatInt(serieID, 10) + "|" + strconv.Itoa(numero)
}

// ChaveEpisodio monta a chave de um episódio.
func ChaveEpisodio(temporadaID int64, numero int) string {
	return strconv.FormatInt(temporadaID, 10) + "|" + strconv.Itoa(numero)
}

// Serie devolve o id de uma série pelo título normalizado.
func (idx *SeriesIndex) Serie(titulo string) (int64, bool) {
	id, ok := idx.series[titulo]
	return id, ok
}

// AddSerie registra uma série recém-criada.
func (idx *SeriesIndex) AddSerie(titulo string, id int64) { idx.series[titulo] = id }

// Temporada devolve o id de uma temporada.
func (idx *SeriesIndex) Temporada(serieID int64, numero int) (int64, bool) {
	id, ok := idx.temporadas[ChaveTemporada(serieID, numero)]
	return id, ok
}

// AddTemporada registra uma temporada recém-criada.
func (idx *SeriesIndex) AddTemporada(serieID int64, numero int, id int64) {
	idx.temporadas[ChaveTemporada(serieID, numero)] = id
}

// Episodio devolve o id de um episódio.
func (idx *SeriesIndex) Episodio(temporadaID int64, numero int) (int64, bool) {
	id, ok := idx.episodios[ChaveEpisodio(temporadaID, numero)]
	return id, ok
}

// AddEpisodio registra um episódio recém-criado.
func (idx *SeriesIndex) AddEpisodio(temporadaID int64, numero int, id int64) {
	idx.episodios[ChaveEpisodio(temporadaID, numero)] = id
}

// CategoryIndex evita uma ida ao banco por item só para resolver a categoria.
type CategoryIndex map[string]int64

// LoadCategoryIndex carrega as categorias canônicas.
func (s *Store) LoadCategoryIndex(ctx context.Context) (CategoryIndex, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, normalized_name, content_type FROM categories`)
	if err != nil {
		return nil, wrapErr("carregando índice de categorias", err)
	}
	defer rows.Close()

	idx := CategoryIndex{}
	for rows.Next() {
		var id int64
		var nome, tipo string
		if err := rows.Scan(&id, &nome, &tipo); err != nil {
			return nil, wrapErr("carregando índice de categorias", err)
		}
		idx[chaveCategoria(nome, tipo)] = id
	}
	return idx, wrapErr("carregando índice de categorias", rows.Err())
}

func chaveCategoria(normalizado, tipo string) string {
	return strings.ToLower(normalizado) + "|" + tipo
}

// Get busca uma categoria no índice.
func (idx CategoryIndex) Get(normalizado, tipo string) (int64, bool) {
	id, ok := idx[chaveCategoria(normalizado, tipo)]
	return id, ok
}

// Set registra uma categoria recém-criada.
func (idx CategoryIndex) Set(normalizado, tipo string, id int64) {
	idx[chaveCategoria(normalizado, tipo)] = id
}
