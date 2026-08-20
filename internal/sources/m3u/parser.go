// Package m3u interpreta listas M3U/M3U-plus e emite RawItem.
//
// O parser é PURO e em streaming: recebe um io.Reader, emite item a item, e nunca abre
// nenhuma URL — as URLs são copiadas como texto. O consumo de memória independe do
// tamanho da lista.
package m3u

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"vodmanager/internal/ingest"
)

// maxLineBytes limita uma linha de #EXTINF. Listas reais têm linhas longas por causa dos
// atributos; 1 MiB é folgado e ainda protege contra arquivo malformado que não tem
// quebra de linha nenhuma.
const maxLineBytes = 1 << 20

// Stats resume o que o parser viu.
type Stats struct {
	Lines           int
	Items           int
	ExtinfSemURL    int
	URLSemExtinf    int
	LinhasIgnoradas int
	SemTitulo       int
}

// ParseOptions ajusta a interpretação.
type ParseOptions struct {
	// FetchedAt entra na procedência dos itens. Injetável para determinismo em teste.
	FetchedAt time.Time
}

// Parse lê a lista e chama emit para cada item encontrado.
//
// Um erro devolvido por emit interrompe a leitura e é propagado — é assim que o
// orquestrador aplica o teto de itens de uma run.
func Parse(r io.Reader, opts ParseOptions, emit func(ingest.RawItem) error) (Stats, error) {
	var stats Stats

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	var (
		pendente   *ingest.RawItem
		pendenteLn int
		grupoAtual string
	)

	for scanner.Scan() {
		stats.Lines++
		linha := strings.TrimSpace(scanner.Text())
		if linha == "" {
			continue
		}

		switch {
		case strings.HasPrefix(linha, "#EXTM3U"):
			continue

		case strings.HasPrefix(linha, "#EXTINF:"):
			if pendente != nil {
				// Dois #EXTINF seguidos: o primeiro ficou sem URL.
				stats.ExtinfSemURL++
			}
			item := parseExtinf(linha, stats.Lines, opts.FetchedAt)
			if grupoAtual != "" && item.GroupTitle == "" {
				item.GroupTitle = grupoAtual
			}
			pendente = &item
			pendenteLn = stats.Lines

		case strings.HasPrefix(linha, "#EXTGRP:"):
			// #EXTGRP aplica-se ao item seguinte (ou aos seguintes, conforme a lista).
			grupoAtual = strings.TrimSpace(strings.TrimPrefix(linha, "#EXTGRP:"))
			if pendente != nil && pendente.GroupTitle == "" {
				pendente.GroupTitle = grupoAtual
			}

		case strings.HasPrefix(linha, "#"):
			// #EXTVLCOPT, #KODIPROP, comentários: irrelevantes para o catálogo VOD.
			stats.LinhasIgnoradas++

		default:
			if pendente == nil {
				// URL solta, sem #EXTINF antes: não há título nem categoria a associar.
				stats.URLSemExtinf++
				continue
			}
			pendente.StreamURL = linha
			pendente.Origin.Line = pendenteLn
			if strings.TrimSpace(pendente.Title) == "" {
				stats.SemTitulo++
			}
			if err := emit(*pendente); err != nil {
				return stats, err
			}
			stats.Items++
			pendente = nil
		}
	}

	if pendente != nil {
		stats.ExtinfSemURL++
	}
	if err := scanner.Err(); err != nil {
		return stats, fmt.Errorf("m3u: lendo a lista: %w", err)
	}
	return stats, nil
}

