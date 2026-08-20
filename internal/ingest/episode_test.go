package ingest

import "testing"

func TestParseSeasonEpisode(t *testing.T) {
	tests := []struct {
		nome    string
		entrada string
		season  int
		episode int
		before  string
		after   string
		padrao  string
	}{
		{"SxxExx", "Breaking Bad S01E01 - Pilot", 1, 1, "Breaking Bad", "Pilot", "SxxExx"},
		{"SxxExx minúsculo", "breaking bad s1e2", 1, 2, "breaking bad", "", "SxxExx"},
		{"SxxExx com espaço", "Breaking Bad S01 E02", 1, 2, "Breaking Bad", "", "SxxExx"},
		{"NxNN", "Breaking Bad 1x03", 1, 3, "Breaking Bad", "", "NxNN"},
		{"NxNN com sufixo", "Dark 2x04 Legendado", 2, 4, "Dark", "Legendado", "NxNN"},
		{"TxxEPxx", "Cidade Invisível T01 EP01", 1, 1, "Cidade Invisível", "", "TxxExx"},
		{"TxxExx sem EP", "Cidade Invisível T02E05", 2, 5, "Cidade Invisível", "", "TxxExx"},
		{"temporada episódio", "Cidade Invisível Temporada 1 Episódio 2", 1, 2, "Cidade Invisível", "", "temporada-episodio"},
		{"temporada episodio sem acento", "Serie X Temporada 3 Episodio 7", 3, 7, "Serie X", "", "temporada-episodio"},
		{"temporada ep abreviado", "Serie Y Temporada 2 Ep 9", 2, 9, "Serie Y", "", "temporada-episodio"},
		{"numero temporada", "Cidade Invisível 2 Temporada Ep 5", 2, 5, "Cidade Invisível", "", "numero-temporada-episodio"},
		{"numero temporada ordinal", "Serie Z 3ª Temporada Episódio 1", 3, 1, "Serie Z", "", "numero-temporada-episodio"},
		{"season episode", "Dark Season 2 Episode 4", 2, 4, "Dark", "", "season-episode"},
		{"episódio zero é válido", "Serie W S01E00", 1, 0, "Serie W", "", "SxxExx"},
		{"três dígitos de episódio", "Anime Q S01E120", 1, 120, "Anime Q", "", "SxxExx"},
	}

	for _, tc := range tests {
		t.Run(tc.nome, func(t *testing.T) {
			got, ok := ParseSeasonEpisode(tc.entrada)
			if !ok {
				t.Fatalf("ParseSeasonEpisode(%q) não reconheceu o padrão", tc.entrada)
			}
			if got.Season != tc.season || got.Episode != tc.episode {
				t.Errorf("S%dE%d, esperava S%dE%d", got.Season, got.Episode, tc.season, tc.episode)
			}
			if got.Before != tc.before {
				t.Errorf("before = %q, esperava %q", got.Before, tc.before)
			}
			if got.After != tc.after {
				t.Errorf("after = %q, esperava %q", got.After, tc.after)
			}
			if got.Pattern != tc.padrao {
				t.Errorf("padrão = %q, esperava %q", got.Pattern, tc.padrao)
			}
		})
	}
}

func TestParseSeasonEpisodeNaoReconhece(t *testing.T) {
	naoDeveCasar := []string{
		"Interestelar (2014)",
		"Toy Story",
		"Rocky II",
		"Filme 1080p DUAL",
		"Série Sem Numeração - Temporada Completa",
		"",
	}
	for _, entrada := range naoDeveCasar {
		if m, ok := ParseSeasonEpisode(entrada); ok {
			t.Errorf("ParseSeasonEpisode(%q) casou indevidamente: S%dE%d via %s",
				entrada, m.Season, m.Episode, m.Pattern)
		}
	}
}

// Um ano de quatro dígitos não pode ser confundido com "1x02" nem com temporada.
func TestParseSeasonEpisodeNaoConfundeAno(t *testing.T) {
	if m, ok := ParseSeasonEpisode("Interestelar 2014"); ok {
		t.Errorf("ano virou temporada/episódio: S%dE%d", m.Season, m.Episode)
	}
	if m, ok := ParseSeasonEpisode("Blade Runner 2049 1080p"); ok {
		t.Errorf("ano virou temporada/episódio: S%dE%d", m.Season, m.Episode)
	}
}

// Casos reais que apareceram na fila de não resolvidos de um catálogo de produção.
// Todos são FILMES cujo nome contém uma palavra de série.
func TestLooksLikeSeriesNaoConfundeFilmesComNomeParecido(t *testing.T) {
	filmes := []string{
		"Temporada de Sangue",
		"Temporada de Furacão",
		"Temporada de Caça",
		"Temporada de Traição",
		"Alerta! Temporada de Tubarões",
		"Temporada",
		"Star Wars: Episódio II - Ataque dos Clones",
		"Star Wars: Episódio III - A Vingança dos Sith",
		"BLUE LOCK O FILME -EPISÓDIO NAGI-",
		"Andre Matos - Maestro do Rock - Episódio I",
		"Breakup Season",
		// O genitivo em inglês fazia "'s 2" casar como "Season 2".
		"Five Nights at Freddy's 2",
		"Porky's 2: O Dia Seguinte",
		"Porky's 3: Porky's Contra-Ataca",
		"Z-O-M-B-I-E-S 2",
		"Troca de Bebês 2",
	}
	for _, f := range filmes {
		if LooksLikeSeries(f) {
			t.Errorf("LooksLikeSeries(%q) = true — é um filme e foi para a fila de não resolvidos", f)
		}
	}
}

func TestTipoPelaCategoria(t *testing.T) {
	tests := map[string]ItemKind{
		"Filmes | Ação":        ItemKindMovie,
		"FILMES | LANÇAMENTOS": ItemKindMovie,
		"Movies":               ItemKindMovie,
		"Cinema Nacional":      ItemKindMovie,
		"Séries | Drama":       ItemKindEpisode,
		"SERIES":               ItemKindEpisode,
		"Animes":               ItemKindEpisode,
		"Novelas":              ItemKindEpisode,
		"Doramas":              ItemKindEpisode,
		// Ambíguas ou silenciosas não decidem nada.
		"Filmes e Séries": ItemKindUnresolved,
		"Documentarios":   ItemKindUnresolved,
		"":                ItemKindUnresolved,
		"Diversos":        ItemKindUnresolved,
	}
	for grupo, esperado := range tests {
		if got := TipoPelaCategoria(grupo); got != esperado {
			t.Errorf("TipoPelaCategoria(%q) = %q, esperava %q", grupo, got, esperado)
		}
	}
}

func TestLooksLikeSeries(t *testing.T) {
	series := []string{
		"Breaking Bad S01E01",
		"Cidade Invisível Temporada 1",
		"Dark Season 2",
		"Série X - Episódio 4",
		"Serie Y T03",
		"Anime Z Ep 12",
		"Serie W 2 Temporada",
	}
	for _, s := range series {
		if !LooksLikeSeries(s) {
			t.Errorf("LooksLikeSeries(%q) = false, esperava true", s)
		}
	}

	filmes := []string{
		"Interestelar (2014)",
		"Toy Story",
		"A Origem 1080p",
		"Rocky II",
	}
	for _, f := range filmes {
		if LooksLikeSeries(f) {
			t.Errorf("LooksLikeSeries(%q) = true, esperava false", f)
		}
	}
}
