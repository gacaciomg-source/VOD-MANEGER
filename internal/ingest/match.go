package ingest

import (
	"fmt"
	"sort"
	"strings"
)

// Limiares de decisão do matching (docs/03 §8).
const (
	// ScoreAuto e acima: agrupa automaticamente.
	ScoreAuto = 95
	// ScoreReview e acima (mas abaixo de ScoreAuto): vai para a fila de revisão manual.
	ScoreReview = 80
)

// Pesos dos sinais.
//
// Calibrados a partir dos casos canônicos, não escolhidos no abstrato. As âncoras:
//
//	título idêntico + ano idêntico     → 100  (agrupa)
//	título idêntico, nenhum tem ano    →  95  (agrupa)
//	título idêntico + ano com 1 de gap →  90  (revisão)
//	título idêntico + anos distantes   →  50  (não agrupa: é remake)
//
// Duas correções vieram de rodar o sistema com dados reais, e ambas estão registradas
// em docs/03 §8:
//
//  1. Os pesos originalmente propostos não alcançavam 95 nem no caso mais forte — o
//     "mesmo filme em duas fontes" cairia em revisão manual para sempre.
//  2. Com título idêntico valendo 55, o teto de um filme SEM ano era exatamente 80.
//     Como ausência de ano é neutra, nenhum filme sem ano podia agrupar — e em listas
//     M3U a maioria não traz ano. O catálogo enchia de duplicatas: "Toy Story" aparecia
//     uma vez por fonte. Título idêntico passou a valer 70, o bastante para agrupar
//     sozinho.
//
// Ano com 1 de diferença passou a contar como evidência LEVEMENTE CONTRÁRIA: se duas
// fontes discordam do ano, ou uma está errada ou são obras diferentes. Isso mantém esse
// caso em revisão manual mesmo com o título idêntico valendo mais.
const (
	pesoTMDBIgual          = 95 // sozinho já agrupa: id externo é o sinal mais confiável
	pesoIMDBIgual          = 90
	pesoIDDivergente       = -70 // dois ids conhecidos e diferentes: quase certamente obras distintas
	pesoTituloIdentico     = 70
	pesoSimilaridadeMax    = 25
	pesoAnoIdentico        = 20
	pesoAnoProximo         = -5
	pesoAnoDivergente      = -45 // separa remakes: "Duna 1984" e "Duna 2021"
	pesoTipoDivergente     = -60
	pesoEpisodioIdentico   = 20
	pesoEpisodioDivergente = -70
	// Forte o bastante para separar mesmo com título e ano idênticos: a versão dublada e
	// a legendada da mesma obra são entradas distintas no catálogo.
	pesoIdiomaDivergente = -60
)

func ouPadrao(chave string) string {
	if chave == "" {
		return "padrão"
	}
	return chave
}

// MatchDecision é o veredito do matching.
type MatchDecision string

const (
	// DecisionGrouped: confiança alta o bastante para agrupar sem perguntar.
	DecisionGrouped MatchDecision = "grouped"
	// DecisionPendingReview: plausível, mas exige confirmação do administrador.
	DecisionPendingReview MatchDecision = "pending_review"
	// DecisionRejected: não são o mesmo conteúdo.
	DecisionRejected MatchDecision = "rejected"
	// DecisionLocked: existe decisão manual travada; o algoritmo não opina.
	DecisionLocked MatchDecision = "locked"
)

// MatchResult explica o veredito. Os sinais individuais são preservados para que o
// painel possa mostrar POR QUE dois itens foram (ou não foram) agrupados.
type MatchResult struct {
	Score    int            `json:"score"`
	Decision MatchDecision  `json:"decision"`
	Signals  map[string]int `json:"signals"`
	Notes    []string       `json:"notes"`
}

// MatchCandidate é a forma reduzida de um item para comparação. Trabalhar sobre esta
// struct — e não sobre NormalizedItem — mantém o matching testável sem construir um
// item inteiro, e permite comparar contra um Content já persistido.
type MatchCandidate struct {
	Kind            ItemKind
	NormalizedTitle string
	Year            *int
	TMDBID          string
	IMDBID          string
	Season          *int
	Episode         *int
	// LanguageKey é a versão de áudio/legenda, na forma canônica. Diferente de qualidade,
	// ela DISTINGUE conteúdos: a versão dublada e a legendada do mesmo filme são entradas
	// separadas no catálogo, porque ficam em categorias diferentes e o espectador escolhe
	// uma delas.
	LanguageKey string
}