// parseExtinf interpreta uma linha #EXTINF completa.
//
// Formato: #EXTINF:<duração> <chave="valor">... ,<nome de exibição>
func parseExtinf(linha string, numeroLinha int, fetchedAt time.Time) ingest.RawItem {
	corpo := strings.TrimPrefix(linha, "#EXTINF:")

	atributos, displayName := separarAtributosENome(corpo)
	attrs := parseAtributos(atributos)

	titulo, alternativo := escolherTitulo(attrs, displayName)

	item := ingest.RawItem{
		Kind:       ingest.RawKindUnknown,
		Title:      titulo,
		TitleAlt:   alternativo,
		GroupTitle: attrs["group-title"],
		ExternalID: strings.TrimSpace(attrs["tvg-id"]),
		Attrs:      attrs,
		Origin: ingest.RawOrigin{
			Provider:  "m3u",
			Endpoint:  "playlist",
			Line:      numeroLinha,
			FetchedAt: fetchedAt,
		},
	}

	if logo := attrs["tvg-logo"]; logo != "" {
		item.PosterURL = logo
	}
	// A duração vem antes do primeiro atributo. -1 significa "desconhecida".
	if d := duracao(atributos); d > 0 {
		segundos := d
		item.DurationSeconds = &segundos
	}

	// O payload guarda o que foi lido, não a URL — ela é adicionada só à coluna dedicada.
	payload := map[string]any{
		"extinf_attrs": attrs,
		"display_name": displayName,
		"line":         numeroLinha,
	}
	if raw, err := json.Marshal(payload); err == nil {
		item.Payload = raw
	}
	return item
}

// escolherTitulo prefere tvg-name ao nome de exibição, e devolve o outro como alternativo.
//
// tvg-name costuma ser mais limpo e estável; o nome de exibição é o que aparece no player
// e frequentemente carrega decoração ("★ FILME ★"). Mas nem sempre os dois carregam a
// mesma informação: é comum o tvg-name ser "O Poderoso Chefão" enquanto o nome de
// exibição é "O Poderoso Chefão [1972] [4K]". Devolver os dois permite à normalização
// aproveitar o ano sem abrir mão do título mais limpo.
func escolherTitulo(attrs map[string]string, displayName string) (titulo, alternativo string) {
	tvgName := strings.TrimSpace(attrs["tvg-name"])
	exibicao := strings.TrimSpace(displayName)

	if tvgName == "" {
		return exibicao, ""
	}
	if exibicao == "" || exibicao == tvgName {
		return tvgName, ""
	}
	return tvgName, exibicao
}

// separarAtributosENome divide o corpo do EXTINF na vírgula que fica FORA de aspas.
//
// Fazer isso com strings.LastIndex(",") quebraria em títulos que contêm vírgula, e
// com o primeiro "," quebraria em atributos com vírgula no valor.
func separarAtributosENome(corpo string) (atributos, nome string) {
	dentroDeAspas := false
	for i, r := range corpo {
		switch r {
		case '"':
			dentroDeAspas = !dentroDeAspas
		case ',':
			if !dentroDeAspas {
				return corpo[:i], corpo[i+1:]
			}
		}
	}
	return corpo, ""
}

// parseAtributos extrai os pares chave="valor".
func parseAtributos(s string) map[string]string {
	attrs := make(map[string]string)
	i := 0
	for i < len(s) {
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			break
		}
		eq += i

		// A chave é o token imediatamente antes do '=', delimitado por espaço.
		inicio := strings.LastIndexAny(s[:eq], " \t")
		chave := strings.TrimSpace(s[inicio+1 : eq])

		resto := s[eq+1:]
		if !strings.HasPrefix(resto, `"`) {
			// Valor sem aspas: vai até o próximo espaço.
			fim := strings.IndexAny(resto, " \t")
			if fim < 0 {
				fim = len(resto)
			}
			if chave != "" {
				attrs[strings.ToLower(chave)] = resto[:fim]
			}
			i = eq + 1 + fim
			continue
		}
		fechamento := strings.IndexByte(resto[1:], '"')
		if fechamento < 0 {
			break
		}
		if chave != "" {
			attrs[strings.ToLower(chave)] = resto[1 : 1+fechamento]
		}
		i = eq + 1 + fechamento + 2
	}
	return attrs
}

// duracao lê o número que abre o EXTINF.
func duracao(atributos string) int {
	campo := strings.TrimSpace(atributos)
	if idx := strings.IndexAny(campo, " \t"); idx >= 0 {
		campo = campo[:idx]
	}
	n, err := strconv.Atoi(campo)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
