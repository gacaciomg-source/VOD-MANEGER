// Package xtream traduz as respostas de uma API compatível com Xtream em RawItem.
//
// Este pacote é PURO: ele recebe bytes de JSON já obtidos e devolve itens. Ele não faz
// requisição HTTP, não conhece host, e — deliberadamente — NÃO conhece usuário nem senha
// da fonte. As URLs de mídia não são montadas aqui: cada item leva um StreamRef, e a
// materialização da URL acontece na camada de transporte, que é a única que vê as
// credenciais (docs/07 §3).
package xtream

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"vodmanager/internal/ingest"
)

// Category é uma categoria de VOD ou de séries.
type Category struct {
	ID          string
	Name        string
	ContentType string // "movie" | "series"
}

type rawCategory struct {
	CategoryID   flexString `json:"category_id"`
	CategoryName flexString `json:"category_name"`
	ParentID     flexInt    `json:"parent_id"`
}

// ParseCategories interpreta get_vod_categories ou get_series_categories.
func ParseCategories(data []byte, contentType string) ([]Category, error) {
	var brutas []rawCategory
	if err := json.Unmarshal(data, &brutas); err != nil {
		return nil, fmt.Errorf("xtream: categorias inválidas: %w", err)
	}
	out := make([]Category, 0, len(brutas))
	for _, c := range brutas {
		if !c.CategoryID.Set || !c.CategoryName.Set {
			continue
		}
		out = append(out, Category{
			ID:          c.CategoryID.Value,
			Name:        c.CategoryName.Value,
			ContentType: contentType,
		})
	}
	return out, nil
}

// rawVODStream é uma entrada de get_vod_streams.
//
// Campos além destes existem e variam por painel: eles ficam no raw_payload como nível
// Vendor e NÃO podem virar dependência de lógica (docs/07 §2).
type rawVODStream struct {
	StreamID           flexString `json:"stream_id"`
	Name               flexString `json:"name"`
	Title              flexString `json:"title"`
	StreamType         flexString `json:"stream_type"`
	StreamIcon         flexString `json:"stream_icon"`
	Rating             flexFloat  `json:"rating"`
	CategoryID         flexString `json:"category_id"`
	CategoryName       flexString `json:"category_name"`
	ContainerExtension flexString `json:"container_extension"`
	Added              flexString `json:"added"`
	Year               flexInt    `json:"year"`
	ReleaseDate        flexString `json:"releaseDate"`
	TMDBID             flexString `json:"tmdb_id"`
	TMDB               flexString `json:"tmdb"`
	Plot               flexString `json:"plot"`
	EpisodeRunTime     flexInt    `json:"episode_run_time"`
}

// ParseVODStreams interpreta get_vod_streams e emite um RawItem por filme.
func ParseVODStreams(data []byte, categorias map[string]string, fetchedAt time.Time, emit func(ingest.RawItem) error) (int, error) {
	var brutos []json.RawMessage
	if err := json.Unmarshal(data, &brutos); err != nil {
		return 0, fmt.Errorf("xtream: get_vod_streams inválido: %w", err)
	}

	emitidos := 0
	for _, cru := range brutos {
		var s rawVODStream
		if err := json.Unmarshal(cru, &s); err != nil {
			// Item malformado é pulado, não derruba a run inteira.
			continue
		}
		if !s.StreamID.Set {
			continue
		}
		item := ingest.RawItem{
			Kind:            ingest.RawKindMovie,
			ExternalID:      s.StreamID.Value,
			Title:           primeiroNaoVazio(s.Name.Value, s.Title.Value),
			GroupTitle:      nomeCategoria(s, categorias),
			ContainerExt:    s.ContainerExtension.Value,
			PosterURL:       s.StreamIcon.Value,
			Plot:            s.Plot.Value,
			Rating:          s.Rating.Ptr(),
			DurationSeconds: segundosDeMinutos(s.EpisodeRunTime),
			TMDBID:          primeiroNaoVazio(s.TMDBID.Value, s.TMDB.Value),
			Payload:         cru,
			StreamRef: &ingest.StreamRef{
				Kind:      ingest.StreamRefXtreamMovie,
				ID:        s.StreamID.Value,
				Extension: s.ContainerExtension.Value,
			},
			Origin: ingest.RawOrigin{
				Provider:  "xtream",
				Endpoint:  "get_vod_streams",
				FetchedAt: fetchedAt,
			},
		}
		if s.CategoryID.Set {
			item.CategoryIDs = []string{s.CategoryID.Value}
		}
		if ano := anoDeStream(s); ano > 0 {
			item.Year = &ano
		}
		if err := emit(item); err != nil {
			return emitidos, err
		}
		emitidos++
	}
	return emitidos, nil
}

