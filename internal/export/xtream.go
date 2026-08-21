package export

import (
	"fmt"
	"strconv"
	"strings"

	"vodmanager/internal/store"
)

// Estruturas da API Xtream.
//
// O formato não é documentado por ninguém: é o que os clientes existentes esperam
// encontrar, descoberto por observação do comportamento deles. Duas consequências
// práticas guiaram as escolhas abaixo:
//
//   - Quase tudo é STRING, inclusive números. Clientes que recebem um inteiro onde
//     esperavam texto costumam ignorar o item em silêncio.
//   - Campos que não temos vão vazios, nunca ausentes. Chave faltando quebra cliente;
//     chave vazia não.
//
// Nada aqui é copiado de implementação alheia — são os nomes de campo que o protocolo
// exige, do mesmo modo que um cliente HTTP precisa escrever "Content-Type".

// CategoriaXtream é uma pasta no cliente.
type CategoriaXtream struct {
	CategoryID   string `json:"category_id"`
	CategoryName string `json:"category_name"`
	ParentID     int    `json:"parent_id"`
}

// FilmeXtream é um item da lista de filmes.
type FilmeXtream struct {
	Num                int     `json:"num"`
	Name               string  `json:"name"`
	StreamType         string  `json:"stream_type"`
	StreamID           int64   `json:"stream_id"`
	StreamIcon         string  `json:"stream_icon"`
	Rating             string  `json:"rating"`
	Rating5Based       float64 `json:"rating_5based"`
	Added              string  `json:"added"`
	CategoryID         string  `json:"category_id"`
	ContainerExtension string  `json:"container_extension"`
	CustomSID          string  `json:"custom_sid"`
	// DirectSource vazio é intencional: preenchido, ele diria ao cliente para buscar o
	// vídeo em outro endereço — exatamente o que não queremos, porque exporia a fonte.
	DirectSource string `json:"direct_source"`
}

// SerieXtream é um item da lista de séries.
type SerieXtream struct {
	Num             int      `json:"num"`
	Name            string   `json:"name"`
	SeriesID        int64    `json:"series_id"`
	Cover           string   `json:"cover"`
	Plot            string   `json:"plot"`
	Cast            string   `json:"cast"`
	Director        string   `json:"director"`
	Genre           string   `json:"genre"`
	ReleaseDate     string   `json:"releaseDate"`
	LastModified    string   `json:"last_modified"`
	Rating          string   `json:"rating"`
	Rating5Based    float64  `json:"rating_5based"`
	BackdropPath    []string `json:"backdrop_path"`
	YoutubeTrailer  string   `json:"youtube_trailer"`
	EpisodeRunTime  string   `json:"episode_run_time"`
	CategoryID      string   `json:"category_id"`
	SeriesNoEpisode int      `json:"episode_count"`
}

// EpisodioXtream é um episódio dentro de get_series_info.
type EpisodioXtream struct {
	ID                 string           `json:"id"`
	EpisodeNum         int              `json:"episode_num"`
	Title              string           `json:"title"`
	ContainerExtension string           `json:"container_extension"`
	Info               InfoEpisodioXtre `json:"info"`
	CustomSID          string           `json:"custom_sid"`
	Added              string           `json:"added"`
	Season             int              `json:"season"`
	DirectSource       string           `json:"direct_source"`
}

// InfoEpisodioXtre carrega os metadados do episódio.
type InfoEpisodioXtre struct {
	MovieImage   string `json:"movie_image"`
	Plot         string `json:"plot"`
	Duration     string `json:"duration"`
	DurationSecs int    `json:"duration_secs"`
}

// InfoSerieXtream é o bloco "info" de get_series_info.
type InfoSerieXtream struct {
	Name           string   `json:"name"`
	Cover          string   `json:"cover"`
	Plot           string   `json:"plot"`
	Cast           string   `json:"cast"`
	Director       string   `json:"director"`
	Genre          string   `json:"genre"`
	ReleaseDate    string   `json:"releaseDate"`
	LastModified   string   `json:"last_modified"`
	Rating         string   `json:"rating"`
	Rating5Based   float64  `json:"rating_5based"`
	BackdropPath   []string `json:"backdrop_path"`
	YoutubeTrailer string   `json:"youtube_trailer"`
	EpisodeRunTime string   `json:"episode_run_time"`
	CategoryID     string   `json:"category_id"`
}