// CandidateFrom extrai o candidato de comparação a partir de um item normalizado.
func CandidateFrom(item NormalizedItem) MatchCandidate {
	c := MatchCandidate{
		Kind:            item.Kind,
		NormalizedTitle: item.PrimaryTitle(),
		Year:            item.PrimaryYear(),
		TMDBID:          item.Signals.TMDBID,
		IMDBID:          item.Signals.IMDBID,
		LanguageKey:     LanguageKey(item.Signals.LanguageTags),
	}
	if item.Episode != nil {
		s, e := item.Episode.Season, item.Episode.Episode
		c.Season, c.Episode = &s, &e
	}
	return c
}

// sinonimosIdioma reduz as muitas grafias a poucas famílias comparáveis.
//
// O critério NÃO é "a marcação é diferente?", e sim "o espectador consegue assistir do
// mesmo jeito?". Versões com áudio em português — sem marcação, dublado, nacional, dual —
// são intercambiáveis e devem ser AGRUPADAS: são a mesma obra vinda de fontes diferentes,
// e o administrador escolhe qual fonte usar.
//
// Só a versão legendada é uma escolha diferente para o espectador: ela não tem áudio em
// português e, nas listas, mora numa categoria própria. Por isso é a única que separa.
//
// Separar por qualquer diferença de marcação recriaria o problema das duplicatas: uma
// fonte que escreve "DUAL" e outra que não escreve nada dariam duas entradas para o mesmo
// filme.
var sinonimosIdioma = map[string]string{
	// Sem áudio em português: é uma versão distinta.
	"l": "leg", "leg": "leg", "legenda": "leg", "legendado": "leg", "legendada": "leg",
	"vose": "leg",

	// Com áudio em português: todas equivalem à versão padrão.
	"dub": "", "dubl": "", "dublado": "", "dublada": "", "dublagem": "",
	"nac": "", "nacional": "", "original": "",
	"dual": "", "dual audio": "", "dual-audio": "",
	"pt": "", "pt-br": "", "ptbr": "", "por": "",
}

// LanguageKey reduz as tags de idioma a uma chave canônica e comparável.
//
// Devolve string vazia para a versão padrão (com áudio em português). Marcações de outro
// idioma que não estejam no mapa — "eng", "lat" — entram na chave como estão, porque
// também representam uma escolha diferente para o espectador.
func LanguageKey(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	familias := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if fam, ok := sinonimosIdioma[t]; ok {
			if fam != "" {
				familias[fam] = true
			}
			continue
		}
		familias[t] = true
	}
	if len(familias) == 0 {
		return ""
	}
	saida := make([]string, 0, len(familias))
	for f := range familias {
		saida = append(saida, f)
	}
	sort.Strings(saida)
	return strings.Join(saida, "+")
}

