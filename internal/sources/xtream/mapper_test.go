package xtream_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"vodmanager/internal/ingest"
	"vodmanager/internal/sources/xtream"
	"vodmanager/test/fixtures"
)

func agora() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) }

func mapaCategorias(t *testing.T) map[string]string {
	t.Helper()
	cats, err := xtream.ParseCategories(fixtures.Read(t, "xtream/vod_categories.json"), "movie")
	if err != nil {
		t.Fatalf("ParseCategories: %v", err)
	}
	m := make(map[string]string, len(cats))
	for _, c := range cats {
		m[c.ID] = c.Name
	}
	return m
}

func TestParseCategories(t *testing.T) {
	cats, err := xtream.ParseCategories(fixtures.Read(t, "xtream/vod_categories.json"), "movie")
	if err != nil {
		t.Fatalf("ParseCategories: %v", err)
	}
	// A categoria sem id é descartada: sem id não há como vincular item nenhum.
	if len(cats) != 4 {
		t.Fatalf("categorias = %d, esperava 4", len(cats))
	}

	porID := map[string]string{}
	for _, c := range cats {
		porID[c.ID] = c.Name
		if c.ContentType != "movie" {
			t.Errorf("content_type = %q", c.ContentType)
		}
	}
	// category_id vem como string numa entrada e como número em outra.
	if porID["10"] != "FILMES | AÇÃO" {
		t.Errorf("categoria 10 = %q", porID["10"])
	}
	if porID["12"] != "FILMES | CLÁSSICOS" {
		t.Errorf("categoria 12 (id numérico) = %q — o parser precisa tolerar os dois tipos", porID["12"])
	}
}

func TestParseCategoriesInvalido(t *testing.T) {
	if _, err := xtream.ParseCategories([]byte(`{"erro":"não autorizado"}`), "movie"); err == nil {
		t.Error("esperava erro quando a resposta não é uma lista")
	}
}

func TestParseVODStreams(t *testing.T) {
	cats := mapaCategorias(t)
	var itens []ingest.RawItem
	n, err := xtream.ParseVODStreams(fixtures.Read(t, "xtream/vod_streams.json"), cats, agora(),
		func(i ingest.RawItem) error { itens = append(itens, i); return nil })
	if err != nil {
		t.Fatalf("ParseVODStreams: %v", err)
	}
	// O item sem stream_id é descartado.
	if n != 4 {
		t.Fatalf("emitidos = %d, esperava 4", n)
	}

	primeiro := itens[0]
	if primeiro.Kind != ingest.RawKindMovie {
		t.Errorf("kind = %q", primeiro.Kind)
	}
	if primeiro.ExternalID != "12345" {
		t.Errorf("external_id = %q — stream_id numérico deveria virar string", primeiro.ExternalID)
	}
	if primeiro.Title != "Interestelar (2014)" {
		t.Errorf("título = %q", primeiro.Title)
	}
	if primeiro.GroupTitle != "FILMES | FICÇÃO" {
		t.Errorf("grupo = %q — deveria resolver pelo mapa de categorias", primeiro.GroupTitle)
	}
	if primeiro.Year == nil || *primeiro.Year != 2014 {
		t.Errorf("ano = %v — deveria vir de releaseDate", primeiro.Year)
	}
	if primeiro.TMDBID != "157336" {
		t.Errorf("tmdb = %q", primeiro.TMDBID)
	}
	if primeiro.Rating == nil || *primeiro.Rating != 8.6 {
		t.Errorf("rating = %v — veio como string e deveria ser tolerado", primeiro.Rating)
	}
	if primeiro.DurationSeconds == nil || *primeiro.DurationSeconds != 169*60 {
		t.Errorf("duração = %v", primeiro.DurationSeconds)
	}

	// Este é o ponto central: o mapper não monta URL e não conhece credenciais.
	if primeiro.StreamURL != "" {
		t.Errorf("o mapper Xtream não deveria produzir URL: %q", primeiro.StreamURL)
	}
	if primeiro.StreamRef == nil || primeiro.StreamRef.Kind != ingest.StreamRefXtreamMovie {
		t.Fatalf("stream_ref = %+v", primeiro.StreamRef)
	}
	if primeiro.StreamRef.ID != "12345" || primeiro.StreamRef.Extension != "mp4" {
		t.Errorf("stream_ref = %+v", primeiro.StreamRef)
	}

	// Campos com tipos alternativos.
	segundo := itens[1]
	if segundo.ExternalID != "12346" {
		t.Errorf("stream_id string = %q", segundo.ExternalID)
	}
	if segundo.Year == nil || *segundo.Year != 2010 {
		t.Errorf("ano de campo `year` string = %v", segundo.Year)
	}
	if segundo.TMDBID != "27205" {
		t.Errorf("tmdb do campo alternativo `tmdb` = %q", segundo.TMDBID)
	}

	terceiro := itens[2]
	if terceiro.Rating != nil {
		t.Errorf("rating vazio deveria ficar nulo, veio %v", *terceiro.Rating)
	}
	if terceiro.GroupTitle != "FILMES | CLÁSSICOS" {
		t.Errorf("categoria com id numérico = %q", terceiro.GroupTitle)
	}
}