// InfoFilmeXtream é o bloco "info" de get_vod_info.
type InfoFilmeXtream struct {
	MovieImage   string  `json:"movie_image"`
	Plot         string  `json:"plot"`
	Cast         string  `json:"cast"`
	Director     string  `json:"director"`
	Genre        string  `json:"genre"`
	ReleaseDate  string  `json:"releasedate"`
	Rating       string  `json:"rating"`
	Rating5Based float64 `json:"rating_5based"`
	Duration     string  `json:"duration"`
	DurationSecs int     `json:"duration_secs"`
	TMDBID       string  `json:"tmdb_id"`
}

// DadosFilmeXtream é o bloco "movie_data" de get_vod_info.
type DadosFilmeXtream struct {
	StreamID           int64  `json:"stream_id"`
	Name               string `json:"name"`
	Added              string `json:"added"`
	CategoryID         string `json:"category_id"`
	ContainerExtension string `json:"container_extension"`
	CustomSID          string `json:"custom_sid"`
	DirectSource       string `json:"direct_source"`
}

// UsuarioXtream é o bloco "user_info" do handshake.
type UsuarioXtream struct {
	Username             string   `json:"username"`
	Password             string   `json:"password"`
	Message              string   `json:"message"`
	Auth                 int      `json:"auth"`
	Status               string   `json:"status"`
	ExpDate              string   `json:"exp_date"`
	IsTrial              string   `json:"is_trial"`
	ActiveCons           string   `json:"active_cons"`
	CreatedAt            string   `json:"created_at"`
	MaxConnections       string   `json:"max_connections"`
	AllowedOutputFormats []string `json:"allowed_output_formats"`
}

// ServidorXtream é o bloco "server_info" do handshake.
type ServidorXtream struct {
	URL            string `json:"url"`
	Port           string `json:"port"`
	HTTPSPort      string `json:"https_port"`
	ServerProtocol string `json:"server_protocol"`
	RTMPPort       string `json:"rtmp_port"`
	Timezone       string `json:"timezone"`
	TimestampNow   int64  `json:"timestamp_now"`
	TimeNow        string `json:"time_now"`
	Process        bool   `json:"process"`
}

// --- Conversões --------------------------------------------------------------

func categoriasXtream(cats []store.ExportCategory) []CategoriaXtream {
	out := make([]CategoriaXtream, 0, len(cats))
	for _, c := range cats {
		out = append(out, CategoriaXtream{
			CategoryID:   strconv.FormatInt(c.ID, 10),
			CategoryName: c.Name,
		})
	}
	return out
}

func filmeXtream(m store.ExportMovie, num int) FilmeXtream {
	return FilmeXtream{
		Num:                num,
		Name:               nomeComAno(m.Title, m.Year) + marcaDeIdioma(m.LanguageKey),
		StreamType:         "movie",
		StreamID:           m.ID,
		StreamIcon:         m.PosterURL,
		Rating:             textoNota(m.Rating),
		Rating5Based:       nota5(m.Rating),
		Added:              strconv.FormatInt(m.AddedAt, 10),
		CategoryID:         strconv.FormatInt(m.CategoryID, 10),
		ContainerExtension: extensaoOu(m.Extension),
	}
}

func serieXtream(s store.ExportSeries, num int) SerieXtream {
	backdrops := []string{}
	if s.BackdropURL != "" {
		backdrops = append(backdrops, s.BackdropURL)
	}
	return SerieXtream{
		Num:             num,
		Name:            s.Title + marcaDeIdioma(s.LanguageKey),
		SeriesID:        s.ID,
		Cover:           s.PosterURL,
		Plot:            s.Plot,
		ReleaseDate:     anoTexto(s.Year),
		Rating:          textoNota(s.Rating),
		Rating5Based:    nota5(s.Rating),
		BackdropPath:    backdrops,
		CategoryID:      strconv.FormatInt(s.CategoryID, 10),
		SeriesNoEpisode: s.EpisodeCount,
	}
}

// nomeDeEpisodio monta o nome que o cliente vê.
//
// O padrão é sempre o mesmo, com ou sem título vindo da fonte:
//
//	Arquivo X (Legendado) S02E05 - O Retorno
//	Arquivo X S02E05
//
// Antes, um episódio sem título virava só "Episódio 5" — e num painel que importa os
// itens de forma plana, cinco séries diferentes viravam cinco "Episódio 5" indistinguíveis.
// A série e a numeração vêm primeiro porque são o que identifica; o título é o extra.
func nomeDeEpisodio(e store.ExportEpisode) string {
	nome := fmt.Sprintf("%s%s S%02dE%02d",
		e.SeriesTitle, marcaDeIdioma(e.LanguageKey), e.SeasonNumber, e.Number)
	if t := strings.TrimSpace(e.Title); t != "" && !tituloRedundante(t, e.Number) {
		nome += " - " + t
	}
	return nome
}