// Series é uma série listada em get_series.
type Series struct {
	ID           string
	Name         string
	CategoryID   string
	CategoryName string
	Cover        string
	Plot         string
	Year         int
	Rating       *float64
	// LastModified é o campo que, quando presente, permite o incremental barato de
	// get_series_info. Ainda NÃO é usado em lógica: é nível Vendor até que as amostras
	// reais confirmem que as suas fontes o fornecem (docs/07 §8, pendência 2).
	LastModified string
	Payload      json.RawMessage
	// Digest identifica a entrada desta série na listagem. Mudou o digest, vale a pena
	// buscar get_series_info; não mudou, não vale — é o que evita milhares de
	// requisições por sincronização (docs/07 §6.1).
	Digest string
}

type rawSeries struct {
	SeriesID     flexString `json:"series_id"`
	Name         flexString `json:"name"`
	Title        flexString `json:"title"`
	CategoryID   flexString `json:"category_id"`
	Cover        flexString `json:"cover"`
	Plot         flexString `json:"plot"`
	ReleaseDate  flexString `json:"releaseDate"`
	Year         flexInt    `json:"year"`
	Rating       flexFloat  `json:"rating"`
	LastModified flexString `json:"last_modified"`
}

// ParseSeriesList interpreta get_series.
func ParseSeriesList(data []byte, categorias map[string]string) ([]Series, error) {
	var brutos []json.RawMessage
	if err := json.Unmarshal(data, &brutos); err != nil {
		return nil, fmt.Errorf("xtream: get_series inválido: %w", err)
	}
	out := make([]Series, 0, len(brutos))
	for _, cru := range brutos {
		var s rawSeries
		if err := json.Unmarshal(cru, &s); err != nil {
			continue
		}
		if !s.SeriesID.Set {
			continue
		}
		serie := Series{
			ID:           s.SeriesID.Value,
			Name:         primeiroNaoVazio(s.Name.Value, s.Title.Value),
			CategoryID:   s.CategoryID.Value,
			CategoryName: categorias[s.CategoryID.Value],
			Cover:        s.Cover.Value,
			Plot:         s.Plot.Value,
			Rating:       s.Rating.Ptr(),
			LastModified: s.LastModified.Value,
			Payload:      cru,
		}
		if s.Year.Set {
			serie.Year = s.Year.Value
		} else if ano := anoDeData(s.ReleaseDate.Value); ano > 0 {
			serie.Year = ano
		}
		serie.Digest = ingest.DigestBytes(cru)
		out = append(out, serie)
	}
	return out, nil
}

type rawSeriesInfo struct {
	Info rawSeriesInfoInfo `json:"info"`
	// Mantido como RawMessage para que o payload persistido seja o JSON ORIGINAL do
	// episódio, e não a nossa struct reserializada — que perderia os campos Vendor.
	Episodes map[string][]json.RawMessage `json:"episodes"`
}

type rawSeriesInfoInfo struct {
	Name        flexString `json:"name"`
	Plot        flexString `json:"plot"`
	Cover       flexString `json:"cover"`
	ReleaseDate flexString `json:"releaseDate"`
	Year        flexInt    `json:"year"`
	Rating      flexFloat  `json:"rating"`
}

type rawEpisode struct {
	ID                 flexString     `json:"id"`
	EpisodeNum         flexInt        `json:"episode_num"`
	Season             flexInt        `json:"season"`
	Title              flexString     `json:"title"`
	ContainerExtension flexString     `json:"container_extension"`
	Info               rawEpisodeInfo `json:"info"`
}

type rawEpisodeInfo struct {
	Plot         flexString `json:"plot"`
	Duration     flexString `json:"duration"`
	DurationSecs flexInt    `json:"duration_secs"`
	MovieImage   flexString `json:"movie_image"`
	Rating       flexFloat  `json:"rating"`
	TMDBID       flexString `json:"tmdb_id"`
	ReleaseDate  flexString `json:"releaseDate"`
}

