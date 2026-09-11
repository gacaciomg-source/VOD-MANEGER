package integration

import (
	"context"
	"testing"

	"vodmanager/internal/store"
)

// Unir duas séries tem que JUNTAR os episódios, e não só mudar as temporadas de dono.
//
// O defeito que este teste existe para impedir: a união movia as temporadas com um UPDATE
// simples. Só que temporada é única por (série, número) — e duas fontes da mesma série têm,
// as duas, "Temporada 1". A união quebrava na restrição, e a série continuava partida em
// duas: a de uma fonte com os episódios de uma fonte, a da outra com os da outra.
//
// Na TV, isso aparecia como a pior coisa possível para uma lista de prioridades: o episódio
// só conhecia UMA fonte. A principal podia estar no ar e funcionando que o sistema nem sabia
// que ela tinha aquele episódio — tocava a outra, morta, e não tinha para onde fugir.
func TestUnirSeriesJuntaOsEpisodiosDasDuasFontes(t *testing.T) {
	_, env := newAPI(t)
	ctx := context.Background()

	love := cadastrarFonte(t, env, "Love", store.SourceKindM3U, "http://love.exemplo.tld/l.m3u", false)
	turbo := cadastrarFonte(t, env, "Turbo", store.SourceKindM3U, "http://turbo.exemplo.tld/l.m3u", false)
	// A Love é a principal: prioridade menor toca primeiro.
	env.Pool.Exec(ctx, `UPDATE sources SET priority = 1, enabled = true WHERE id = $1`, love.ID)
	env.Pool.Exec(ctx, `UPDATE sources SET priority = 2, enabled = true WHERE id = $1`, turbo.ID)

	// Uma série por fonte, cada uma com "Temporada 1, Episódio 1" e mais um episódio só dela.
	serie := func(fonte *store.Source, soDela int) (int64, int64) {
		c, err := env.Store.CreateContent(ctx, store.NewContent{
			Type: store.ContentSeries, Title: "Rato", NormalizedTitle: "rato"})
		if err != nil {
			t.Fatalf("CreateContent: %v", err)
		}
		temp, err := env.Store.EnsureSeason(ctx, c.ID, 1)
		if err != nil {
			t.Fatalf("EnsureSeason: %v", err)
		}
		var primeiro int64
		for _, n := range []int{1, soDela} {
			ep, err := env.Store.EnsureEpisode(ctx, temp, n, "", "", "", nil)
			if err != nil {
				t.Fatalf("EnsureEpisode: %v", err)
			}
			if n == 1 {
				primeiro = ep
			}
			if _, err := env.Store.CreateVariant(ctx, store.NewVariant{
				SourceID: fonte.ID, TargetKind: store.TargetEpisode, TargetID: ep,
				ExternalID: fonte.Name + "-" + string(rune('0'+n)),
				OriginURL:  "http://" + fonte.Name + ".tld/e.mp4", ContainerExt: "mp4",
			}); err != nil {
				t.Fatalf("CreateVariant: %v", err)
			}
		}
		return c.ID, primeiro
	}
	serieTurbo, epTurbo := serie(turbo, 2)
	serieLove, _ := serie(love, 3)

	// Fica a da Turbo — o pior caso: quem sobra é justamente a série da fonte morta.
	if _, err := env.Store.UnirConteudos(ctx, serieTurbo, serieLove); err != nil {
		t.Fatalf("unir duas séries com a mesma temporada falhou: %v", err)
	}

	// O episódio 1 agora conhece as DUAS fontes, e a Love vem primeiro.
	_, variantes, err := env.Store.ResolveEpisodeForStream(ctx, epTurbo)
	if err != nil {
		t.Fatalf("ResolveEpisodeForStream: %v", err)
	}
	if len(variantes) != 2 {
		t.Fatalf("o episódio 1 conhece %d fontes, esperava 2 — sem a segunda, não há failover",
			len(variantes))
	}
	if variantes[0].SourceName != "Love" {
		t.Fatalf("tocaria primeiro a %q; a prioridade manda a Love", variantes[0].SourceName)
	}

	// Nada se perdeu: episódios 1, 2 e 3, numa temporada só.
	var temporadas, episodios int
	env.Pool.QueryRow(ctx,
		`SELECT count(*) FROM seasons WHERE series_content_id = $1`, serieTurbo).Scan(&temporadas)
	env.Pool.QueryRow(ctx, `
		SELECT count(*) FROM episodes e JOIN seasons se ON se.id = e.season_id
		WHERE se.series_content_id = $1`, serieTurbo).Scan(&episodios)
	if temporadas != 1 || episodios != 3 {
		t.Fatalf("temporadas=%d episódios=%d, esperava 1 e 3", temporadas, episodios)
	}

	// E a série da Love deixou de existir, sem levar nada junto.
	var sobrou int
	env.Pool.QueryRow(ctx, `SELECT count(*) FROM contents WHERE id = $1`, serieLove).Scan(&sobrou)
	if sobrou != 0 {
		t.Fatal("a série unida continuou no catálogo")
	}
}
