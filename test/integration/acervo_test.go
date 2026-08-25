package integration

import (
	"context"
	"testing"

	"vodmanager/internal/store"
)

// TestConsultasDoAcervoExecutam existe porque uma delas não executava.
//
// A tela do Acervo caía com "erro interno ao listando o acervo", e a causa era uma consulta
// que o Postgres recusava por inteiro: ela faz junção com a tabela de contas de nuvem, e as
// duas tabelas têm colunas `id` e `criado_em`. Sem qualificar o apelido, a referência é
// ambígua.
//
// Nenhum teste chegava a executá-la. Compilar não prova nada aqui: SQL é texto até o
// primeiro contato com o banco, e o primeiro contato foi o administrador abrindo a tela.
//
// Por isso este teste roda cada consulta com o banco VAZIO. Não verifica resultado — se a
// consulta é aceita e devolve zero linhas, ela está bem formada. É o mínimo, e é exatamente
// o que faltava.
func TestConsultasDoAcervoExecutam(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	t.Run("listar sem filtro", func(t *testing.T) {
		if _, err := env.Store.ListarArquivos(ctx, store.FiltroDeArquivos{Limite: 50}); err != nil {
			t.Fatalf("ListarArquivos: %v", err)
		}
	})

	// Os filtros entram na cláusula WHERE e mudam o plano da consulta. Um deles pode estar
	// errado sem que o outro esteja.
	for _, origem := range []string{store.OrigemFonte, store.OrigemProprio} {
		t.Run("listar origem "+origem, func(t *testing.T) {
			_, err := env.Store.ListarArquivos(ctx, store.FiltroDeArquivos{
				Origem: origem, Limite: 50,
			})
			if err != nil {
				t.Fatalf("ListarArquivos(%s): %v", origem, err)
			}
		})
	}

	t.Run("listar por estado", func(t *testing.T) {
		_, err := env.Store.ListarArquivos(ctx, store.FiltroDeArquivos{
			Estado: store.ArquivoPronto, Limite: 50,
		})
		if err != nil {
			t.Fatalf("ListarArquivos(estado): %v", err)
		}
	})

	t.Run("resumo do acervo", func(t *testing.T) {
		if _, err := env.Store.ResumoDoAcervo(ctx); err != nil {
			t.Fatalf("ResumoDoAcervo: %v", err)
		}
	})

	t.Run("listar contas de nuvem", func(t *testing.T) {
		// Esta também faz junção — com a subconsulta que soma o que cada conta guarda.
		if _, err := env.Store.ListarNuvens(ctx); err != nil {
			t.Fatalf("ListarNuvens: %v", err)
		}
	})

	t.Run("candidatos à limpeza", func(t *testing.T) {
		if _, err := env.Store.CandidatosParaLimpeza(ctx, 0, 10); err != nil {
			t.Fatalf("CandidatosParaLimpeza: %v", err)
		}
	})

	t.Run("tomar da fila com a fila vazia", func(t *testing.T) {
		// Fila vazia devolve ErrNotFound, que não é falha: é a resposta "não há trabalho".
		if _, err := env.Store.TomarDaFila(ctx); err != nil && err != store.ErrNotFound {
			t.Fatalf("TomarDaFila: %v", err)
		}
	})

	t.Run("fonte interna do acervo", func(t *testing.T) {
		fonte, err := env.Store.FonteDoAcervo(ctx)
		if err != nil {
			t.Fatalf("FonteDoAcervo: %v", err)
		}
		if fonte.Kind != "proprio" {
			t.Errorf("kind = %q, queria \"proprio\"", fonte.Kind)
		}
		// Chamada duas vezes devolve a MESMA fonte: ela é criada uma vez e reaproveitada.
		// Sem isso, cada envio criaria uma fonte nova e a tela de Fontes encheria.
		outra, err := env.Store.FonteDoAcervo(ctx)
		if err != nil {
			t.Fatalf("FonteDoAcervo (segunda vez): %v", err)
		}
		if outra.ID != fonte.ID {
			t.Errorf("criou uma fonte nova (%d != %d) em vez de reaproveitar", outra.ID, fonte.ID)
		}
	})
}

// TestTituloDeEpisodioTrazASerie: um episódio sozinho não diz de que série é.
//
// "Episódio 3" é uma linha inútil num acervo com centenas deles — vira uma lista de nomes
// repetidos que não ajuda a decidir nada. O acervo mostra "Série · S01E03 · Título".
func TestTituloDeEpisodioTrazASerie(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	serie, err := env.Store.CreateContent(ctx, store.NewContent{
		Type: store.ContentSeries, Title: "Arquivo X", NormalizedTitle: "arquivo x",
	})
	if err != nil {
		t.Fatalf("CreateContent: %v", err)
	}
	temporada, err := env.Store.EnsureSeason(ctx, serie.ID, 1)
	if err != nil {
		t.Fatalf("EnsureSeason: %v", err)
	}
	episodio, err := env.Store.EnsureEpisode(ctx, temporada, 3, "Conduit", "", "", nil)
	if err != nil {
		t.Fatalf("EnsureEpisode: %v", err)
	}

	fonte, err := env.Store.FonteDoAcervo(ctx)
	if err != nil {
		t.Fatalf("FonteDoAcervo: %v", err)
	}
	variante, err := env.Store.CreateVariant(ctx, store.NewVariant{
		SourceID: fonte.ID, TargetKind: "episode", TargetID: episodio,
		ExternalID: "teste:1", OriginURL: "http://exemplo.tld/x.mp4", ContainerExt: "mp4",
	})
	if err != nil {
		t.Fatalf("UpsertVariant: %v", err)
	}

	if _, err := env.Store.EnfileirarArquivo(ctx, store.NovoArquivo{
		VariantID: &variante.ID, TargetKind: "episode", TargetID: episodio,
		Backend: store.BackendLocal, Origem: store.OrigemFonte,
		Localizador: "x.mp4", Bytes: 100,
	}); err != nil {
		t.Fatalf("EnfileirarArquivo: %v", err)
	}

	arquivos, err := env.Store.ListarArquivos(ctx, store.FiltroDeArquivos{Limite: 10})
	if err != nil {
		t.Fatalf("ListarArquivos: %v", err)
	}
	if len(arquivos) != 1 {
		t.Fatalf("listou %d arquivos, queria 1", len(arquivos))
	}

	const quer = "Arquivo X · S01E03 · Conduit"
	if arquivos[0].Titulo != quer {
		t.Errorf("titulo = %q, queria %q", arquivos[0].Titulo, quer)
	}
}

// TestResumoDeFalhasExecuta garante que a consulta do resumo roda de verdade contra o
// Postgres.
//
// Existe porque este projeto já perdeu uma tela inteira para uma consulta que compilava e
// não executava: `id` ambíguo numa junção. Compilar não prova nada sobre SQL — só executar
// prova.
func TestResumoDeFalhasExecuta(t *testing.T) {
	env := newTestEnv(t)

	causas, err := env.Store.ResumoDeFalhas(context.Background())
	if err != nil {
		t.Fatalf("ResumoDeFalhas: %v", err)
	}
	if causas == nil {
		t.Fatal("o resumo precisa ser uma lista vazia, e nunca nula: a tela itera sobre ela")
	}
}
