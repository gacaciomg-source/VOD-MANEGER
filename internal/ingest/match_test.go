package ingest

import (
	"reflect"
	"testing"
)

func filme(titulo string, ano *int) MatchCandidate {
	return MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: titulo, Year: ano}
}

func episodio(serie string, temporada, ep int) MatchCandidate {
	return MatchCandidate{
		Kind: ItemKindEpisode, NormalizedTitle: serie,
		Season: &temporada, Episode: &ep,
	}
}

func TestScoreDecisoes(t *testing.T) {
	tests := []struct {
		nome     string
		a, b     MatchCandidate
		decisao  MatchDecision
		minScore int
		maxScore int
	}{
		{
			nome: "título e ano idênticos agrupam automaticamente",
			a:    filme("interestelar", ptr(2014)), b: filme("interestelar", ptr(2014)),
			decisao: DecisionGrouped, minScore: ScoreAuto,
		},
		{
			nome:    "mesmo TMDB ID basta",
			a:       MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "interestelar", TMDBID: "157336"},
			b:       MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "interstellar", TMDBID: "157336"},
			decisao: DecisionGrouped, minScore: ScoreAuto,
		},
		{
			nome:    "TMDB divergente derruba mesmo com título igual",
			a:       MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "interestelar", TMDBID: "1"},
			b:       MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "interestelar", TMDBID: "2"},
			decisao: DecisionRejected, maxScore: ScoreReview - 1,
		},
		{
			// Listas M3U frequentemente não trazem ano. Se este caso não agrupasse, o
			// catálogo teria uma entrada duplicada por fonte para cada filme sem ano —
			// foi exatamente o que aconteceu no primeiro uso real.
			nome: "título idêntico sem ano em nenhum dos dois agrupa",
			a:    filme("toy story", nil), b: filme("toy story", nil),
			decisao: DecisionGrouped, minScore: ScoreAuto,
		},
		{
			nome: "título idêntico com ano só de um lado ainda agrupa",
			a:    filme("rocky 2", ptr(1979)), b: filme("rocky 2", nil),
			decisao: DecisionGrouped, minScore: ScoreAuto,
		},
		{
			// Bug real: a versão legendada foi agrupada com a dublada e sumiu da
			// categoria de legendados. Idioma separa conteúdos; qualidade não.
			nome: "versão legendada não agrupa com a dublada",
			a:    MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "seacabo diario das campeas"},
			b: MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "seacabo diario das campeas",
				LanguageKey: "leg"},
			decisao: DecisionRejected, maxScore: ScoreReview - 1,
		},
		{
			nome: "mesma versão de idioma agrupa normalmente",
			a: MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "interestelar",
				Year: ptr(2014), LanguageKey: "leg"},
			b: MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "interestelar",
				Year: ptr(2014), LanguageKey: "leg"},
			decisao: DecisionGrouped, minScore: ScoreAuto,
		},
		{
			// "DUAL" e "sem marcação" ambos têm áudio em português: são a mesma obra
			// vinda de fontes que escrevem diferente. Separá-las recriaria duplicatas.
			nome: "dual e sem marcação continuam agrupando",
			a: MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "duna", Year: ptr(2021),
				LanguageKey: LanguageKey([]string{"dual"})},
			b: MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "duna", Year: ptr(2021),
				LanguageKey: LanguageKey(nil)},
			decisao: DecisionGrouped, minScore: ScoreAuto,
		},
		{
			nome: "ano divergente separa remakes",
			a:    filme("duna", ptr(1984)), b: filme("duna", ptr(2021)),
			decisao: DecisionRejected, maxScore: ScoreReview - 1,
		},
		{
			nome: "ano com um de diferença ainda é plausível",
			a:    filme("o poderoso chefao", ptr(1972)), b: filme("o poderoso chefao", ptr(1973)),
			decisao: DecisionPendingReview,
		},
		{
			nome: "títulos diferentes não agrupam",
			a:    filme("interestelar", ptr(2014)), b: filme("toy story", ptr(1995)),
			decisao: DecisionRejected, maxScore: ScoreReview - 1,
		},
		{
			nome: "filme e episódio não se misturam",
			a:    filme("breaking bad", nil), b: episodio("breaking bad", 1, 1),
			decisao: DecisionRejected, maxScore: ScoreReview - 1,
		},
		{
			nome: "mesmo episódio agrupa",
			a:    episodio("breaking bad", 1, 1), b: episodio("breaking bad", 1, 1),
			decisao: DecisionGrouped, minScore: ScoreAuto,
		},
		{
			nome: "episódios diferentes da mesma série não agrupam",
			a:    episodio("breaking bad", 1, 1), b: episodio("breaking bad", 1, 2),
			decisao: DecisionRejected, maxScore: ScoreReview - 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.nome, func(t *testing.T) {
			got := Score(tc.a, tc.b)
			if got.Decision != tc.decisao {
				t.Errorf("decisão = %q (score %d, sinais %v), esperava %q",
					got.Decision, got.Score, got.Signals, tc.decisao)
			}
			if tc.minScore > 0 && got.Score < tc.minScore {
				t.Errorf("score = %d, esperava ao menos %d", got.Score, tc.minScore)
			}
			if tc.maxScore > 0 && got.Score > tc.maxScore {
				t.Errorf("score = %d, esperava no máximo %d", got.Score, tc.maxScore)
			}
			if got.Score < 0 || got.Score > 100 {
				t.Errorf("score fora da faixa: %d", got.Score)
			}
		})
	}
}

