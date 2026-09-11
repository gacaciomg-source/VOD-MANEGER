package integration

import (
	"context"
	"testing"

	"vodmanager/internal/store"
)

// A estimativa conta cada TÍTULO uma vez, e não cada cópia que as fontes oferecem.
//
// A versão anterior multiplicava variantes pelo tamanho médio. O mesmo filme em três pastas
// de uma fonte e mais uma vez em outra entrava na conta quatro vezes — e o painel pedia
// quatro vezes o disco que o sistema de fato usaria, porque ele guarda uma cópia só.
func TestEstimativaContaCadaTituloUmaVez(t *testing.T) {
	_, env := newAPI(t)
	ctx := context.Background()

	love := cadastrarFonte(t, env, "Love", store.SourceKindM3U, "http://love.exemplo.tld/l.m3u", false)
	turbo := cadastrarFonte(t, env, "Turbo", store.SourceKindM3U, "http://turbo.exemplo.tld/l.m3u", false)
	env.Pool.Exec(ctx, `UPDATE sources SET priority = 1, enabled = true WHERE id = $1`, love.ID)
	env.Pool.Exec(ctx, `UPDATE sources SET priority = 2, enabled = true WHERE id = $1`, turbo.ID)

	filme, err := env.Store.CreateContent(ctx, store.NewContent{
		Type: store.ContentMovie, Title: "Filme", NormalizedTitle: "filme"})
	if err != nil {
		t.Fatalf("CreateContent: %v", err)
	}
	variante := func(fonte *store.Source, id string, qualidade ...string) {
		if _, err := env.Store.CreateVariant(ctx, store.NewVariant{
			SourceID: fonte.ID, TargetKind: store.TargetContent, TargetID: filme.ID,
			ExternalID: id, OriginURL: "http://x.tld/" + id, ContainerExt: "mp4",
			QualityTags: qualidade,
		}); err != nil {
			t.Fatalf("CreateVariant: %v", err)
		}
	}
	// A Love declara o filme em três pastas, em 1080p. A Turbo, uma vez, em SD.
	variante(love, "l1", "1080p")
	variante(love, "l2", "1080p")
	variante(love, "l3", "1080p")
	variante(turbo, "t1", "sd")

	est, err := env.Store.EstimarAcervo(ctx)
	if err != nil {
		t.Fatalf("EstimarAcervo: %v", err)
	}
	if est.Filmes != 1 {
		t.Fatalf("filmes = %d; quatro cópias do mesmo filme são UM título a guardar", est.Filmes)
	}
	if est.Variantes != 4 {
		t.Fatalf("variantes = %d, esperava 4", est.Variantes)
	}

	fhd, sd := pesoDe(t, est, "fhd"), pesoDe(t, est, "sd")
	// Seguindo a prioridade, fica a cópia da Love: um filme 1080p, uma vez.
	if est.Prioridade.Bytes != fhd {
		t.Errorf("prioridade = %d, esperava um 1080p (%d)", est.Prioridade.Bytes, fhd)
	}
	// A mais leve é a SD da Turbo; a melhor, a 1080p da Love.
	if est.MaisLeve.Bytes != sd {
		t.Errorf("mais leve = %d, esperava um SD (%d)", est.MaisLeve.Bytes, sd)
	}
	if est.MelhorQualidade.Bytes != fhd {
		t.Errorf("melhor qualidade = %d, esperava um 1080p (%d)", est.MelhorQualidade.Bytes, fhd)
	}
}

func pesoDe(t *testing.T, est *store.EstimativaDoAcervo, faixa string) int64 {
	t.Helper()
	for _, p := range est.Faixas {
		if p.Tipo == store.TargetContent && p.Faixa == faixa {
			return p.Bytes
		}
	}
	t.Fatalf("faixa %q não veio na resposta", faixa)
	return 0
}