func TestParseSeriesList(t *testing.T) {
	cats, err := xtream.ParseCategories(fixtures.Read(t, "xtream/series_categories.json"), "series")
	if err != nil {
		t.Fatalf("ParseCategories: %v", err)
	}
	mapa := map[string]string{}
	for _, c := range cats {
		mapa[c.ID] = c.Name
	}

	series, err := xtream.ParseSeriesList(fixtures.Read(t, "xtream/series.json"), mapa)
	if err != nil {
		t.Fatalf("ParseSeriesList: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("séries = %d, esperava 2 (a sem series_id é descartada)", len(series))
	}

	bb := series[0]
	if bb.ID != "5501" || bb.Name != "Breaking Bad" {
		t.Errorf("série = %+v", bb)
	}
	if bb.Year != 2008 {
		t.Errorf("ano = %d — deveria vir de releaseDate", bb.Year)
	}
	if bb.CategoryName != "SÉRIES | DRAMA" {
		t.Errorf("categoria = %q", bb.CategoryName)
	}
	if bb.Digest == "" {
		t.Error("digest vazio — sem ele o incremental de get_series_info não funciona")
	}
	if bb.Digest == series[1].Digest {
		t.Error("séries diferentes com o mesmo digest")
	}
}

// O digest é o gatilho do incremental: mesma entrada, mesmo digest.
func TestSeriesDigestEEstavel(t *testing.T) {
	dados := fixtures.Read(t, "xtream/series.json")
	a, _ := xtream.ParseSeriesList(dados, nil)
	b, _ := xtream.ParseSeriesList(dados, nil)
	for i := range a {
		if a[i].Digest != b[i].Digest {
			t.Fatalf("digest instável para a série %s", a[i].ID)
		}
	}
}

func TestParseSeriesInfo(t *testing.T) {
	series, err := xtream.ParseSeriesList(fixtures.Read(t, "xtream/series.json"), nil)
	if err != nil {
		t.Fatalf("ParseSeriesList: %v", err)
	}
	bb := series[0]
	bb.CategoryName = "SÉRIES | DRAMA"

	var itens []ingest.RawItem
	n, err := xtream.ParseSeriesInfo(fixtures.Read(t, "xtream/series_info_5501.json"), bb, agora(),
		func(i ingest.RawItem) error { itens = append(itens, i); return nil })
	if err != nil {
		t.Fatalf("ParseSeriesInfo: %v", err)
	}
	// O episódio sem id é descartado.
	if n != 3 {
		t.Fatalf("episódios = %d, esperava 3", n)
	}

	// A ordem precisa ser determinística e numérica: temporada 1 antes da 2.
	if itens[0].SeasonNum == nil || *itens[0].SeasonNum != 1 {
		t.Fatalf("primeiro episódio é da temporada %v, esperava 1", itens[0].SeasonNum)
	}
	if itens[2].SeasonNum == nil || *itens[2].SeasonNum != 2 {
		t.Fatalf("último episódio é da temporada %v, esperava 2", itens[2].SeasonNum)
	}

	primeiro := itens[0]
	if primeiro.Kind != ingest.RawKindEpisode {
		t.Errorf("kind = %q", primeiro.Kind)
	}
	if primeiro.ExternalID != "55001" {
		t.Errorf("external_id = %q", primeiro.ExternalID)
	}
	if primeiro.SeriesExtID != "5501" {
		t.Errorf("series_id = %q", primeiro.SeriesExtID)
	}
	if primeiro.SeriesTitle != "Breaking Bad" {
		t.Errorf("nome da série = %q — precisa vir separado do título do episódio", primeiro.SeriesTitle)
	}
	if primeiro.Title != "Pilot" {
		t.Errorf("título do episódio = %q", primeiro.Title)
	}
	if primeiro.EpisodeNum == nil || *primeiro.EpisodeNum != 1 {
		t.Errorf("episode_num = %v", primeiro.EpisodeNum)
	}
	if primeiro.DurationSeconds == nil || *primeiro.DurationSeconds != 3480 {
		t.Errorf("duração = %v", primeiro.DurationSeconds)
	}
	if primeiro.StreamURL != "" {
		t.Errorf("o mapper não deveria produzir URL: %q", primeiro.StreamURL)
	}
	if primeiro.StreamRef == nil || primeiro.StreamRef.Kind != ingest.StreamRefXtreamSeries {
		t.Fatalf("stream_ref = %+v", primeiro.StreamRef)
	}

	// Episódio sem título é aceito: muitas fontes não nomeiam episódios.
	if itens[1].Title != "" {
		t.Errorf("título do segundo episódio = %q, esperava vazio", itens[1].Title)
	}
	if itens[1].EpisodeNum == nil || *itens[1].EpisodeNum != 2 {
		t.Errorf("episode_num = %v", itens[1].EpisodeNum)
	}
}

// direct_source traz a URL completa com credencial. Ela chega no payload bruto, e é
// exatamente por isso que o payload precisa ser sanitizado antes de persistir.
func TestPayloadDeEpisodioESanitizavel(t *testing.T) {
	series, _ := xtream.ParseSeriesList(fixtures.Read(t, "xtream/series.json"), nil)

	var itens []ingest.RawItem
	if _, err := xtream.ParseSeriesInfo(fixtures.Read(t, "xtream/series_info_5501.json"), series[0], agora(),
		func(i ingest.RawItem) error { itens = append(itens, i); return nil }); err != nil {
		t.Fatalf("ParseSeriesInfo: %v", err)
	}

	var achouDirectSource bool
	for _, i := range itens {
		if strings.Contains(string(i.Payload), "direct_source") {
			achouDirectSource = true
			limpo := string(ingest.SanitizePayload(i.Payload))
			if strings.Contains(limpo, "senha") {
				t.Errorf("a credencial de direct_source sobreviveu à sanitização:\n%s", limpo)
			}
			if !strings.Contains(limpo, "direct_source") {
				t.Error("a chave direct_source deveria permanecer, só o valor é removido")
			}
		}
	}
	if !achouDirectSource {
		t.Fatal("o fixture deveria conter um episódio com direct_source")
	}
}

func TestParseSeriesInfoInvalido(t *testing.T) {
	_, err := xtream.ParseSeriesInfo([]byte(`não é json`), xtream.Series{ID: "1"}, agora(),
		func(ingest.RawItem) error { return nil })
	if err == nil {
		t.Error("esperava erro em JSON inválido")
	}
}

func TestParseVODStreamsToleraItensMalformados(t *testing.T) {
	entrada := []byte(`[
		{"stream_id":"1","name":"Bom","container_extension":"mp4"},
		"isto não é um objeto",
		{"stream_id":{"aninhado":true},"name":"Tipo estranho"},
		{"stream_id":"2","name":"Também bom"}
	]`)
	var itens []ingest.RawItem
	n, err := xtream.ParseVODStreams(entrada, nil, agora(),
		func(i ingest.RawItem) error { itens = append(itens, i); return nil })
	if err != nil {
		t.Fatalf("um item estranho não pode derrubar a lista inteira: %v", err)
	}
	if n != 2 {
		t.Errorf("emitidos = %d, esperava 2 itens válidos", n)
	}
}

func TestParseVODStreamsInterrompeQuandoEmitFalha(t *testing.T) {
	cats := mapaCategorias(t)
	limite := 2
	contador := 0
	n, err := xtream.ParseVODStreams(fixtures.Read(t, "xtream/vod_streams.json"), cats, agora(),
		func(ingest.RawItem) error {
			contador++
			if contador >= limite {
				return errTeto
			}
			return nil
		})
	if err != errTeto {
		t.Fatalf("erro = %v, esperava o erro do emit", err)
	}
	if n != limite-1 {
		t.Errorf("emitidos = %d", n)
	}
}

var errTeto = errTetoTipo{}

type errTetoTipo struct{}

func (errTetoTipo) Error() string { return "teto de itens atingido" }

// O payload persistido precisa ser o JSON original, para que campos Vendor sobrevivam
// à auditoria — e não a nossa struct reserializada.
func TestPayloadPreservaCamposDoFornecedor(t *testing.T) {
	cats := mapaCategorias(t)
	var itens []ingest.RawItem
	if _, err := xtream.ParseVODStreams(fixtures.Read(t, "xtream/vod_streams.json"), cats, agora(),
		func(i ingest.RawItem) error { itens = append(itens, i); return nil }); err != nil {
		t.Fatalf("ParseVODStreams: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(itens[0].Payload, &payload); err != nil {
		t.Fatalf("payload não é JSON válido: %v", err)
	}
	// rating_5based e added não têm campo tipado no contrato: são Vendor e precisam
	// continuar acessíveis para auditoria.
	for _, chave := range []string{"rating_5based", "added", "num"} {
		if _, ok := payload[chave]; !ok {
			t.Errorf("o payload perdeu o campo Vendor %q", chave)
		}
	}
}