// Score compara dois candidatos e devolve o veredito.
//
// Função pura, sem banco: a geração de candidatos por similaridade no Postgres (pg_trgm)
// é da camada de sincronização; aqui fica a decisão, que é o que precisa ser auditável
// e coberto por tabela de casos.
func Score(a, b MatchCandidate) MatchResult {
	res := MatchResult{Signals: map[string]int{}, Notes: []string{}}
	total := 0

	add := func(nome string, pontos int) {
		if pontos == 0 {
			return
		}
		res.Signals[nome] = pontos
		total += pontos
	}

	// Identificadores externos são o sinal mais forte que existe: um TMDB ID igual
	// sozinho já basta para agrupar, mesmo com títulos em idiomas diferentes.
	switch {
	case a.TMDBID != "" && a.TMDBID == b.TMDBID:
		add("tmdb_id_igual", pesoTMDBIgual)
	case a.TMDBID != "" && b.TMDBID != "" && a.TMDBID != b.TMDBID:
		add("tmdb_id_divergente", pesoIDDivergente)
		res.Notes = append(res.Notes, "os dois itens têm TMDB ID e eles são diferentes")
	}
	switch {
	case a.IMDBID != "" && a.IMDBID == b.IMDBID:
		add("imdb_id_igual", pesoIMDBIgual)
	case a.IMDBID != "" && b.IMDBID != "" && a.IMDBID != b.IMDBID:
		add("imdb_id_divergente", pesoIDDivergente)
		res.Notes = append(res.Notes, "os dois itens têm IMDb ID e eles são diferentes")
	}

	// Título.
	if a.NormalizedTitle != "" && a.NormalizedTitle == b.NormalizedTitle {
		add("titulo_identico", pesoTituloIdentico)
	}
	sim := TrigramSimilarity(a.NormalizedTitle, b.NormalizedTitle)
	add("similaridade_titulo", int(sim*pesoSimilaridadeMax))

	// Ano.
	switch {
	case a.Year != nil && b.Year != nil && *a.Year == *b.Year:
		add("ano_identico", pesoAnoIdentico)
	case a.Year != nil && b.Year != nil && abs(*a.Year-*b.Year) == 1:
		add("ano_proximo", pesoAnoProximo)
		res.Notes = append(res.Notes, "os anos diferem em 1 — comum entre lançamento e estreia local")
	case a.Year != nil && b.Year != nil:
		add("ano_divergente", pesoAnoDivergente)
	}

	// Tipo.
	if a.Kind != b.Kind {
		add("tipo_divergente", pesoTipoDivergente)
		res.Notes = append(res.Notes, "um item é filme e o outro é episódio")
	}

	// Versão de idioma. Diferente de qualidade, ela separa conteúdos: dublado e
	// legendado ficam em categorias distintas e o espectador escolhe uma delas.
	if a.LanguageKey != b.LanguageKey {
		add("idioma_divergente", pesoIdiomaDivergente)
		res.Notes = append(res.Notes, fmt.Sprintf(
			"versões de idioma diferentes (%q e %q)", ouPadrao(a.LanguageKey), ouPadrao(b.LanguageKey)))
	}

	// Temporada e episódio.
	if a.Kind == ItemKindEpisode && b.Kind == ItemKindEpisode {
		switch {
		case sameInt(a.Season, b.Season) && sameInt(a.Episode, b.Episode):
			add("temporada_episodio_identicos", pesoEpisodioIdentico)
		case a.Season != nil && b.Season != nil && a.Episode != nil && b.Episode != nil:
			add("temporada_episodio_divergentes", pesoEpisodioDivergente)
			res.Notes = append(res.Notes, "mesma série, mas temporada/episódio diferentes")
		}
	}

	res.Score = clamp(total, 0, 100)
	res.Decision = decide(res.Score)
	return res
}

// Decide traduz um score em veredito, respeitando decisão manual travada.
//
// Regra inviolável (docs/07 §7.5): quando existe decisão do administrador com
// locked = true, o algoritmo NÃO opina. Nem para confirmar, nem para desfazer.
func Decide(score int, locked bool) MatchDecision {
	if locked {
		return DecisionLocked
	}
	return decide(score)
}

func decide(score int) MatchDecision {
	switch {
	case score >= ScoreAuto:
		return DecisionGrouped
	case score >= ScoreReview:
		return DecisionPendingReview
	default:
		return DecisionRejected
	}
}

// TrigramSimilarity é o coeficiente de Dice sobre trigramas, em [0,1].
//
// Implementação própria e pura para que o scoring seja testável sem Postgres. O pg_trgm
// continua sendo usado no banco, mas apenas para GERAR candidatos baratos; a decisão
// final é sempre tomada aqui, com a mesma regra em todo lugar.
func TrigramSimilarity(a, b string) float64 {
	if a == "" && b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ta, tb := trigramas(a), trigramas(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	comuns := 0
	for tri := range ta {
		if tb[tri] {
			comuns++
		}
	}
	return 2 * float64(comuns) / float64(len(ta)+len(tb))
}

// trigramas devolve o conjunto de trigramas de uma string, com padding nas bordas para
// que prefixos e sufixos pesem — "aneis" e "aneis 2" não devem parecer idênticos.
func trigramas(s string) map[string]bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	padded := "  " + s + " "
	runas := []rune(padded)
	out := make(map[string]bool, len(runas))
	for i := 0; i+3 <= len(runas); i++ {
		out[string(runas[i:i+3])] = true
	}
	return out
}

// BestMatch escolhe o melhor candidato de uma lista. Empates são resolvidos pela ordem
// de entrada, que é estável — nunca por iteração de mapa.
func BestMatch(item MatchCandidate, candidatos []MatchCandidate) (int, MatchResult) {
	melhor := -1
	var melhorRes MatchResult
	for i, c := range candidatos {
		r := Score(item, c)
		if melhor == -1 || r.Score > melhorRes.Score {
			melhor, melhorRes = i, r
		}
	}
	if melhor == -1 {
		return -1, MatchResult{Decision: DecisionRejected, Signals: map[string]int{}, Notes: []string{}}
	}
	return melhor, melhorRes
}

// SignalNames devolve os sinais de um resultado em ordem determinística, para log e
// para exibição no painel.
func (r MatchResult) SignalNames() []string {
	out := make([]string, 0, len(r.Signals))
	for k := range r.Signals {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sameInt(a, b *int) bool { return a != nil && b != nil && *a == *b }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