func TestScoreESimetrico(t *testing.T) {
	pares := [][2]MatchCandidate{
		{filme("interestelar", ptr(2014)), filme("interestelar", ptr(2014))},
		{filme("duna", ptr(1984)), filme("duna", ptr(2021))},
		{episodio("dark", 2, 4), episodio("dark", 2, 4)},
		{filme("toy story", nil), episodio("toy story", 1, 1)},
	}
	for _, p := range pares {
		ab, ba := Score(p[0], p[1]), Score(p[1], p[0])
		if ab.Score != ba.Score {
			t.Errorf("Score não é simétrico: %d vs %d para %+v", ab.Score, ba.Score, p)
		}
	}
}

// Regra inviolável: decisão manual travada não é revista pelo algoritmo.
func TestDecideRespeitaLocked(t *testing.T) {
	for _, score := range []int{0, 50, 80, 99, 100} {
		if got := Decide(score, true); got != DecisionLocked {
			t.Errorf("Decide(%d, locked) = %q, esperava %q", score, got, DecisionLocked)
		}
	}
	if got := Decide(100, false); got != DecisionGrouped {
		t.Errorf("Decide(100, destravado) = %q", got)
	}
}

func TestTrigramSimilarity(t *testing.T) {
	if got := TrigramSimilarity("interestelar", "interestelar"); got != 1 {
		t.Errorf("strings idênticas = %v, esperava 1", got)
	}
	if got := TrigramSimilarity("", ""); got != 0 {
		t.Errorf("strings vazias = %v, esperava 0", got)
	}
	if got := TrigramSimilarity("interestelar", ""); got != 0 {
		t.Errorf("uma string vazia = %v, esperava 0", got)
	}

	proximas := TrigramSimilarity("interestelar", "interstelar")
	distantes := TrigramSimilarity("interestelar", "toy story")
	if proximas <= distantes {
		t.Errorf("similaridade não discrimina: próximas=%v distantes=%v", proximas, distantes)
	}
	if proximas > 1 || distantes < 0 {
		t.Errorf("similaridade fora de [0,1]: %v / %v", proximas, distantes)
	}

	if TrigramSimilarity("a", "b") != TrigramSimilarity("b", "a") {
		t.Error("TrigramSimilarity não é simétrica")
	}
}