// ParseSeriesInfo interpreta get_series_info de UMA série e emite um RawItem por episódio.
//
// `serie` é a entrada correspondente de get_series: o nome da série vem dela quando o
// info não traz, e o series_id é necessário para vincular os episódios.
func ParseSeriesInfo(data []byte, serie Series, fetchedAt time.Time, emit func(ingest.RawItem) error) (int, error) {
	var info rawSeriesInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return 0, fmt.Errorf("xtream: get_series_info da série %s inválido: %w", serie.ID, err)
	}

	nomeSerie := primeiroNaoVazio(serie.Name, info.Info.Name.Value)
	anoSerie := serie.Year
	if anoSerie == 0 {
		if info.Info.Year.Set {
			anoSerie = info.Info.Year.Value
		} else {
			anoSerie = anoDeData(info.Info.ReleaseDate.Value)
		}
	}

	// A ordem das temporadas precisa ser determinística: o mapa do JSON não é ordenado,
	// e uma ordem instável faria o digest da run oscilar sem que nada tivesse mudado.
	temporadas := chavesOrdenadasNumericamente(info.Episodes)

	emitidos := 0
	for _, chave := range temporadas {
		for _, cru := range info.Episodes[chave] {
			var ep rawEpisode
			if err := json.Unmarshal(cru, &ep); err != nil {
				continue // episódio malformado é pulado, não derruba a série inteira
			}
			if !ep.ID.Set {
				continue
			}
			numTemporada := ep.Season.Ptr()
			if numTemporada == nil {
				if n, err := strconv.Atoi(chave); err == nil {
					numTemporada = &n
				}
			}
			numEpisodio := ep.EpisodeNum.Ptr()

			titulo := ep.Title.Value // pode ser vazio: muitas fontes não nomeiam episódios

			item := ingest.RawItem{
				Kind:         ingest.RawKindEpisode,
				ExternalID:   ep.ID.Value,
				SeriesExtID:  serie.ID,
				SeriesTitle:  nomeSerie,
				SeasonNum:    numTemporada,
				EpisodeNum:   numEpisodio,
				Title:        titulo,
				GroupTitle:   serie.CategoryName,
				ContainerExt: ep.ContainerExtension.Value,
				PosterURL:    primeiroNaoVazio(ep.Info.MovieImage.Value, serie.Cover),
				Plot:         primeiroNaoVazio(ep.Info.Plot.Value, serie.Plot),
				Rating:       ep.Info.Rating.Ptr(),
				TMDBID:       ep.Info.TMDBID.Value,
				StreamRef: &ingest.StreamRef{
					Kind:      ingest.StreamRefXtreamSeries,
					ID:        ep.ID.Value,
					Extension: ep.ContainerExtension.Value,
				},
				Origin: ingest.RawOrigin{
					Provider:  "xtream",
					Endpoint:  "get_series_info",
					FetchedAt: fetchedAt,
				},
			}
			if serie.CategoryID != "" {
				item.CategoryIDs = []string{serie.CategoryID}
			}
			if anoSerie > 0 {
				a := anoSerie
				item.Year = &a
			}
			if d := duracaoEmSegundos(ep.Info); d != nil {
				item.DurationSeconds = d
			}
			item.Payload = cru

			if err := emit(item); err != nil {
				return emitidos, err
			}
			emitidos++
		}
	}
	return emitidos, nil
}

// --- auxiliares --------------------------------------------------------------

func nomeCategoria(s rawVODStream, categorias map[string]string) string {
	if s.CategoryName.Set {
		return s.CategoryName.Value
	}
	return categorias[s.CategoryID.Value]
}

func anoDeStream(s rawVODStream) int {
	if s.Year.Set && s.Year.Value >= 1888 {
		return s.Year.Value
	}
	return anoDeData(s.ReleaseDate.Value)
}

func duracaoEmSegundos(info rawEpisodeInfo) *int {
	if info.DurationSecs.Set && info.DurationSecs.Value > 0 {
		v := info.DurationSecs.Value
		return &v
	}
	// "01:23:45"
	if d := info.Duration.Value; d != "" {
		var h, m, sec int
		if _, err := fmt.Sscanf(d, "%d:%d:%d", &h, &m, &sec); err == nil {
			total := h*3600 + m*60 + sec
			if total > 0 {
				return &total
			}
		}
	}
	return nil
}

func primeiroNaoVazio(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func chavesOrdenadasNumericamente(m map[string][]json.RawMessage) []string {
	chaves := make([]string, 0, len(m))
	for k := range m {
		chaves = append(chaves, k)
	}
	// Ordenação numérica quando possível, textual como desempate estável.
	for i := 1; i < len(chaves); i++ {
		for j := i; j > 0 && menorChave(chaves[j], chaves[j-1]); j-- {
			chaves[j], chaves[j-1] = chaves[j-1], chaves[j]
		}
	}
	return chaves
}

func menorChave(a, b string) bool {
	na, erra := strconv.Atoi(a)
	nb, errb := strconv.Atoi(b)
	if erra == nil && errb == nil {
		return na < nb
	}
	return a < b
}
