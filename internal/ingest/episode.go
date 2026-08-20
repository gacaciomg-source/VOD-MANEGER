package ingest

import (
	"regexp"
	"strconv"
	"strings"
)

// EpisodeMatch é o resultado da detecção de temporada/episódio num título.
type EpisodeMatch struct {
	Season  int
	Episode int
	// Before e After são o que ficou antes e depois do trecho reconhecido: normalmente
	// o nome da série e o título do episódio.
	Before string
	After  string
	// Pattern identifica qual padrão casou, para diagnóstico e para o painel mostrar
	// por que o item foi interpretado daquela forma.
	Pattern string
}

// padraoEpisodio é um padrão reconhecido com o nome usado na procedência.
type padraoEpisodio struct {
	nome  string
	re    *regexp.Regexp
	temp  int // índice do grupo da temporada
	episo int // índice do grupo do episódio
}

// Padrões ordenados do mais específico para o mais genérico. A ordem importa: "S01E02"
// precisa ser testado antes de "1x02", senão "S01x02" casaria errado.
//
// Todos são case-insensitive e aceitam as grafias com e sem acento usadas em português.
var padroesEpisodio = []padraoEpisodio{
	{
		nome: "SxxExx",
		re:   regexp.MustCompile(`(?i)\bS\s?(\d{1,3})\s?[\s._-]?\s?E\s?P?\s?(\d{1,4})\b`),
		temp: 1, episo: 2,
	},
	{
		nome: "TxxExx",
		re:   regexp.MustCompile(`(?i)\bT\s?(\d{1,3})\s?[\s._-]?\s?E\s?P?\s?(\d{1,4})\b`),
		temp: 1, episo: 2,
	},
	{
		nome: "temporada-episodio",
		re: regexp.MustCompile(`(?i)\btemporada\s*(\d{1,3})\b[\s._|,-]*` +
			`(?:epis[oó]dio|episodio|ep)\s*(\d{1,4})\b`),
		temp: 1, episo: 2,
	},
	{
		nome: "numero-temporada-episodio",
		re: regexp.MustCompile(`(?i)\b(\d{1,3})\s*[ªa]?\s*temporada\b[\s._|,-]*` +
			`(?:epis[oó]dio|episodio|ep)\s*(\d{1,4})\b`),
		temp: 1, episo: 2,
	},
	{
		nome: "season-episode",
		re:   regexp.MustCompile(`(?i)\bseason\s*(\d{1,3})\b[\s._|,-]*episode\s*(\d{1,4})\b`),
		temp: 1, episo: 2,
	},
	{
		nome: "NxNN",
		re:   regexp.MustCompile(`(?i)(?:^|[\s._|,\[(-])(\d{1,3})\s?x\s?(\d{1,4})(?:$|[\s._|,\])-])`),
		temp: 1, episo: 2,
	},
}

// ParseSeasonEpisode procura temporada e episódio num título.
//
// Função pura. Devolve ok=false quando nenhum padrão casa — e nesse caso o chamador
// NÃO deve tratar o item como filme: a decisão aprovada (docs/07 §4.3) é mandá-lo para
// unresolved.
func ParseSeasonEpisode(title string) (EpisodeMatch, bool) {
	for _, p := range padroesEpisodio {
		loc := p.re.FindStringSubmatchIndex(title)
		if loc == nil {
			continue
		}
		season, err1 := strconv.Atoi(strings.TrimSpace(title[loc[2*p.temp]:loc[2*p.temp+1]]))
		episode, err2 := strconv.Atoi(strings.TrimSpace(title[loc[2*p.episo]:loc[2*p.episo+1]]))
		if err1 != nil || err2 != nil {
			continue
		}
		// Episódio 0 existe (especiais); temporada 0 também. Números negativos, não.
		if season < 0 || episode < 0 {
			continue
		}
		return EpisodeMatch{
			Season:  season,
			Episode: episode,
			Before:  limparBorda(title[:loc[0]]),
			After:   limparBorda(title[loc[1]:]),
			Pattern: p.nome,
		}, true
	}
	return EpisodeMatch{}, false
}

// indiciosSerie detectam conteúdo seriado quando a numeração está INCOMPLETA.
//
// Todos exigem um NÚMERO junto do marcador. A palavra sozinha não basta, e essa é a
// diferença entre acertar e destruir o catálogo: "Temporada de Caça", "Star Wars:
// Episódio II" e "Breakup Season" são filmes cujo nome contém a palavra. Antes desta
// regra, todos eles eram jogados na fila de não resolvidos.
var indiciosSerie = []*regexp.Regexp{
	// "Temporada 2", "2 Temporada", "3ª Temporada"
	regexp.MustCompile(`(?i)\btemporada\s*\d{1,3}\b`),
	regexp.MustCompile(`(?i)\b\d{1,3}\s*[ªa]?\s*temporada\b`),
	// "Season 2"
	regexp.MustCompile(`(?i)\bseason\s*\d{1,3}\b`),
	// "Episódio 12" em ALGARISMOS. Numeral romano fica de fora de propósito:
	// "Episódio II" é quase sempre nome de filme, como em Star Wars.
	regexp.MustCompile(`(?i)\bepis[oó]dio\s*\d{1,4}\b`),
	regexp.MustCompile(`(?i)\bep\s?\d{1,4}\b`),
	// "S01" / "T01" isolados, com o número COLADO à letra.
	//
	// Exigir o número colado é o que separa a marcação real ("Serie Y T03") de uma letra
	// que por acaso antecede um número: "Freddy's 2", "Porky's 2" e "Z-O-M-B-I-E-S 2"
	// casavam como "Season 2" quando o espaço era permitido. Listas de IPTV escrevem
	// S01/T01 colado; "S 01" praticamente não existe.
	regexp.MustCompile(`(?i)(?:^|[\s.\-_|\[(])[ST]\d{1,3}(?:$|[\s.\-_|\])])`),
}

// LooksLikeSeries informa se o título tem indício de conteúdo seriado.
//
// Um título com numeração completa reconhecida é série por definição; os demais indícios
// cobrem os casos incompletos e são deliberadamente conservadores — na dúvida, é filme,
// porque um catálogo VOD é majoritariamente de filmes e classificar errado tira o item
// do lugar certo.
func LooksLikeSeries(title string) bool {
	if _, ok := ParseSeasonEpisode(title); ok {
		return true
	}
	for _, re := range indiciosSerie {
		if re.MatchString(title) {
			return true
		}
	}
	return false
}

func limparBorda(s string) string {
	return strings.Trim(strings.TrimSpace(s), " -–—_.,|:")
}
