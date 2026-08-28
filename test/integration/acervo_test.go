package integration

import (
	"context"
	"errors"
	"testing"
	"time"

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

// TestConsultasDeCamadaExecutam roda as consultas do arquivamento contra o Postgres.
//
// Mesma razão das outras: compilar não prova nada sobre SQL. Estas duas mexem em `backend` e
// `nuvem_id`, que têm restrições no banco — e uma combinação errada só aparece na execução.
func TestConsultasDeCamadaExecutam(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	if _, err := env.Store.CandidatoParaArquivar(ctx, 24*time.Hour); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CandidatoParaArquivar: %v", err)
	}
	for _, backend := range []string{store.BackendLocal, store.BackendNuvem} {
		if _, err := env.Store.CandidatoParaLimpeza(ctx, 24*time.Hour, backend); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			t.Fatalf("CandidatoParaLimpeza(%s): %v", backend, err)
		}
	}

	// Sem candidato, mudar de camada tem de recusar em vez de afetar linha nenhuma em
	// silêncio — é o que garante que uma falha de arquivamento não perca o apontamento.
	if err := env.Store.MudarDeCamada(ctx, 999999, 1, "seja-o-que-for", 10); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("MudarDeCamada num id inexistente devia recusar, veio: %v", err)
	}

	if _, err := env.Store.ExplicarLimpeza(ctx, 24*time.Hour); err != nil {
		t.Fatalf("ExplicarLimpeza: %v", err)
	}
	if _, err := env.Store.BytesEmCache(ctx, store.BackendLocal); err != nil {
		t.Fatalf("BytesEmCache: %v", err)
	}
	if _, err := env.Store.TomarParaRemocao(ctx); err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("TomarParaRemocao: %v", err)
	}
	if _, err := env.Store.DestravarCopiasPendentes(ctx); err != nil {
		t.Fatalf("DestravarCopiasPendentes: %v", err)
	}
}

// TestCicloDaContaDeNuvemExecuta percorre o cadastro de uma conta de ponta a ponta.
//
// Este teste nasceu de um defeito que só apareceu em produção, e no pior instante possível:
// o Google já tinha autorizado, e o cadastro falhava com
// "missing FROM-clause entry for table n". A lista de colunas era uma constante única, já
// qualificada com `n.` — o que funciona no SELECT com junção e é inválido no RETURNING de um
// INSERT, que não tem apelido de tabela.
//
// O consentimento do Google se gasta a cada tentativa, então o custo do defeito não era um
// erro na tela: era refazer o fluxo inteiro sem saber por quê.
//
// Compilar não pega isso. Só executar pega.
func TestCicloDaContaDeNuvemExecuta(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	nuvem, err := env.Store.CriarNuvem(ctx, store.NovaNuvem{
		Nome:        "conta-de-teste",
		Provedor:    store.ProvedorGDrive,
		Credenciais: []byte("cifrado-de-mentira"),
	})
	if err != nil {
		t.Fatalf("CriarNuvem: %v", err)
	}
	if nuvem.Nome != "conta-de-teste" {
		t.Fatalf("a conta voltou com o nome errado: %q", nuvem.Nome)
	}

	// As colunas precisam cair nos campos certos. Uma lista lida fora de ordem passaria
	// silenciosamente no INSERT e só apareceria como dado trocado muito depois.
	if nuvem.Provedor != store.ProvedorGDrive || nuvem.Ordem != 100 {
		t.Fatalf("colunas fora de ordem: provedor=%q ordem=%d", nuvem.Provedor, nuvem.Ordem)
	}

	if _, err := env.Store.NuvemPorID(ctx, nuvem.ID); err != nil {
		t.Fatalf("NuvemPorID: %v", err)
	}
	if _, err := env.Store.ListarNuvens(ctx); err != nil {
		t.Fatalf("ListarNuvens: %v", err)
	}
	if _, err := env.Store.NuvemParaGravar(ctx, 0); err != nil {
		t.Fatalf("NuvemParaGravar: %v", err)
	}

	somenteLeitura := true
	if _, err := env.Store.AtualizarNuvem(ctx, nuvem.ID,
		store.AjusteDeNuvem{SomenteLeitura: &somenteLeitura}); err != nil {
		t.Fatalf("AtualizarNuvem: %v", err)
	}
	if err := env.Store.RemoverNuvem(ctx, nuvem.ID); err != nil {
		t.Fatalf("RemoverNuvem: %v", err)
	}
}

