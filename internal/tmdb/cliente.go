// Package tmdb consulta o catálogo público do The Movie Database.
//
// # Para que ele existe aqui
//
// Uma fonte de IPTV entrega o filme e o nome do filme. Ela não entrega gênero — e quando
// entrega uma "categoria", ela é a pasta que AQUELA fonte escolheu, que muda de fonte para
// fonte e frequentemente é só "Filmes". O resultado é um acervo com milhares de títulos numa
// pasta só, impossível de navegar.
//
// O TMDB sabe o gênero de praticamente todo filme lançado. É informação catalogada por
// gente, e não deduzida do nome — e essa é a diferença entre organizar e adivinhar.
//
// # O que este pacote NÃO faz
//
// Não decide nada. Ele responde "quais os gêneros deste filme" e devolve o que o TMDB disse.
// A decisão de qual gênero vira qual pasta é do sistema, e fica fora daqui.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const base = "https://api.themoviedb.org/3"

// ErrSemChave: nenhuma chave de API foi configurada.
//
// Erro próprio porque o chamador reage diferente: não é falha de rede nem título não
// encontrado, é uma configuração que falta — e a tela precisa dizer isso, não "falhou".
var ErrSemChave = errors.New("nenhuma chave de API do TMDB configurada")

// ErrNaoEncontrado: o TMDB não conhece este título.
//
// Acontece o tempo todo e não é problema: conteúdo regional, coletânea, material que nunca
// entrou num catálogo internacional. Quem chama trata deixando o item onde está.
var ErrNaoEncontrado = errors.New("título não encontrado no TMDB")

// Cliente fala com a API do TMDB.
type Cliente struct {
	chave  string
	idioma string
	http   *http.Client

	// generos é o mapa id→nome, buscado uma vez.
	//
	// Ele é pequeno (algumas dezenas de linhas) e praticamente imutável — buscá-lo por filme
	// seriam milhares de requisições para receber sempre a mesma resposta.
	mu       sync.Mutex
	generos  map[int]string
	buscados bool
}

// Novo cria o cliente. Chave vazia devolve nulo: sem ela não há o que fazer.
func Novo(chave, idioma string) *Cliente {
	chave = strings.TrimSpace(chave)
	if chave == "" {
		return nil
	}
	if idioma == "" {
		idioma = "pt-BR"
	}
	return &Cliente{
		chave:  chave,
		idioma: idioma,
		http: &http.Client{
			// Prazo total curto, e aqui isto é correto: são requisições pequenas de
			// metadados, não transferências de vídeo. Uma que demora dez segundos está
			// travada, e insistir nela atrasa as milhares que vêm depois.
			Timeout: 10 * time.Second,
		},
	}
}

// Filme é o que interessa de um título, já reduzido.
type Filme struct {
	ID      int
	Titulo  string
	Ano     int
	Generos []string
}

