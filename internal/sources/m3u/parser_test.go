package m3u_test

import (
	"strings"
	"testing"
	"time"

	"vodmanager/internal/ingest"
	"vodmanager/internal/sources/m3u"
	"vodmanager/test/fixtures"
)

func agora() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) }

func parse(t *testing.T, rel string) ([]ingest.RawItem, m3u.Stats) {
	t.Helper()
	f := fixtures.Open(t, rel)
	var itens []ingest.RawItem
	stats, err := m3u.Parse(f, m3u.ParseOptions{FetchedAt: agora()}, func(i ingest.RawItem) error {
		itens = append(itens, i)
		return nil
	})
	if err != nil {
		t.Fatalf("Parse(%s): %v", rel, err)
	}
	return itens, stats
}

func TestParseFilmes(t *testing.T) {
	itens, stats := parse(t, "m3u/filmes.m3u")

	if stats.Items != 8 {
		t.Fatalf("itens = %d, esperava 8", stats.Items)
	}
	if len(itens) != stats.Items {
		t.Fatalf("emitidos %d, contados %d", len(itens), stats.Items)
	}

	primeiro := itens[0]
	if primeiro.Title != "Interestelar (2014)" {
		t.Errorf("título = %q", primeiro.Title)
	}
	if primeiro.ExternalID != "12345" {
		t.Errorf("external_id = %q — deveria vir de tvg-id", primeiro.ExternalID)
	}
	if primeiro.GroupTitle != "FILMES | FICÇÃO" {
		t.Errorf("grupo = %q", primeiro.GroupTitle)
	}
	if primeiro.StreamURL != "http://fonte-a.exemplo.tld/movie/usuario/senha/12345.mp4" {
		t.Errorf("url = %q", primeiro.StreamURL)
	}
	if primeiro.PosterURL == "" {
		t.Error("tvg-logo deveria virar poster")
	}
	if primeiro.Origin.Provider != "m3u" || primeiro.Origin.Line == 0 {
		t.Errorf("origem = %+v", primeiro.Origin)
	}

	// Duração positiva vira segundos; -1 significa desconhecida.
	if itens[2].DurationSeconds == nil || *itens[2].DurationSeconds != 7200 {
		t.Errorf("duração = %v", itens[2].DurationSeconds)
	}
	if itens[0].DurationSeconds != nil {
		t.Errorf("duração -1 deveria ficar nula, veio %v", *itens[0].DurationSeconds)
	}
}

// A vírgula que separa atributos do nome é a primeira FORA de aspas — não a última.
func TestParseTituloComVirgula(t *testing.T) {
	itens, _ := parse(t, "m3u/filmes.m3u")
	var achou bool
	for _, i := range itens {
		if i.ExternalID == "12352" {
			achou = true
			if i.Title != "Título, Com Vírgula (2020)" {
				t.Errorf("título = %q", i.Title)
			}
			if i.GroupTitle != "FILMES, DIVERSOS" {
				t.Errorf("grupo = %q — a vírgula dentro das aspas não pode dividir", i.GroupTitle)
			}
		}
	}
	if !achou {
		t.Fatal("item 12352 não foi emitido")
	}
}

func TestParseEXTGRPAplicaGrupo(t *testing.T) {
	itens, _ := parse(t, "m3u/filmes.m3u")
	for _, i := range itens {
		if i.ExternalID == "12350" {
			if i.GroupTitle != "FILMES | INFANTIL" {
				t.Errorf("grupo = %q, esperava o valor de #EXTGRP", i.GroupTitle)
			}
			return
		}
	}
	t.Fatal("item 12350 não foi emitido")
}

func TestParseSeries(t *testing.T) {
	itens, stats := parse(t, "m3u/series.m3u")
	if stats.Items != 8 {
		t.Fatalf("itens = %d, esperava 8", stats.Items)
	}
	if itens[0].Title != "Breaking Bad S01E01" {
		t.Errorf("título = %q — tvg-name tem precedência sobre o nome de exibição", itens[0].Title)
	}
	// O parser não interpreta temporada/episódio: isso é da normalização.
	if itens[0].SeasonNum != nil || itens[0].EpisodeNum != nil {
		t.Error("o parser M3U não deveria inferir temporada/episódio — isso é papel do normalizador")
	}
	if itens[0].Kind != ingest.RawKindUnknown {
		t.Errorf("kind = %q — o M3U não declara tipo, então deve ficar unknown", itens[0].Kind)
	}
}

