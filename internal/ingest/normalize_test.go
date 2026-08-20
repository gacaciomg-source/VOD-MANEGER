package ingest

import (
	"encoding/json"
	"strings"
	"testing"
)

func normalizador(t *testing.T) *Normalizer {
	t.Helper()
	n, err := NewNormalizer(WithClock(agora))
	if err != nil {
		t.Fatalf("NewNormalizer: %v", err)
	}
	return n
}

func itemM3U(titulo, grupo, url string) RawItem {
	return RawItem{
		Kind:       RawKindUnknown,
		ExternalID: "",
		Title:      titulo,
		GroupTitle: grupo,
		StreamURL:  url,
		Attrs:      map[string]string{"tvg-name": titulo},
		Origin:     RawOrigin{Provider: "m3u", Endpoint: "playlist"},
	}
}

func TestNormalizeFilme(t *testing.T) {
	n := normalizador(t)
	raw := itemM3U("Interestelar (2014)", "FILMES | FICÇÃO", "http://fonte-a.exemplo.tld/movie/u/s/12345.mp4")
	raw.ExternalID = "12345"

	item := n.Normalize(7, raw, CategoryFilter{})

	if item.Kind != ItemKindMovie {
		t.Fatalf("kind = %q, esperava movie (rejeição: %+v)", item.Kind, item.Rejection)
	}
	if item.Movie.Title.Declared != "Interestelar (2014)" {
		t.Errorf("declared = %q — o título bruto nunca pode ser sobrescrito", item.Movie.Title.Declared)
	}
	if item.Movie.Title.Normalized != "interestelar" {
		t.Errorf("normalized = %q", item.Movie.Title.Normalized)
	}
	if item.Movie.Year.Value == nil || *item.Movie.Year.Value != 2014 {
		t.Errorf("ano = %v", item.Movie.Year.Value)
	}
	if item.Variant.ExternalID != "12345" || !item.Variant.Stable() {
		t.Errorf("variante = %+v, esperava identidade por external_id", item.Variant)
	}
	if item.Media.ContainerExt != "mp4" {
		t.Errorf("container = %q", item.Media.ContainerExt)
	}
	if item.Category.NormalizedName != "filmes ficcao" {
		t.Errorf("categoria = %q", item.Category.NormalizedName)
	}
}

