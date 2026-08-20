package ingest

import "encoding/json"

// NormalizedItem é o que o resto do sistema conhece. Nenhuma camada acima da ingestão
// olha para RawItem, exceto para auditoria.
type NormalizedItem struct {
	Kind      ItemKind        `json:"kind"`
	KindProv  Provenance      `json:"kind_provenance"`
	Variant   VariantKey      `json:"variant"`
	Movie     *MovieFields    `json:"movie,omitempty"`
	Episode   *EpisodeFields  `json:"episode,omitempty"`
	Category  CategoryRef     `json:"category"`
	Media     MediaFields     `json:"media"`
	Signals   MatchSignals    `json:"signals"`
	Rejection *Rejection      `json:"rejection,omitempty"`
	Payload   json.RawMessage `json:"-"` // sanitizado; nunca serializado em resposta pública
	Digest    string          `json:"digest"`
}

// VariantKey é a identidade estável de uma origem.
//
// Decisão aprovada (docs/07 §4.1): ExternalID tem PRECEDÊNCIA sobre URLHash. Fontes
// trocam a URL do mesmo item com frequência (rotação de CDN, token no path); identificar
// pela URL faria cada rotação criar uma variante nova e inflar o catálogo.
type VariantKey struct {
	SourceID   int64      `json:"source_id"`
	ExternalID string     `json:"external_id,omitempty"`
	URLHash    string     `json:"url_hash,omitempty"`
	Prov       Provenance `json:"provenance"`
}

// Stable informa se a identidade veio de um id da fonte (estável) ou de hash de URL
// (frágil a rotação de token).
func (k VariantKey) Stable() bool { return k.ExternalID != "" }

// String devolve a chave canônica para deduplicação em memória.
func (k VariantKey) String() string {
	if k.ExternalID != "" {
		return "id:" + k.ExternalID
	}
	return "url:" + k.URLHash
}

// TitleFields carrega as três formas de um título mais a procedência.
//
// Declared NUNCA é sobrescrito: é o que o administrador vê quando pergunta "o que essa
// fonte realmente mandou?".
type TitleFields struct {
	Declared   string     `json:"declared"`
	Display    string     `json:"display"`
	Normalized string     `json:"normalized"`
	Prov       Provenance `json:"provenance"`
}

// YearField é um ano com procedência: campo próprio da fonte vale mais que ano
// adivinhado do título, e o matching da Fase 3 usa essa diferença.
type YearField struct {
	Value *int       `json:"value"`
	Prov  Provenance `json:"provenance"`
}

// MovieFields são os campos de um filme normalizado.
type MovieFields struct {
	Title TitleFields `json:"title"`
	Year  YearField   `json:"year"`
}

// EpisodeFields são os campos de um episódio normalizado.
//
// Season e Episode são sempre conhecidos aqui — se não fossem, o item seria unresolved.
type EpisodeFields struct {
	SeriesTitle  TitleFields `json:"series_title"`
	SeriesYear   YearField   `json:"series_year"`
	Season       int         `json:"season"`
	Episode      int         `json:"episode"`
	NumberProv   Provenance  `json:"number_provenance"`
	EpisodeTitle TitleFields `json:"episode_title"`
}

// CategoryRef é a categoria como a fonte a declara, mais sua forma normalizada.
type CategoryRef struct {
	SourceCategoryID string     `json:"source_category_id,omitempty"`
	DeclaredName     string     `json:"declared_name"`
	NormalizedName   string     `json:"normalized_name"`
	ContentType      string     `json:"content_type"` // "movie" | "series" | "unknown"
	Prov             Provenance `json:"provenance"`
}

// MediaFields descreve onde os bytes estão, sem nunca tocá-los.
type MediaFields struct {
	// OriginURL só é preenchida quando a fonte entrega URL direta (M3U).
	// Para Xtream fica vazia e StreamRef é usada — a URL final é materializada pela
	// camada de transporte, que é a única que conhece as credenciais.
	OriginURL string     `json:"-"`
	StreamRef *StreamRef `json:"stream_ref,omitempty"`

	// ContainerExt é DECLARATIVO. Não é verificado, não é confirmado por requisição e
	// não é inferido do conteúdo do arquivo. É uma dica para o edge na Fase 6.
	ContainerExt    string     `json:"container_ext,omitempty"`
	ContainerProv   Provenance `json:"container_provenance"`
	PosterURL       string     `json:"poster_url,omitempty"`
	BackdropURL     string     `json:"backdrop_url,omitempty"`
	Plot            string     `json:"plot,omitempty"`
	Rating          *float64   `json:"rating,omitempty"`
	DurationSeconds *int       `json:"duration_seconds,omitempty"`
}

// MatchSignals são os insumos que a Fase 3 usa para agrupar conteúdo.
//
// Decisão aprovada (docs/07 §4.6): tags de qualidade e idioma SAEM do título normalizado
// mas são PRESERVADAS aqui. Elas não determinam a identidade do conteúdo — dois
// "Interestelar" em 1080p e 720p são o mesmo filme — mas o administrador precisa vê-las
// para escolher a fonte principal.
type MatchSignals struct {
	TMDBID       string     `json:"tmdb_id,omitempty"`
	TMDBProv     Provenance `json:"tmdb_provenance"`
	IMDBID       string     `json:"imdb_id,omitempty"`
	IMDBProv     Provenance `json:"imdb_provenance"`
	QualityTags  []string   `json:"quality_tags"`
	LanguageTags []string   `json:"language_tags"`
	TagsProv     Provenance `json:"tags_provenance"`
}

// IsUnresolved informa se o item precisa de revisão manual.
func (n NormalizedItem) IsUnresolved() bool { return n.Kind == ItemKindUnresolved }

// PrimaryTitle devolve o título normalizado principal do item, qualquer que seja o tipo.
func (n NormalizedItem) PrimaryTitle() string {
	switch {
	case n.Movie != nil:
		return n.Movie.Title.Normalized
	case n.Episode != nil:
		return n.Episode.SeriesTitle.Normalized
	default:
		return ""
	}
}

// PrimaryYear devolve o ano principal do item, se houver.
func (n NormalizedItem) PrimaryYear() *int {
	switch {
	case n.Movie != nil:
		return n.Movie.Year.Value
	case n.Episode != nil:
		return n.Episode.SeriesYear.Value
	default:
		return nil
	}
}