func TestBestMatch(t *testing.T) {
	alvo := filme("interestelar", ptr(2014))
	candidatos := []MatchCandidate{
		filme("toy story", ptr(1995)),
		filme("interestelar", ptr(2014)),
		filme("interestelar", ptr(2013)),
	}
	idx, res := BestMatch(alvo, candidatos)
	if idx != 1 {
		t.Errorf("melhor candidato = %d, esperava 1", idx)
	}
	if res.Decision != DecisionGrouped {
		t.Errorf("decisão = %q", res.Decision)
	}

	idxVazio, resVazio := BestMatch(alvo, nil)
	if idxVazio != -1 || resVazio.Decision != DecisionRejected {
		t.Errorf("lista vazia = (%d, %q)", idxVazio, resVazio.Decision)
	}
}

func TestSignalNamesEDeterministico(t *testing.T) {
	res := Score(filme("interestelar", ptr(2014)), filme("interestelar", ptr(2014)))
	primeiro := res.SignalNames()
	for i := 0; i < 20; i++ {
		if !reflect.DeepEqual(primeiro, res.SignalNames()) {
			t.Fatal("SignalNames não é determinístico")
		}
	}
	if len(primeiro) == 0 {
		t.Error("nenhum sinal registrado — o painel não teria o que explicar")
	}
}

func TestLanguageKey(t *testing.T) {
	tests := []struct {
		nome     string
		tags     []string
		esperado string
	}{
		{"sem marcação é a versão padrão", nil, ""},
		{"[L] é legendado", []string{"l"}, "leg"},
		{"legendado", []string{"legendado"}, "leg"},
		{"grafias diferentes convergem", []string{"leg"}, "leg"},
		{"duplicatas somem", []string{"leg", "legendado", "l"}, "leg"},
		{"idioma não mapeado entra como está", []string{"lat"}, "lat"},

		// Tudo que tem áudio em português equivale à versão padrão: são a mesma obra
		// escrita de formas diferentes por fontes diferentes.
		{"dublado equivale ao padrão", []string{"dublado"}, ""},
		{"nacional equivale ao padrão", []string{"nacional"}, ""},
		{"dual equivale ao padrão", []string{"dual"}, ""},
		{"pt-br equivale ao padrão", []string{"pt-br"}, ""},
		{"dublado e dual juntos ainda são o padrão", []string{"dublado", "dual"}, ""},

		// Legendado + dublado na mesma marcação: a presença do legendado é o que
		// caracteriza a versão.
		{"leg predomina sobre dub", []string{"leg", "dublado"}, "leg"},
	}
	for _, tc := range tests {
		if got := LanguageKey(tc.tags); got != tc.esperado {
			t.Errorf("%s: LanguageKey(%v) = %q, esperava %q", tc.nome, tc.tags, got, tc.esperado)
		}
	}

	// A chave precisa ser estável: ela vai para o banco e é comparada entre execuções.
	if LanguageKey([]string{"leg", "eng"}) != LanguageKey([]string{"eng", "leg"}) {
		t.Error("LanguageKey depende da ordem das tags")
	}
}

// Qualidade continua NÃO separando: é a mesma obra em resoluções diferentes, e o
// sistema deve poder escolher a melhor fonte.
func TestQualidadeNaoSeparaConteudo(t *testing.T) {
	a := MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "interestelar", Year: ptr(2014)}
	b := MatchCandidate{Kind: ItemKindMovie, NormalizedTitle: "interestelar", Year: ptr(2014)}
	// As tags de qualidade nem entram no candidato — ficam só em MatchSignals.
	if got := Score(a, b); got.Decision != DecisionGrouped {
		t.Errorf("decisão = %q, esperava agrupamento entre qualidades diferentes", got.Decision)
	}
}

func TestCandidateFrom(t *testing.T) {
	n := normalizador(t)
	item := n.Normalize(1, itemM3U("Breaking Bad S01E01", "SÉRIES", "http://x.exemplo.tld/a.mkv"), CategoryFilter{})
	c := CandidateFrom(item)

	if c.Kind != ItemKindEpisode || c.NormalizedTitle != "breaking bad" {
		t.Fatalf("candidato = %+v", c)
	}
	if c.Season == nil || *c.Season != 1 || c.Episode == nil || *c.Episode != 1 {
		t.Errorf("temporada/episódio = %v/%v", c.Season, c.Episode)
	}
}