// O requisito central: toda decisão diz de qual campo veio e por qual regra.
func TestNormalizeRegistraProcedencia(t *testing.T) {
	n := normalizador(t)

	t.Run("ano do título", func(t *testing.T) {
		item := n.Normalize(1, itemM3U("Interestelar (2014)", "", "http://x.exemplo.tld/a.mp4"), CategoryFilter{})
		if got := item.Movie.Title.Prov; got.Source != SourceM3UTvgName || got.Rule != RuleTitleCleanupV1 {
			t.Errorf("procedência do título = %+v", got)
		}
		if got := item.Movie.Year.Prov; got.Rule != RuleYearFromTitleV1 {
			t.Errorf("procedência do ano = %+v, esperava regra %s", got, RuleYearFromTitleV1)
		}
	})

	t.Run("ano de campo próprio vale mais que o do título", func(t *testing.T) {
		raw := itemM3U("Interestelar (2013)", "", "http://x.exemplo.tld/a.mp4")
		raw.Origin.Provider = "xtream"
		raw.Kind = RawKindMovie
		ano := 2014
		raw.Year = &ano

		item := n.Normalize(1, raw, CategoryFilter{})
		if *item.Movie.Year.Value != 2014 {
			t.Errorf("ano = %d, o campo próprio deveria ganhar do título", *item.Movie.Year.Value)
		}
		if item.Movie.Year.Prov.Rule != RuleYearFromFieldV1 {
			t.Errorf("procedência do ano = %+v", item.Movie.Year.Prov)
		}
		if item.Movie.Year.Prov.Source != SourceXtreamReleaseDate {
			t.Errorf("origem do ano = %q", item.Movie.Year.Prov.Source)
		}
	})

	t.Run("ano ausente", func(t *testing.T) {
		item := n.Normalize(1, itemM3U("Toy Story", "", "http://x.exemplo.tld/a.mp4"), CategoryFilter{})
		if item.Movie.Year.Value != nil {
			t.Errorf("ano deveria ser nulo, veio %v", *item.Movie.Year.Value)
		}
		if item.Movie.Year.Prov.Source != SourceNenhum || item.Movie.Year.Prov.Rule != RuleYearAusenteV1 {
			t.Errorf("procedência do ano ausente = %+v", item.Movie.Year.Prov)
		}
	})

	t.Run("numeração do episódio vinda do título registra o padrão", func(t *testing.T) {
		item := n.Normalize(1, itemM3U("Breaking Bad S01E01 - Pilot", "", "http://x.exemplo.tld/a.mkv"), CategoryFilter{})
		if item.Kind != ItemKindEpisode {
			t.Fatalf("kind = %q", item.Kind)
		}
		if !strings.HasPrefix(item.Episode.NumberProv.Rule, RuleSeasonEpisodeTitleV1) {
			t.Errorf("regra = %q, esperava prefixo %s", item.Episode.NumberProv.Rule, RuleSeasonEpisodeTitleV1)
		}
		if !strings.Contains(item.Episode.NumberProv.Rule, "SxxExx") {
			t.Errorf("a regra deveria identificar o padrão reconhecido: %q", item.Episode.NumberProv.Rule)
		}
	})
}

func TestNormalizeEpisodioDoTitulo(t *testing.T) {
	n := normalizador(t)
	item := n.Normalize(1, itemM3U("Breaking Bad S01E01 - Pilot", "SÉRIES | DRAMA", "http://x.exemplo.tld/a.mkv"), CategoryFilter{})

	if item.Kind != ItemKindEpisode {
		t.Fatalf("kind = %q (rejeição: %+v)", item.Kind, item.Rejection)
	}
	if item.Episode.SeriesTitle.Normalized != "breaking bad" {
		t.Errorf("série = %q", item.Episode.SeriesTitle.Normalized)
	}
	if item.Episode.Season != 1 || item.Episode.Episode != 1 {
		t.Errorf("S%dE%d", item.Episode.Season, item.Episode.Episode)
	}
	if item.Episode.EpisodeTitle.Display != "Pilot" {
		t.Errorf("título do episódio = %q", item.Episode.EpisodeTitle.Display)
	}
}

func TestNormalizeEpisodioComCamposProprios(t *testing.T) {
	n := normalizador(t)
	temporada, episodio := 2, 4
	raw := RawItem{
		Kind:        RawKindEpisode,
		ExternalID:  "55021",
		SeriesExtID: "5501",
		SeriesTitle: "Breaking Bad",
		Title:       "Seven Thirty-Seven",
		SeasonNum:   &temporada,
		EpisodeNum:  &episodio,
		StreamRef:   &StreamRef{Kind: StreamRefXtreamSeries, ID: "55021", Extension: "mkv"},
		Origin:      RawOrigin{Provider: "xtream", Endpoint: "get_series_info"},
	}

	item := n.Normalize(1, raw, CategoryFilter{})
	if item.Kind != ItemKindEpisode {
		t.Fatalf("kind = %q (rejeição: %+v)", item.Kind, item.Rejection)
	}
	if item.Episode.SeriesTitle.Normalized != "breaking bad" {
		t.Errorf("série = %q — deveria vir do campo separado, não do título do episódio",
			item.Episode.SeriesTitle.Normalized)
	}
	if item.Episode.EpisodeTitle.Display != "Seven Thirty-Seven" {
		t.Errorf("título do episódio = %q", item.Episode.EpisodeTitle.Display)
	}
	if item.Episode.NumberProv.Rule != RuleSeasonEpisodeFieldV1 {
		t.Errorf("regra dos números = %q", item.Episode.NumberProv.Rule)
	}
	if item.Media.OriginURL != "" {
		t.Error("item de Xtream não deveria ter URL materializada na ingestão")
	}
	if item.Media.StreamRef == nil {
		t.Error("StreamRef deveria estar presente")
	}
}

