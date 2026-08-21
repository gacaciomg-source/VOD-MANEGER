package integration

import (
	"context"
	"errors"
	"testing"

	"vodmanager/internal/store"
)

// Unir categorias move acervo e apaga uma pasta. Se mover errado, o cliente vê filme em
// pasta de série; se apagar sem mover, o conteúdo some da lista sem ter sido apagado —
// que é a falha mais difícil de perceber, porque nada no banco parece errado.
func TestAbsorverCategoria(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	principal, err := env.Store.CriarPrincipal(ctx, "Ação", "acao", "movie")
	if err != nil {
		t.Fatalf("CriarPrincipal: %v", err)
	}
	secundaria, err := env.Store.EnsureCategory(ctx, "Filmes de Ação", "filmes de acao", "movie")
	if err != nil {
		t.Fatalf("EnsureCategory: %v", err)
	}

	criar := func(titulo, normalizado string, cat int64) int64 {
		t.Helper()
		c, err := env.Store.CreateContent(ctx, store.NewContent{
			Type: "movie", Title: titulo, NormalizedTitle: normalizado, CategoryID: &cat,
		})
		if err != nil {
			t.Fatalf("CreateContent(%s): %v", titulo, err)
		}
		return c.ID
	}
	a := criar("Filme A", "filme a", secundaria)
	b := criar("Filme B", "filme b", secundaria)
	// Já está no destino: não pode ser contado como movido nem se perder no caminho.
	c := criar("Filme C", "filme c", principal)

	movidos, err := env.Store.AbsorverCategoria(ctx, secundaria, principal)
	if err != nil {
		t.Fatalf("AbsorverCategoria: %v", err)
	}
	if movidos != 2 {
		t.Fatalf("esperava 2 conteúdos movidos, obtive %d", movidos)
	}

	for _, id := range []int64{a, b, c} {
		got, err := env.Store.GetContent(ctx, id)
		if err != nil {
			t.Fatalf("GetContent(%d): %v", id, err)
		}
		if got.CategoryID == nil || *got.CategoryID != principal {
			t.Fatalf("conteúdo %d ficou na categoria %v, esperava %d", id, got.CategoryID, principal)
		}
	}

	// A pasta de origem não pode sobreviver: sobrando, ela volta a aparecer na tela e a
	// decisão parece não ter surtido efeito.
	cats, err := env.Store.ListCategories(ctx)
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	for _, k := range cats {
		if k.ID == secundaria {
			t.Fatalf("a categoria de origem %d continua existindo", secundaria)
		}
		if k.ID == principal {
			if !k.Principal {
				t.Fatalf("o destino deixou de ser principal")
			}
			if k.ContentCount != 3 {
				t.Fatalf("o destino ficou com %d conteúdos, esperava 3", k.ContentCount)
			}
		}
	}
}

// As recusas importam tanto quanto o caminho feliz: unir tipos diferentes ou apontar para
// uma pasta que os clientes não veem transforma uma arrumação em perda de acervo.
func TestAbsorverCategoriaRecusas(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	filmes, err := env.Store.CriarPrincipal(ctx, "Ação", "acao", "movie")
	if err != nil {
		t.Fatalf("CriarPrincipal: %v", err)
	}
	series, err := env.Store.CriarPrincipal(ctx, "Séries de Ação", "series de acao", "series")
	if err != nil {
		t.Fatalf("CriarPrincipal séries: %v", err)
	}
	comum, err := env.Store.EnsureCategory(ctx, "Antiga", "antiga", "movie")
	if err != nil {
		t.Fatalf("EnsureCategory: %v", err)
	}
	naoPrincipal, err := env.Store.EnsureCategory(ctx, "Outra", "outra", "movie")
	if err != nil {
		t.Fatalf("EnsureCategory: %v", err)
	}

	casos := []struct {
		nome            string
		origem, destino int64
	}{
		{"tipos diferentes", comum, series},
		{"destino não é principal", comum, naoPrincipal},
		{"origem igual ao destino", filmes, filmes},
	}
	for _, k := range casos {
		t.Run(k.nome, func(t *testing.T) {
			if _, err := env.Store.AbsorverCategoria(ctx, k.origem, k.destino); !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("esperava ErrInvalid, obtive %v", err)
			}
		})
	}

	// Recusa não pode ter meio-efeito: a transação precisa ter voltado inteira.
	cats, err := env.Store.ListCategories(ctx)
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	achou := 0
	for _, c := range cats {
		if c.ID == comum || c.ID == naoPrincipal || c.ID == filmes || c.ID == series {
			achou++
		}
	}
	if achou != 4 {
		t.Fatalf("depois das recusas restaram %d das 4 categorias", achou)
	}
}

// O vínculo da fonte tem de acompanhar o conteúdo.
//
// A chave estrangeira é ON DELETE SET NULL: apagar a categoria de origem sem levar o
// vínculo junto deixaria source_categories apontando para nada — e a próxima
// sincronização veria isso como pendência de novo, recriando a pasta que acabou de ser
// unida. A decisão do administrador seria desfeita sozinha, sem erro nenhum aparecer.
func TestAbsorverCategoriaLevaOVinculoDaFonte(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	src, err := env.Store.CreateSource(ctx, store.NewSource{
		Name: "Fonte de Teste", Kind: store.SourceKindXtream, BaseURL: "http://exemplo.tld:8080",
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	principal, err := env.Store.CriarPrincipal(ctx, "Ação", "acao", "movie")
	if err != nil {
		t.Fatalf("CriarPrincipal: %v", err)
	}
	secundaria, err := env.Store.EnsureCategory(ctx, "Filmes de Ação", "filmes de acao", "movie")
	if err != nil {
		t.Fatalf("EnsureCategory: %v", err)
	}
	if err := env.Store.UpsertSourceCategory(ctx, src.ID, "42",
		"FILMES: AÇÃO", "filmes acao", "movie", secundaria); err != nil {
		t.Fatalf("UpsertSourceCategory: %v", err)
	}

	if _, err := env.Store.AbsorverCategoria(ctx, secundaria, principal); err != nil {
		t.Fatalf("AbsorverCategoria: %v", err)
	}

	// Se o vínculo tivesse virado NULL, ela apareceria aqui.
	pend, err := env.Store.ListarPendencias(ctx)
	if err != nil {
		t.Fatalf("ListarPendencias: %v", err)
	}
	for _, p := range pend {
		if p.Declarado == "FILMES: AÇÃO" {
			t.Fatal("a categoria de fonte voltou a ser pendência depois da união")
		}
	}

	vinculos, err := env.Store.VinculosDaFonte(ctx, src.ID)
	if err != nil {
		t.Fatalf("VinculosDaFonte: %v", err)
	}
	achou := false
	for _, id := range vinculos {
		if id == principal {
			achou = true
		}
		if id == secundaria {
			t.Fatalf("vínculo ainda aponta para a categoria apagada %d", secundaria)
		}
	}
	if !achou {
		t.Fatalf("nenhum vínculo da fonte passou a apontar para a principal %d", principal)
	}
}
