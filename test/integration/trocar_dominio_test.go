package integration

import (
	"context"
	"testing"

	"vodmanager/internal/ingest"
	"vodmanager/internal/store"
)

// Trocar o domínio atualiza o endereço da fonte, os links E a identidade dos links de M3U —
// e não encosta em domínio parecido.
func TestTrocarDominioEmMassa(t *testing.T) {
	_, env := newAPI(t)
	ctx := context.Background()

	love := cadastrarFonte(t, env, "Love", store.SourceKindM3U, "http://antigo.com:8080/get.php?u=x", false)
	outra := cadastrarFonte(t, env, "Outra", store.SourceKindM3U, "http://antigo.com.br/lista.m3u", false)

	filme, err := env.Store.CreateContent(ctx, store.NewContent{
		Type: store.ContentMovie, Title: "Filme", NormalizedTitle: "filme"})
	if err != nil {
		t.Fatalf("CreateContent: %v", err)
	}
	variante := func(fonte *store.Source, u string) int64 {
		h, _ := ingest.HashURL(u)
		v, err := env.Store.CreateVariant(ctx, store.NewVariant{
			SourceID: fonte.ID, TargetKind: store.TargetContent, TargetID: filme.ID,
			URLHash: h, OriginURL: u, ContainerExt: "mp4",
		})
		if err != nil {
			t.Fatalf("CreateVariant: %v", err)
		}
		return v.ID
	}
	daLove := variante(love, "http://antigo.com:8080/movie/u/s/1.mp4")
	// Domínio PARECIDO: não pode ser tocado.
	daOutra := variante(outra, "http://antigo.com.br/movie/u/s/2.mp4")

	// A prévia não muda nada.
	previa, err := env.Store.TrocarDominio(ctx, "http://Antigo.com/", "novo.net", true)
	if err != nil {
		t.Fatalf("simulando: %v", err)
	}
	if previa.Links != 1 || previa.Fontes != 1 || previa.NomesDasFontes[0] != "Love" {
		t.Fatalf("prévia = %+v, esperava 1 link e só a Love", previa)
	}
	var url string
	env.Pool.QueryRow(ctx, `SELECT origin_url FROM source_variants WHERE id = $1`, daLove).Scan(&url)
	if url != "http://antigo.com:8080/movie/u/s/1.mp4" {
		t.Fatalf("a prévia alterou o link: %s", url)
	}

	if _, err := env.Store.TrocarDominio(ctx, "antigo.com", "novo.net", false); err != nil {
		t.Fatalf("trocando: %v", err)
	}

	// O link da Love mudou de domínio e manteve a porta e o caminho.
	var hash string
	env.Pool.QueryRow(ctx, `SELECT origin_url, url_hash FROM source_variants WHERE id = $1`,
		daLove).Scan(&url, &hash)
	if url != "http://novo.net:8080/movie/u/s/1.mp4" {
		t.Fatalf("link da Love = %s", url)
	}
	// E a identidade acompanhou — senão a próxima sincronização duplicaria a fonte.
	if esperado, _ := ingest.HashURL(url); hash != esperado {
		t.Fatal("a identidade do link não foi recalculada: a próxima sincronização o duplicaria")
	}
	var base string
	env.Pool.QueryRow(ctx, `SELECT base_url FROM sources WHERE id = $1`, love.ID).Scan(&base)
	if base != "http://novo.net:8080/get.php?u=x" {
		t.Fatalf("endereço da fonte = %s", base)
	}

	// O domínio parecido ficou intacto.
	env.Pool.QueryRow(ctx, `SELECT origin_url FROM source_variants WHERE id = $1`, daOutra).Scan(&url)
	env.Pool.QueryRow(ctx, `SELECT base_url FROM sources WHERE id = $1`, outra.ID).Scan(&base)
	if url != "http://antigo.com.br/movie/u/s/2.mp4" || base != "http://antigo.com.br/lista.m3u" {
		t.Fatalf("mexeu no domínio parecido: %s / %s", url, base)
	}
}

func TestTrocarDominioRecusaOQueQuebrariaLinks(t *testing.T) {
	_, env := newAPI(t)
	ctx := context.Background()
	casos := map[string][2]string{
		"porta só no novo": {"antigo.com", "novo.com:9090"},
		"iguais":           {"antigo.com", "ANTIGO.com"},
		"vazio":            {"", "novo.com"},
		"com espaço":       {"antigo .com", "novo.com"},
	}
	for nome, c := range casos {
		if _, err := env.Store.TrocarDominio(ctx, c[0], c[1], true); err == nil {
			t.Errorf("%s: aceitou %q → %q", nome, c[0], c[1])
		}
	}
}