// A decisão aprovada (docs/07 §4.3): nunca converter em filme por descarte.
func TestNormalizeSerieSemNumeracaoViraUnresolved(t *testing.T) {
	n := normalizador(t)
	item := n.Normalize(1, itemM3U("Série Sem Numeração - Temporada Completa", "SÉRIES", "http://x.exemplo.tld/a.mp4"), CategoryFilter{})

	if item.Kind != ItemKindUnresolved {
		t.Fatalf("kind = %q, esperava unresolved", item.Kind)
	}
	if item.Rejection == nil || item.Rejection.Reason != RejectTemporadaEpisodioAusente {
		t.Fatalf("rejeição = %+v", item.Rejection)
	}
	if item.Movie != nil {
		t.Error("um item de série jamais pode virar filme por descarte")
	}
}

func TestNormalizeRejeicoes(t *testing.T) {
	n := normalizador(t)

	tests := []struct {
		nome   string
		raw    RawItem
		filtro CategoryFilter
		motivo RejectReason
	}{
		{
			nome:   "sem título",
			raw:    itemM3U("   ", "FILMES", "http://x.exemplo.tld/a.mp4"),
			motivo: RejectSemTitulo,
		},
		{
			nome:   "sem mídia",
			raw:    itemM3U("Filme Sem URL", "FILMES", ""),
			motivo: RejectSemMidia,
		},
		{
			nome:   "URL relativa",
			raw:    itemM3U("Filme", "FILMES", "/caminho/relativo/a.mp4"),
			motivo: RejectURLInvalida,
		},
		{
			nome:   "playlist ao vivo não é VOD",
			raw:    itemM3U("Canal Ao Vivo", "CANAIS", "http://x.exemplo.tld/live/90001.m3u8"),
			motivo: RejectNaoEVOD,
		},
		{
			nome:   "categoria ignorada",
			raw:    itemM3U("Filme", "ADULTO", "http://x.exemplo.tld/a.mp4"),
			filtro: CategoryFilter{Ignored: []string{"adulto"}},
			motivo: RejectCategoriaFiltrada,
		},
		{
			nome:   "categoria fora da lista permitida",
			raw:    itemM3U("Filme", "ESPORTES", "http://x.exemplo.tld/a.mp4"),
			filtro: CategoryFilter{Allowed: []string{"FILMES | AÇÃO"}},
			motivo: RejectCategoriaFiltrada,
		},
	}

	for _, tc := range tests {
		t.Run(tc.nome, func(t *testing.T) {
			item := n.Normalize(1, tc.raw, tc.filtro)
			if item.Kind != ItemKindUnresolved {
				t.Fatalf("kind = %q, esperava unresolved", item.Kind)
			}
			if item.Rejection == nil || item.Rejection.Reason != tc.motivo {
				t.Fatalf("motivo = %+v, esperava %s", item.Rejection, tc.motivo)
			}
			if strings.Contains(item.Rejection.Detail, "exemplo.tld") {
				t.Errorf("o detalhe da rejeição vazou a URL: %q", item.Rejection.Detail)
			}
		})
	}
}

