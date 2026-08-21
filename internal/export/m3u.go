// Package export publica o catálogo para clientes externos: lista M3U e API Xtream.
//
// É a saída do sistema — o que transforma o catálogo unificado numa URL que o XC_VM, ou
// qualquer aplicativo de IPTV, consome inteira.
//
// Duas regras valem em tudo aqui:
//
//  1. Nenhum endereço de fonte sai. Todo link aponta para o nosso próprio servidor; quem
//     escolhe a variante é a camada de transporte, no momento em que o play acontece.
//  2. Nada é acumulado em memória. Uma lista de 270 mil itens é escrita enquanto as
//     linhas chegam do banco, direto no socket do cliente.
package export

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"vodmanager/internal/store"
)

// tamanhoBuffer é quanto acumulamos antes de escrever no socket.
//
// 64KB é o ponto em que a escrita para de ser dominada por chamada de sistema sem que a
// memória por cliente conectado passe a importar.
const tamanhoBuffer = 64 << 10

// EscritorM3U monta a lista no formato M3U estendido (o "m3u_plus" dos clientes Xtream).
type EscritorM3U struct {
	w    *bufio.Writer
	base string
	user string
	pass string
	// itens conta o que foi efetivamente escrito, para o registro de evento.
	itens int
}

// NovoEscritorM3U prepara a escrita e emite o cabeçalho.
func NovoEscritorM3U(dst io.Writer, base, usuario, senha string) *EscritorM3U {
	e := &EscritorM3U{
		w:    bufio.NewWriterSize(dst, tamanhoBuffer),
		base: strings.TrimRight(base, "/"),
		user: usuario,
		pass: senha,
	}
	e.w.WriteString("#EXTM3U\n")
	return e
}

// Filme escreve uma entrada de filme.
func (e *EscritorM3U) Filme(m store.ExportMovie) error {
	nome := m.Title
	if m.Year != nil {
		nome = fmt.Sprintf("%s (%d)", nome, *m.Year)
	}
	// Dublado e legendado são conteúdos separados com título idêntico: sem a marca, o
	// cliente vê dois itens iguais e não sabe qual escolher.
	nome += marcaDeIdioma(m.LanguageKey)
	return e.entrada(entradaM3U{
		id:        "movie." + strconv.FormatInt(m.ID, 10),
		nome:      nome,
		logo:      m.PosterURL,
		grupo:     grupoOu(m.CategoryName, "Filmes"),
		segmento:  "movie",
		streamID:  m.ID,
		extensao:  m.Extension,
		tipoGrupo: "VOD",
	})
}

// Episodio escreve uma entrada de episódio.
//
// O nome carrega série, temporada e episódio porque a lista M3U é plana: o cliente não
// tem estrutura de pastas, só o texto de cada linha para se orientar.
func (e *EscritorM3U) Episodio(ep store.ExportEpisode) error {
	// Mesma composição da API Xtream: os dois formatos precisam produzir o MESMO nome,
	// senão o mesmo episódio aparece diferente conforme o app do cliente.
	nome := nomeDeEpisodio(ep)
	logo := ep.PosterURL
	return e.entrada(entradaM3U{
		id:        "episode." + strconv.FormatInt(ep.ID, 10),
		nome:      nome,
		logo:      logo,
		grupo:     grupoOu(ep.CategoryName, "Séries"),
		segmento:  "series",
		streamID:  ep.ID,
		extensao:  ep.Extension,
		tipoGrupo: "Series",
	})
}

type entradaM3U struct {
	id        string
	nome      string
	logo      string
	grupo     string
	segmento  string
	streamID  int64
	extensao  string
	tipoGrupo string
}

func (e *EscritorM3U) entrada(it entradaM3U) error {
	nome := limpar(it.nome)
	ext := it.extensao
	if ext == "" {
		ext = "mp4"
	}

	// #EXTINF:-1 — duração desconhecida. Colocar a duração real não traria nada: o
	// cliente descobre pelo próprio arquivo, e um valor errado atrapalha o seek.
	_, err := fmt.Fprintf(e.w,
		"#EXTINF:-1 tvg-id=%q tvg-name=%q tvg-logo=%q group-title=%q,%s\n",
		it.id, nome, limpar(it.logo), limpar(it.grupo), nome)
	if err != nil {
		return err
	}

	// O link aponta para nós, sempre. A origem fica escondida atrás desta URL.
	_, err = fmt.Fprintf(e.w, "%s/%s/%s/%s/%d.%s\n",
		e.base, it.segmento, e.user, e.pass, it.streamID, ext)
	if err != nil {
		return err
	}
	e.itens++
	return nil
}

// Finalizar descarrega o que restou no buffer e devolve quantos itens saíram.
func (e *EscritorM3U) Finalizar() (int, error) {
	return e.itens, e.w.Flush()
}

// Itens devolve quantas entradas já foram escritas.
func (e *EscritorM3U) Itens() int { return e.itens }

// limpar remove o que quebraria o formato.
//
// Aspas encerrariam um atributo no meio, e quebras de linha encerrariam a própria
// entrada — um título com qualquer dos dois corromperia a lista inteira a partir dali.
func limpar(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		case '"':
			return '\''
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func grupoOu(nome, padrao string) string {
	if n := strings.TrimSpace(nome); n != "" {
		return n
	}
	return padrao
}