// Generos devolve o mapa de id para nome, buscando na primeira chamada.
func (c *Cliente) Generos(ctx context.Context, serie bool) (map[int]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buscados {
		return c.generos, nil
	}

	// Os dois catálogos — filme e série — compartilham a maioria dos ids e diferem em
	// alguns. Juntar os dois num mapa só evita duas listas quase iguais, e um id que exista
	// só de um lado continua resolvendo.
	juntos := map[int]string{}
	for _, caminho := range []string{"/genre/movie/list", "/genre/tv/list"} {
		var resp struct {
			Genres []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"genres"`
		}
		if err := c.pedir(ctx, caminho, nil, &resp); err != nil {
			return nil, err
		}
		for _, g := range resp.Genres {
			juntos[g.ID] = g.Name
		}
	}

	c.generos, c.buscados = juntos, true
	return c.generos, nil
}

// PorID busca um título pelo id do TMDB, que é o caminho barato e exato.
func (c *Cliente) PorID(ctx context.Context, id string, serie bool) (Filme, error) {
	caminho := "/movie/" + url.PathEscape(id)
	if serie {
		caminho = "/tv/" + url.PathEscape(id)
	}

	var resp struct {
		ID     int `json:"id"`
		Genres []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"genres"`
		Title       string `json:"title"`
		Name        string `json:"name"`
		ReleaseDate string `json:"release_date"`
		FirstAir    string `json:"first_air_date"`
	}
	if err := c.pedir(ctx, caminho, nil, &resp); err != nil {
		return Filme{}, err
	}

	f := Filme{ID: resp.ID, Titulo: primeiro(resp.Title, resp.Name)}
	f.Ano = anoDe(primeiro(resp.ReleaseDate, resp.FirstAir))
	for _, g := range resp.Genres {
		f.Generos = append(f.Generos, g.Name)
	}
	return f, nil
}

// Buscar procura pelo título, e é o caminho caro e aproximado.
//
// Usado só quando não há id do TMDB. O ano entra na busca porque é o que separa uma refilmagem
// do original — dois filmes com o mesmo nome e gêneros diferentes.
func (c *Cliente) Buscar(ctx context.Context, titulo string, ano int, serie bool) (Filme, error) {
	caminho := "/search/movie"
	if serie {
		caminho = "/search/tv"
	}
	q := url.Values{"query": {titulo}}
	if ano > 0 {
		if serie {
			q.Set("first_air_date_year", fmt.Sprint(ano))
		} else {
			q.Set("year", fmt.Sprint(ano))
		}
	}

	var resp struct {
		Results []struct {
			ID          int    `json:"id"`
			Title       string `json:"title"`
			Name        string `json:"name"`
			GenreIDs    []int  `json:"genre_ids"`
			ReleaseDate string `json:"release_date"`
			FirstAir    string `json:"first_air_date"`
		} `json:"results"`
	}
	if err := c.pedir(ctx, caminho, q, &resp); err != nil {
		return Filme{}, err
	}
	if len(resp.Results) == 0 {
		return Filme{}, ErrNaoEncontrado
	}

	// O primeiro resultado, e só ele.
	//
	// O TMDB ordena por relevância, e a partir do segundo a chance de ser outro filme cresce
	// rápido. Numa operação que vai classificar milhares de títulos sem ninguém conferindo,
	// errar em silêncio é pior que não classificar: uma pasta com o filme errado dentro é
	// mais difícil de descobrir que uma pasta vazia.
	r := resp.Results[0]
	mapa, err := c.Generos(ctx, serie)
	if err != nil {
		return Filme{}, err
	}
	f := Filme{ID: r.ID, Titulo: primeiro(r.Title, r.Name)}
	f.Ano = anoDe(primeiro(r.ReleaseDate, r.FirstAir))
	for _, id := range r.GenreIDs {
		if nome, ok := mapa[id]; ok {
			f.Generos = append(f.Generos, nome)
		}
	}
	return f, nil
}

func (c *Cliente) pedir(ctx context.Context, caminho string, q url.Values, destino any) error {
	if c == nil || c.chave == "" {
		return ErrSemChave
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("api_key", c.chave)
	q.Set("language", c.idioma)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+caminho+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("consultando o TMDB: %w", err)
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ErrNaoEncontrado
	case http.StatusUnauthorized:
		// Distinguir chave inválida de tudo o mais: é o único caso em que insistir não
		// adianta, e o único que a pessoa resolve sozinha em trinta segundos.
		return fmt.Errorf("%w: o TMDB recusou a chave", ErrSemChave)
	default:
		return fmt.Errorf("o TMDB respondeu %s", resp.Status)
	}
	if err := json.Unmarshal(corpo, destino); err != nil {
		return fmt.Errorf("resposta inesperada do TMDB: %w", err)
	}
	return nil
}

func primeiro(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// anoDe extrai o ano de "2019-07-04". Zero quando não dá — e zero é uma resposta, não um erro.
func anoDe(data string) int {
	if len(data) < 4 {
		return 0
	}
	var ano int
	if _, err := fmt.Sscanf(data[:4], "%d", &ano); err != nil {
		return 0
	}
	return ano
}