func TestCategoryFilter(t *testing.T) {
	f := CategoryFilter{Allowed: []string{"FILMES | AÇÃO", "Filmes Ficção"}}
	if !f.Permite("filmes acao") {
		t.Error("a comparação deveria ser por forma canônica")
	}
	if !f.Permite("FILMES  -  FICÇÃO") {
		t.Error("a comparação deveria ignorar pontuação e acento")
	}
	if f.Permite("ESPORTES") {
		t.Error("categoria fora da lista permitida deveria ser recusada")
	}

	semLista := CategoryFilter{}
	if !semLista.Permite("QUALQUER") {
		t.Error("sem lista de permitidas, tudo passa")
	}

	// Ignored ganha de Allowed: negar é mais forte que permitir.
	ambos := CategoryFilter{Allowed: []string{"FILMES"}, Ignored: []string{"FILMES"}}
	if ambos.Permite("FILMES") {
		t.Error("a lista de ignoradas deve ter precedência")
	}
}

func TestVariantKeyUsaURLQuandoNaoHaExternalID(t *testing.T) {
	n := normalizador(t)
	raw := itemM3U("Filme Sem ID", "FILMES", "http://fonte-b.exemplo.tld/vod/98765.mp4?token=abc&expires=1700000000")

	item := n.Normalize(1, raw, CategoryFilter{})
	if item.Variant.ExternalID != "" {
		t.Fatalf("external_id = %q, esperava vazio", item.Variant.ExternalID)
	}
	if item.Variant.URLHash == "" {
		t.Fatal("url_hash deveria estar preenchido")
	}
	if item.Variant.Stable() {
		t.Error("identidade por hash de URL não é estável e não deveria se declarar como tal")
	}

	// Rotacionar o token não pode criar outra variante.
	raw2 := raw
	raw2.StreamURL = "http://fonte-b.exemplo.tld/vod/98765.mp4?token=xyz&expires=1800000000"
	item2 := n.Normalize(1, raw2, CategoryFilter{})
	if item.Variant.URLHash != item2.Variant.URLHash {
		t.Error("rotação de token na query gerou uma variante nova — isso incharia o catálogo")
	}
}

func TestDigestMudaSoQuandoOValorMuda(t *testing.T) {
	n := normalizador(t)
	base := itemM3U("Interestelar (2014)", "FILMES", "http://x.exemplo.tld/a.mp4")
	base.ExternalID = "12345"

	original := n.Normalize(1, base, CategoryFilter{})

	t.Run("reprocessar não muda o digest", func(t *testing.T) {
		outra := n.Normalize(1, base, CategoryFilter{})
		if original.Digest != outra.Digest {
			t.Error("o mesmo item produziu digests diferentes — o sync incremental não funcionaria")
		}
	})

	t.Run("mudança irrelevante não muda o digest", func(t *testing.T) {
		variacao := base
		variacao.Payload = json.RawMessage(`{"campo_novo_do_fornecedor":1}`)
		if n.Normalize(1, variacao, CategoryFilter{}).Digest != original.Digest {
			t.Error("campo Vendor no payload alterou o digest — causaria reescrita desnecessária")
		}
	})

	t.Run("mudança de título muda o digest", func(t *testing.T) {
		variacao := base
		variacao.Title = "Interestelar (2015)"
		variacao.Attrs = map[string]string{"tvg-name": variacao.Title}
		if n.Normalize(1, variacao, CategoryFilter{}).Digest == original.Digest {
			t.Error("mudança de ano não alterou o digest")
		}
	})

	t.Run("mudança de URL muda o digest", func(t *testing.T) {
		variacao := base
		variacao.StreamURL = "http://x.exemplo.tld/b.mp4"
		if n.Normalize(1, variacao, CategoryFilter{}).Digest == original.Digest {
			t.Error("mudança de URL não alterou o digest")
		}
	})
}