func TestParseProblematicos(t *testing.T) {
	itens, stats := parse(t, "m3u/problematicos.m3u")

	if stats.ExtinfSemURL != 1 {
		t.Errorf("extinf sem URL = %d, esperava 1", stats.ExtinfSemURL)
	}
	if stats.URLSemExtinf != 1 {
		t.Errorf("URL sem extinf = %d, esperava 1", stats.URLSemExtinf)
	}
	if stats.SemTitulo != 1 {
		t.Errorf("sem título = %d, esperava 1", stats.SemTitulo)
	}
	if stats.LinhasIgnoradas != 2 {
		t.Errorf("linhas ignoradas = %d, esperava 2 (#EXTVLCOPT e #KODIPROP)", stats.LinhasIgnoradas)
	}

	// O item cujo #EXTINF ficou sem URL não pode ser emitido.
	for _, i := range itens {
		if i.ExternalID == "90003" {
			t.Error("item sem URL foi emitido")
		}
	}

	// Atributo sem aspas ainda é lido.
	achouSemAspas := false
	for _, i := range itens {
		if i.ExternalID == "90008" {
			achouSemAspas = true
			if i.GroupTitle != "FILMES" {
				t.Errorf("grupo sem aspas = %q", i.GroupTitle)
			}
		}
	}
	if !achouSemAspas {
		t.Error("item com atributo sem aspas não foi emitido")
	}
}

func TestParseInterrompeQuandoEmitFalha(t *testing.T) {
	f := fixtures.Open(t, "m3u/filmes.m3u")
	limite := 3
	contador := 0
	_, err := m3u.Parse(f, m3u.ParseOptions{}, func(ingest.RawItem) error {
		contador++
		if contador >= limite {
			return errTeto
		}
		return nil
	})
	if err != errTeto {
		t.Fatalf("erro = %v, esperava o erro do emit", err)
	}
	if contador != limite {
		t.Errorf("emitidos = %d, esperava parar em %d", contador, limite)
	}
}

var errTeto = errTetoTipo{}

type errTetoTipo struct{}

func (errTetoTipo) Error() string { return "teto de itens atingido" }

func TestParseListaVaziaEMalformada(t *testing.T) {
	casos := map[string]string{
		"vazia":           "",
		"só cabeçalho":    "#EXTM3U\n",
		"só comentários":  "#EXTM3U\n#EXTVLCOPT:x=1\n#comentário\n",
		"só URLs":         "http://x.exemplo.tld/a.mp4\nhttp://x.exemplo.tld/b.mp4\n",
		"extinf truncado": "#EXTM3U\n#EXTINF:",
	}
	for nome, conteudo := range casos {
		t.Run(nome, func(t *testing.T) {
			var itens []ingest.RawItem
			_, err := m3u.Parse(strings.NewReader(conteudo), m3u.ParseOptions{}, func(i ingest.RawItem) error {
				itens = append(itens, i)
				return nil
			})
			if err != nil {
				t.Fatalf("Parse não deveria falhar: %v", err)
			}
		})
	}
}

// O payload guardado nunca pode conter a URL da mídia.
func TestPayloadNaoContemURL(t *testing.T) {
	itens, _ := parse(t, "m3u/filmes.m3u")
	for _, i := range itens {
		if strings.Contains(string(i.Payload), i.StreamURL) && i.StreamURL != "" {
			t.Errorf("o payload do item %q contém a URL da mídia", i.Title)
		}
		if strings.Contains(string(i.Payload), "senha") {
			t.Errorf("o payload do item %q contém a credencial da URL", i.Title)
		}
	}
}

// Consumo de memória constante: o parser é streaming, então uma lista muito maior não
// pode exigir que ela caiba na memória de uma vez.
func TestParseEmStreaming(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	const total = 20000
	for i := 0; i < total; i++ {
		sb.WriteString(`#EXTINF:-1 tvg-id="`)
		sb.WriteString(strings.Repeat("0", 4))
		sb.WriteString(`" group-title="FILMES",Filme de Teste`)
		sb.WriteString("\nhttp://x.exemplo.tld/a.mp4\n")
	}

	vistos := 0
	stats, err := m3u.Parse(strings.NewReader(sb.String()), m3u.ParseOptions{}, func(ingest.RawItem) error {
		vistos++
		return nil // o item é descartado imediatamente: nada se acumula
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if stats.Items != total || vistos != total {
		t.Errorf("itens = %d/%d, esperava %d", stats.Items, vistos, total)
	}
}