// TestProximoEpisodioAtravessaTemporadas cobre a consulta que faz o adiantamento existir.
//
// Ela ficou QUEBRADA em produção sem que ninguém percebesse, e o motivo importa: a coluna é
// `series_content_id`, e eu escrevi `content_id`. O Postgres recusava a consulta a cada
// reprodução de série, e `TalvezAdiantarProximo` engolia o erro como um aviso no log — então
// o recurso simplesmente não acontecia, sem nada quebrar.
//
// É o padrão que mais custou tempo neste projeto: consulta que compila, falha ao executar, e
// tem o erro tratado como "não deu, siga em frente".
//
// O caso da virada de temporada é o que mais se esquece de testar, e é justamente o que uma
// implementação ingênua — "mesma temporada, episódio + 1" — erra. Ele só apareceria no fim
// de cada temporada, que é quando ninguém está olhando.
func TestProximoEpisodioAtravessaTemporadas(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	serie, err := env.Store.CreateContent(ctx, store.NewContent{
		Type: store.ContentSeries, Title: "Série de Teste", NormalizedTitle: "serie de teste",
	})
	if err != nil {
		t.Fatalf("CreateContent: %v", err)
	}

	t1, err := env.Store.EnsureSeason(ctx, serie.ID, 1)
	if err != nil {
		t.Fatalf("EnsureSeason 1: %v", err)
	}
	t2, err := env.Store.EnsureSeason(ctx, serie.ID, 2)
	if err != nil {
		t.Fatalf("EnsureSeason 2: %v", err)
	}

	ep20, err := env.Store.EnsureEpisode(ctx, t1, 20, "Vinte", "", "", nil)
	if err != nil {
		t.Fatalf("EnsureEpisode 20: %v", err)
	}
	ep21, err := env.Store.EnsureEpisode(ctx, t1, 21, "Vinte e um", "", "", nil)
	if err != nil {
		t.Fatalf("EnsureEpisode 21: %v", err)
	}
	t2e1, err := env.Store.EnsureEpisode(ctx, t2, 1, "Nova temporada", "", "", nil)
	if err != nil {
		t.Fatalf("EnsureEpisode T2E1: %v", err)
	}

	// O caso comum: dentro da mesma temporada.
	proximo, err := env.Store.ProximoEpisodio(ctx, ep20)
	if err != nil {
		t.Fatalf("ProximoEpisodio(20): %v", err)
	}
	if proximo != ep21 {
		t.Fatalf("depois do 20 vem o 21; veio %d", proximo)
	}

	// O caso esquecido: o último da temporada leva ao primeiro da seguinte.
	proximo, err = env.Store.ProximoEpisodio(ctx, ep21)
	if err != nil {
		t.Fatalf("ProximoEpisodio(21): %v", err)
	}
	if proximo != t2e1 {
		t.Fatalf("depois do último da T1 vem o T2E1; veio %d", proximo)
	}

	// O fim da série não é erro: é o fim.
	if _, err := env.Store.ProximoEpisodio(ctx, t2e1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("depois do último episódio não há próximo; veio: %v", err)
	}
}

// TestVariantesDoAlvoExecuta cobre a consulta que dá failover ao baixador.
//
// Sem ela o baixador conhece uma origem só, e o efeito apareceu em produção: uma fonte
// respondeu 403 ao robô e o episódio ficou com erro para sempre — mesmo com outra fonte, na
// mesma lista, servindo o filme inteiro sem reclamar.
func TestVariantesDoAlvoExecuta(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	for _, kind := range []string{store.TargetContent, store.TargetEpisode} {
		if _, err := env.Store.VariantesDoAlvo(ctx, kind, 1); err != nil {
			t.Fatalf("VariantesDoAlvo(%s): %v", kind, err)
		}
	}
}
