// Package ingest define o contrato de ingestão de catálogo: o que uma fonte emite
// (RawItem), o que o sistema entende (NormalizedItem) e as regras puras que traduzem
// um no outro.
//
// Tudo neste pacote é função pura sobre dados em memória. Não há HTTP, não há banco,
// não há disco, e nenhuma URL de vídeo é aberta em nenhuma circunstância — essa é uma
// garantia estrutural da Fase 2, verificada por teste.
//
// Ver docs/07-contrato-normalizado.md.
package ingest

import (
	"encoding/json"
	"time"
)

// RawKind é o que a FONTE diz que o item é. Pode estar errado ou ausente; a decisão
// final sobre o tipo é da normalização.
type RawKind string

const (
	RawKindMovie   RawKind = "movie"
	RawKindSeries  RawKind = "series"
	RawKindEpisode RawKind = "episode"
	RawKindUnknown RawKind = "unknown"
)

// RawItem é fiel ao que a fonte enviou. Nenhuma interpretação, nenhuma limpeza.
//
// Existe para que, quando uma fonte se comportar de forma inesperada, seja possível
// provar o que ela realmente mandou — sem re-consultar a fonte.
type RawItem struct {
	// --- Identidade na fonte ---
	Kind        RawKind
	ExternalID  string
	SeriesExtID string
	SeasonNum   *int
	EpisodeNum  *int

	// --- Como a fonte descreve ---
	Title string
	// TitleAlt é o outro título que a fonte forneceu para o MESMO item, quando existem
	// dois (no M3U: tvg-name e o nome de exibição). Um costuma ser mais limpo e o outro
	// mais completo — frequentemente só um deles traz o ano. Guardar ambos evita
	// descartar informação que a fonte de fato enviou.
	TitleAlt string
	// SeriesTitle é o nome da série, quando a fonte o fornece em campo separado do
	// título do episódio. Sem ele, a normalização teria que extrair o nome da série do
	// título do episódio — o que só funciona quando os dois vêm concatenados.
	SeriesTitle string
	GroupTitle  string
	CategoryIDs []string

	// --- Onde estão os bytes ---
	// Exatamente um dos dois é preenchido para movie/episode.
	// Nenhum dos dois é jamais requisitado durante a sincronização.
	StreamURL string     // URL direta (típico de M3U)
	StreamRef *StreamRef // referência a resolver depois (típico de Xtream)

	ContainerExt string

	// --- Metadados que a fonte oferece ---
	TMDBID          string
	IMDBID          string
	Year            *int // SÓ quando vem em campo próprio; ano tirado do título é da normalização
	PosterURL       string
	BackdropURL     string
	Plot            string
	Rating          *float64
	DurationSeconds *int

	// --- Rastreabilidade ---
	Attrs   map[string]string
	Payload json.RawMessage
	Origin  RawOrigin
}

// StreamRef descreve como construir a URL de mídia sem que o parser conheça credenciais.
//
// Esta indireção é deliberada: os parsers são puros e nunca veem usuário/senha da fonte.
// A materialização da URL acontece na camada de transporte, no momento de persistir
// `source_variants.origin_url` — e só lá.
type StreamRef struct {
	Kind      StreamRefKind
	ID        string // stream_id (filme) ou id do episódio
	Extension string
}

// StreamRefKind identifica o formato de URL a construir.
type StreamRefKind string

const (
	StreamRefXtreamMovie  StreamRefKind = "xtream_movie"
	StreamRefXtreamSeries StreamRefKind = "xtream_series"
)

// RawOrigin registra qual requisição ou linha gerou o item, para diagnóstico.
// Nunca contém URL de mídia nem credencial.
type RawOrigin struct {
	Provider  string // "m3u" | "xtream"
	Endpoint  string // ação da API ou nome do recurso; sem host, sem credencial
	Line      int    // linha do M3U, quando aplicável
	FetchedAt time.Time
}

// HasMedia informa se o item aponta para bytes de mídia de alguma forma.
func (r RawItem) HasMedia() bool {
	return r.StreamURL != "" || r.StreamRef != nil
}

// ItemKind é o tipo DECIDIDO pela normalização.
type ItemKind string

const (
	// ItemKindMovie é um filme com título utilizável.
	ItemKindMovie ItemKind = "movie"
	// ItemKindEpisode é um episódio com série, temporada E episódio conhecidos.
	ItemKindEpisode ItemKind = "episode"
	// ItemKindUnresolved é um item que não pôde ser classificado com segurança.
	//
	// Decisão aprovada (docs/07 §4.3): um item de série sem temporada/episódio conhecidos
	// NUNCA vira filme por descarte. Ele fica aqui, visível para revisão manual.
	ItemKindUnresolved ItemKind = "unresolved"
)

// RejectReason é o motivo pelo qual um item ficou unresolved.
type RejectReason string

const (
	RejectSemTitulo                RejectReason = "sem_titulo"
	RejectSemMidia                 RejectReason = "sem_midia"
	RejectNaoEVOD                  RejectReason = "nao_e_vod"
	RejectTipoIndeterminado        RejectReason = "tipo_indeterminado"
	RejectCategoriaFiltrada        RejectReason = "categoria_filtrada"
	RejectTemporadaEpisodioAusente RejectReason = "temporada_episodio_ausente"
	RejectURLInvalida              RejectReason = "url_invalida"
)

// Rejection explica por que um item não virou filme nem episódio.
//
// Detail é legível por humanos e NUNCA contém URL de mídia nem credencial.
type Rejection struct {
	Reason RejectReason `json:"reason"`
	Detail string       `json:"detail"`
}