// tituloRedundante descarta títulos que só repetem a numeração.
//
// Fontes costumam preencher o título com "Episódio 5", "EP 5" ou "E05" quando não têm o
// nome real. Repetir isso depois de "S02E05" não acrescenta nada e só deixa o nome longo.
func tituloRedundante(titulo string, numero int) bool {
	limpo := strings.ToLower(strings.TrimSpace(titulo))
	limpo = strings.NewReplacer("º", "", "°", "", ".", "", "-", " ", ":", " ").Replace(limpo)
	limpo = strings.Join(strings.Fields(limpo), " ")

	n := strconv.Itoa(numero)
	for _, forma := range []string{
		"episodio " + n, "episódio " + n, "epis " + n,
		"ep " + n, "ep" + n, "e" + n, n,
		fmt.Sprintf("episodio %02d", numero), fmt.Sprintf("episódio %02d", numero),
		fmt.Sprintf("ep %02d", numero), fmt.Sprintf("e%02d", numero),
	} {
		if limpo == forma {
			return true
		}
	}
	return false
}

func episodioXtream(e store.ExportEpisode) EpisodioXtream {
	segundos := 0
	if e.Duration != nil {
		segundos = *e.Duration
	}
	return EpisodioXtream{
		ID:                 strconv.FormatInt(e.ID, 10),
		EpisodeNum:         e.Number,
		Title:              nomeDeEpisodio(e),
		ContainerExtension: extensaoOu(e.Extension),
		Season:             e.SeasonNumber,
		Added:              strconv.FormatInt(e.AddedAt, 10),
		Info: InfoEpisodioXtre{
			MovieImage:   e.PosterURL,
			Plot:         e.Plot,
			Duration:     duracaoTexto(segundos),
			DurationSecs: segundos,
		},
	}
}

// --- Formatação ---------------------------------------------------------------

func nomeComAno(titulo string, ano *int) string {
	if ano == nil {
		return titulo
	}
	return titulo + " (" + strconv.Itoa(*ano) + ")"
}

func anoTexto(ano *int) string {
	if ano == nil {
		return ""
	}
	return strconv.Itoa(*ano)
}

func textoNota(n *float64) string {
	if n == nil {
		return ""
	}
	return strconv.FormatFloat(*n, 'f', 1, 64)
}

// nota5 converte a nota de 0–10 para a escala de 5 estrelas que o cliente desenha.
func nota5(n *float64) float64 {
	if n == nil {
		return 0
	}
	v := *n / 2
	// Uma casa decimal basta: é o que cabe numa fileira de estrelas.
	return float64(int(v*10+0.5)) / 10
}

func duracaoTexto(segundos int) string {
	if segundos <= 0 {
		return "00:00:00"
	}
	h := segundos / 3600
	m := (segundos % 3600) / 60
	s := segundos % 60
	return pad2(h) + ":" + pad2(m) + ":" + pad2(s)
}

func pad2(v int) string {
	if v < 10 {
		return "0" + strconv.Itoa(v)
	}
	return strconv.Itoa(v)
}

func extensaoOu(ext string) string {
	if e := strings.TrimSpace(ext); e != "" {
		return e
	}
	return "mp4"
}

// marcaDeIdioma devolve o sufixo que distingue esta versão do conteúdo.
//
// Dublado e legendado do mesmo filme são conteúdos SEPARADOS, com título idêntico. Sem
// esta marca, o cliente vê dois itens iguais na lista e só descobre qual é qual abrindo —
// e num aplicativo de TV, onde a pasta nem sempre está visível, nem isso ajuda.
//
// Só marcamos o legendado. O dublado é o padrão do acervo e marcá-lo poluiria a lista
// inteira para distinguir mil e poucos itens.
func marcaDeIdioma(chave string) string {
	if chave == "" {
		return ""
	}
	for _, parte := range strings.Split(chave, "+") {
		if parte == "leg" {
			return " (Legendado)"
		}
	}
	return ""
}