// Todo campo classificado como Garantido precisa estar preenchido em toda saída.
func TestCamposGarantidosEstaoSemprePreenchidos(t *testing.T) {
	n := normalizador(t)
	entradas := []RawItem{
		itemM3U("Interestelar (2014)", "FILMES", "http://x.exemplo.tld/a.mp4"),
		itemM3U("Breaking Bad S01E01", "SÉRIES", "http://x.exemplo.tld/b.mkv"),
		itemM3U("Série Sem Numeração - Temporada", "SÉRIES", "http://x.exemplo.tld/c.mp4"),
		itemM3U("", "FILMES", "http://x.exemplo.tld/d.mp4"),
		itemM3U("Filme", "FILMES", ""),
	}

	for i, raw := range entradas {
		item := n.Normalize(1, raw, CategoryFilter{})

		if item.Kind == "" {
			t.Errorf("entrada %d: kind vazio", i)
		}
		if item.KindProv.Rule == "" || item.KindProv.Source == "" {
			t.Errorf("entrada %d: procedência do tipo incompleta: %+v", i, item.KindProv)
		}
		if item.Variant.Prov.Rule == "" {
			t.Errorf("entrada %d: procedência da variante vazia", i)
		}
		if item.Digest == "" {
			t.Errorf("entrada %d: digest vazio", i)
		}
		if item.Signals.QualityTags == nil || item.Signals.LanguageTags == nil {
			t.Errorf("entrada %d: listas de tags nulas — o contrato exige lista vazia, nunca nula", i)
		}
		if item.Media.ContainerProv.Rule == "" {
			t.Errorf("entrada %d: procedência do container vazia", i)
		}
		if item.Category.Prov.Rule == "" {
			t.Errorf("entrada %d: procedência da categoria vazia", i)
		}
		if item.Kind == ItemKindUnresolved && item.Rejection == nil {
			t.Errorf("entrada %d: unresolved sem motivo declarado", i)
		}
		if item.Movie != nil && item.Movie.Title.Declared == "" {
			t.Errorf("entrada %d: título declarado perdido", i)
		}
	}
}

func TestSinaisNormalizados(t *testing.T) {
	n := normalizador(t)
	raw := itemM3U("Interestelar", "FILMES", "http://x.exemplo.tld/a.mp4")
	raw.TMDBID = "157336"
	raw.IMDBID = "TT0816692"

	item := n.Normalize(1, raw, CategoryFilter{})
	if item.Signals.TMDBID != "157336" {
		t.Errorf("tmdb = %q", item.Signals.TMDBID)
	}
	if item.Signals.IMDBID != "tt0816692" {
		t.Errorf("imdb = %q — deveria ser normalizado para minúsculas", item.Signals.IMDBID)
	}

	// Valores inválidos são descartados, não "corrigidos".
	ruim := itemM3U("Filme", "FILMES", "http://x.exemplo.tld/a.mp4")
	ruim.TMDBID = "não-é-número"
	ruim.IMDBID = "xx123"
	itemRuim := n.Normalize(1, ruim, CategoryFilter{})
	if itemRuim.Signals.TMDBID != "" || itemRuim.Signals.IMDBID != "" {
		t.Errorf("ids inválidos deveriam ser descartados: %+v", itemRuim.Signals)
	}
}

// As tags saem do título mas não somem do sistema.
func TestTagsSaemDoTituloMasSaoPreservadas(t *testing.T) {
	n := normalizador(t)
	item := n.Normalize(1, itemM3U("A Origem 2010 1080p DUAL", "FILMES", "http://x.exemplo.tld/a.mp4"), CategoryFilter{})

	if strings.Contains(item.Movie.Title.Normalized, "1080p") ||
		strings.Contains(item.Movie.Title.Normalized, "dual") {
		t.Errorf("as tags contaminaram o título normalizado: %q", item.Movie.Title.Normalized)
	}
	if len(item.Signals.QualityTags) == 0 || len(item.Signals.LanguageTags) == 0 {
		t.Errorf("as tags foram descartadas em vez de preservadas: %+v", item.Signals)
	}
	if item.Signals.TagsProv.Rule != RuleTagsV1 {
		t.Errorf("procedência das tags = %+v", item.Signals.TagsProv)
	}
}
