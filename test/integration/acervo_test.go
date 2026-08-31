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

	// O ciclo do esvaziamento: marcar, achar a conta marcada, procurar arquivo, desmarcar.
	// A restricao do banco recusa esvaziar sem somente_leitura, entao a ordem importa.
	if _, err := env.Store.EsvaziarNuvem(ctx, nuvem.ID, true); err != nil {
		t.Fatalf("EsvaziarNuvem: %v", err)
	}
	marcada, err := env.Store.NuvemEsvaziando(ctx)
	if err != nil {
		t.Fatalf("NuvemEsvaziando: %v", err)
	}
	if marcada.ID != nuvem.ID || !marcada.Esvaziando || !marcada.SomenteLeitura {
		t.Fatalf("esvaziar precisa ligar somente_leitura junto: %+v", marcada)
	}
	if _, err := env.Store.ArquivoParaMudarDeConta(ctx, nuvem.ID); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ArquivoParaMudarDeConta: %v", err)
	}
	if err := env.Store.TrazerParaODisco(ctx, 999999, "x", 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("TrazerParaODisco num id inexistente devia recusar, veio: %v", err)
	}
	if _, err := env.Store.EsvaziarNuvem(ctx, nuvem.ID, false); err != nil {
		t.Fatalf("EsvaziarNuvem(false): %v", err)
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

// TestCopiaComFalhaVoltaParaAFila cobre a decisão entre "tenta de novo" e "desiste".
//
// A causa mais comum de falha numa fonte de IPTV é temporária: "403 Forbidden" quase nunca
// significa "você não tem direito a este filme", e sim "você pediu demais nesta hora". Antes
// disto, a primeira falha aposentava o título para sempre.
//
// O limite existe pelo motivo oposto: um link morto seria retentado para sempre, uma conexão
// a cada ciclo, por meses, para nunca funcionar.
func TestCopiaComFalhaVoltaParaAFila(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte, err := env.Store.FonteDoAcervo(ctx)
	if err != nil {
		t.Fatalf("FonteDoAcervo: %v", err)
	}
	filme, err := env.Store.CreateContent(ctx, store.NewContent{
		Type: store.ContentMovie, Title: "Teste de Retentativa", NormalizedTitle: "teste de retentativa",
	})
	if err != nil {
		t.Fatalf("CreateContent: %v", err)
	}
	variante, err := env.Store.CreateVariant(ctx, store.NewVariant{
		SourceID: fonte.ID, TargetKind: "content", TargetID: filme.ID,
		ExternalID: "retentativa:1", OriginURL: "http://exemplo.tld/f.mp4", ContainerExt: "mp4",
	})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	arquivo, err := env.Store.EnfileirarArquivo(ctx, store.NovoArquivo{
		VariantID: &variante.ID, TargetKind: "content", TargetID: filme.ID,
		Backend: store.BackendLocal, Origem: store.OrigemFonte,
	})
	if err != nil {
		t.Fatalf("EnfileirarArquivo: %v", err)
	}

	// As primeiras falhas devolvem à fila, com espera.
	for i := 1; i < store.MaxTentativasDeCopia; i++ {
		if err := env.Store.FalharArquivo(ctx, arquivo.ID, "a fonte respondeu 403 Forbidden"); err != nil {
			t.Fatalf("FalharArquivo %d: %v", i, err)
		}
		atual, err := env.Store.ArquivoPorID(ctx, arquivo.ID)
		if err != nil {
			t.Fatalf("ArquivoPorID: %v", err)
		}
		if atual.Estado != store.ArquivoPendente {
			t.Fatalf("falha %d de %d devia voltar à fila, e o estado é %q",
				i, store.MaxTentativasDeCopia, atual.Estado)
		}
		// Em espera, a fila não pode enxergá-la — senão o baixador repetiria na hora o
		// pedido que a fonte acabou de recusar.
		if _, err := env.Store.TomarDaFila(ctx); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("uma cópia em espera não pode ser tomada da fila; veio: %v", err)
		}
	}

	// A última esgota o limite e vira erro definitivo.
	if err := env.Store.FalharArquivo(ctx, arquivo.ID, "a fonte respondeu 403 Forbidden"); err != nil {
		t.Fatalf("FalharArquivo final: %v", err)
	}
	atual, err := env.Store.ArquivoPorID(ctx, arquivo.ID)
	if err != nil {
		t.Fatalf("ArquivoPorID: %v", err)
	}
	if atual.Estado != store.ArquivoErro {
		t.Fatalf("esgotadas as tentativas, o estado devia ser erro; é %q", atual.Estado)
	}

	// "Tentar de novo" zera a contagem: quem clica está dizendo que a causa mudou.
	if err := env.Store.ReenfileirarArquivo(ctx, arquivo.ID); err != nil {
		t.Fatalf("ReenfileirarArquivo: %v", err)
	}
	if _, err := env.Store.TomarDaFila(ctx); err != nil {
		t.Fatalf("depois de reenfileirar, a fila devia entregá-la: %v", err)
	}
}

// TestCopiaPequenaDemaisNaoEServida cobre o piso de plausibilidade.
//
// O defeito que ele impede: uma fonte responde 200 com uma página de erro de dois
// quilobytes e SEM Content-Length. Todas as conferências de tamanho do baixador dependiam
// desse cabeçalho, então a página virava uma cópia "pronta" — e o acervo passava a servi-la
// no lugar do filme, que na fonte estava inteiro.
//
// Na tela: 1,6 KB, verde, "pronto", com acessos contados. Para o cliente: não abre.
func TestCopiaPequenaDemaisNaoEServida(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte, err := env.Store.FonteDoAcervo(ctx)
	if err != nil {
		t.Fatalf("FonteDoAcervo: %v", err)
	}
	filme, err := env.Store.CreateContent(ctx, store.NewContent{
		Type: store.ContentMovie, Title: "Pagina de Erro", NormalizedTitle: "pagina de erro",
	})
	if err != nil {
		t.Fatalf("CreateContent: %v", err)
	}
	variante, err := env.Store.CreateVariant(ctx, store.NewVariant{
		SourceID: fonte.ID, TargetKind: "content", TargetID: filme.ID,
		ExternalID: "piso:1", OriginURL: "http://exemplo.tld/e.mp4", ContainerExt: "mp4",
	})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}

	// 1,6 KB gravados como prontos, sem tamanho anunciado — exatamente o caso real.
	if _, err := env.Store.EnfileirarArquivo(ctx, store.NovoArquivo{
		VariantID: &variante.ID, TargetKind: "content", TargetID: filme.ID,
		Backend: store.BackendLocal, Origem: store.OrigemFonte,
		Localizador: "erro.mp4", Bytes: 1600,
	}); err != nil {
		t.Fatalf("EnfileirarArquivo: %v", err)
	}

	if _, err := env.Store.ArquivoProntoDaVariante(ctx, variante.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("uma cópia de 1,6 KB não pode ser servida como filme; veio: %v", err)
	}

	// E a varredura tem de alcançá-la, ou ela ocuparia a variante para sempre — impedindo
	// que o título fosse copiado de novo.
	n, err := env.Store.MarcarCopiasTruncadas(ctx)
	if err != nil {
		t.Fatalf("MarcarCopiasTruncadas: %v", err)
	}
	if n == 0 {
		t.Fatal("a varredura não alcançou a cópia pequena demais")
	}
}

// TestConcluirRecusaCopiaImplausivel prende o piso no lugar onde ele não pode ser contornado.
//
// Este defeito voltou DUAS vezes, e as duas por caminhos diferentes:
//
//   - o baixador comparava com o Content-Length, e a fonte podia não enviá-lo;
//   - a captura comparava com o tamanho ANUNCIADO — e uma página de erro de 1,6 KB que
//     anuncia 1600 bytes bate certinho com o que prometeu.
//
// Cada correção arrumava um caminho e deixava o outro. Por isso a regra desceu para
// ConcluirArquivo, por onde toda cópia obrigatoriamente passa para virar acervo: um caminho
// novo não tem como esquecer de aplicá-la.
func TestConcluirRecusaCopiaImplausivel(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte, err := env.Store.FonteDoAcervo(ctx)
	if err != nil {
		t.Fatalf("FonteDoAcervo: %v", err)
	}
	criar := func(titulo, ext string, origem string) int64 {
		c, err := env.Store.CreateContent(ctx, store.NewContent{
			Type: store.ContentMovie, Title: titulo, NormalizedTitle: titulo,
		})
		if err != nil {
			t.Fatalf("CreateContent: %v", err)
		}
		v, err := env.Store.CreateVariant(ctx, store.NewVariant{
			SourceID: fonte.ID, TargetKind: "content", TargetID: c.ID,
			ExternalID: ext, OriginURL: "http://exemplo.tld/x.mp4", ContainerExt: "mp4",
		})
		if err != nil {
			t.Fatalf("CreateVariant: %v", err)
		}
		a, err := env.Store.EnfileirarArquivo(ctx, store.NovoArquivo{
			VariantID: &v.ID, TargetKind: "content", TargetID: c.ID,
			Backend: store.BackendLocal, Origem: origem,
		})
		if err != nil {
			t.Fatalf("EnfileirarArquivo: %v", err)
		}
		return a.ID
	}

	// Cache de 1,6 KB: recusado, com erro identificável para quem chamou poder apagar o
	// arquivo que gravou.
	cache := criar("erro do cache", "piso-cache", store.OrigemFonte)
	err = env.Store.ConcluirArquivo(ctx, cache, "erro.mp4", 1600)
	if !errors.Is(err, store.ErrCopiaImplausivel) {
		t.Fatalf("concluir 1,6 KB de cache devia recusar com ErrCopiaImplausivel; veio: %v", err)
	}
	atual, err := env.Store.ArquivoPorID(ctx, cache)
	if err != nil {
		t.Fatalf("ArquivoPorID: %v", err)
	}
	if atual.Estado == store.ArquivoPronto {
		t.Fatal("a linha não pode ter ficado pronta depois da recusa")
	}

	// Acervo próprio do mesmo tamanho: aceito. Um arquivo pequeno que alguém enviou pelo
	// painel é um arquivo pequeno que alguém quis enviar.
	proprio := criar("envio pequeno", "piso-proprio", store.OrigemProprio)
	if err := env.Store.ConcluirArquivo(ctx, proprio, "meu.mp4", 1600); err != nil {
		t.Fatalf("o piso não pode alcançar o acervo próprio: %v", err)
	}
}

// TestEstimarArmazenamentoExecuta roda a consulta da estimativa contra o Postgres.
//
// Ela junta arquivos_guardados, source_variants e sources, e usa CTEs — o tipo de consulta
// que compila em Go e o Postgres recusa. É a mesma razão dos outros testes deste arquivo: só
// executar prova.
func TestEstimarArmazenamentoExecuta(t *testing.T) {
	env := newTestEnv(t)

	fontes, err := env.Store.EstimarArmazenamento(context.Background())
	if err != nil {
		t.Fatalf("EstimarArmazenamento: %v", err)
	}
	if fontes == nil {
		t.Fatal("a estimativa precisa ser uma lista vazia, e nunca nula: a tela itera sobre ela")
	}
}

// TestApelidosSobrevivemAoApagarAFonte confirma o que o schema já garante.
//
// A preocupação é legítima e a resposta é boa: `categories` é a tabela CANÔNICA — as pastas
// que você organiza — e não pertence a fonte nenhuma. Quem morre com a fonte é
// `source_categories`, a categoria como aquela fonte a declara.
//
// Então apagar todas as fontes leva embora as declarações e preserva a organização: as
// pastas e os apelidos ficam, e uma fonte recadastrada cai neles automaticamente.
//
// Este teste existe porque a distinção é invisível de fora, e alguém — inclusive eu — vai
// olhar o CASCADE de `source_categories` de novo um dia e concluir o contrário.
func TestApelidosSobrevivemAoApagarAFonte(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte, err := env.Store.FonteDoAcervo(ctx)
	if err != nil {
		t.Fatalf("FonteDoAcervo: %v", err)
	}

	var destinoID int64
	if err := env.Pool.QueryRow(ctx, `
		INSERT INTO categories (name, normalized_name, content_type)
		VALUES ('Novelas', 'novelas', 'movie') RETURNING id`).Scan(&destinoID); err != nil {
		t.Fatalf("criando a pasta canônica: %v", err)
	}
	if _, err := env.Pool.Exec(ctx, `
		INSERT INTO source_categories
			(source_id, external_id, declared_name, normalized_name, content_type, category_id)
		VALUES ($1, 'v44', 'Vídeos 44', 'videos 44', 'movie', $2)`,
		fonte.ID, destinoID); err != nil {
		t.Fatalf("criando a categoria da fonte: %v", err)
	}
	if _, err := env.Pool.Exec(ctx, `
		INSERT INTO category_aliases
			(normalized_name, content_type, category_id, declared_name, origem)
		VALUES ('videos 44', 'movie', $1, 'Vídeos 44', 'uniao')`, destinoID); err != nil {
		t.Fatalf("criando o apelido: %v", err)
	}

	if _, err := env.Pool.Exec(ctx, `DELETE FROM sources WHERE id = $1`, fonte.ID); err != nil {
		t.Fatalf("apagando a fonte: %v", err)
	}

	// A pasta e o apelido continuam de pé, e o apelido continua resolvendo.
	idx, err := env.Store.ApelidosCategoria(ctx)
	if err != nil {
		t.Fatalf("ApelidosCategoria: %v", err)
	}
	if idx[store.ChaveCategoria("videos 44", "movie")] != destinoID {
		t.Fatal("o apelido não sobreviveu: a organização feita à mão se perderia ao " +
			"recadastrar uma fonte")
	}

	// A declaração da fonte, essa sim, foi embora junto — é dela, e sem a fonte não
	// significa nada.
	var declaracoes int64
	if err := env.Pool.QueryRow(ctx,
		`SELECT count(*) FROM source_categories WHERE normalized_name = 'videos 44'`).
		Scan(&declaracoes); err != nil {
		t.Fatalf("contando declarações: %v", err)
	}
	if declaracoes != 0 {
		t.Fatalf("a categoria declarada pela fonte devia ter ido junto; sobraram %d", declaracoes)
	}
}

// TestBuscaDeCopiaRespeitaAPrioridade cobre a busca em lote que substituiu N consultas.
//
// O proxy perguntava por uma variante de cada vez até achar — quatro idas ao banco antes do
// primeiro byte, e as três primeiras normalmente sem encontrar nada. Agora é uma consulta só.
//
// O que a troca NÃO pode perder é a ordem: as variantes chegam por prioridade de reprodução,
// e a cópia da fonte preferida tem de ganhar da cópia de uma fonte de reserva. Um `IN` no SQL
// não devolve nada ordenado, então a ordem é reimposta em Go — e é isso que este teste prende.
func TestBuscaDeCopiaRespeitaAPrioridade(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	fonte, err := env.Store.FonteDoAcervo(ctx)
	if err != nil {
		t.Fatalf("FonteDoAcervo: %v", err)
	}
	filme, err := env.Store.CreateContent(ctx, store.NewContent{
		Type: store.ContentMovie, Title: "Duas Fontes", NormalizedTitle: "duas fontes",
	})
	if err != nil {
		t.Fatalf("CreateContent: %v", err)
	}

	criarCopia := func(ext string, bytes int64) int64 {
		v, err := env.Store.CreateVariant(ctx, store.NewVariant{
			SourceID: fonte.ID, TargetKind: "content", TargetID: filme.ID,
			ExternalID: ext, OriginURL: "http://exemplo.tld/" + ext, ContainerExt: "mp4",
		})
		if err != nil {
			t.Fatalf("CreateVariant: %v", err)
		}
		if _, err := env.Store.EnfileirarArquivo(ctx, store.NovoArquivo{
			VariantID: &v.ID, TargetKind: "content", TargetID: filme.ID,
			Backend: store.BackendLocal, Origem: store.OrigemFonte,
			Localizador: ext + ".mp4", Bytes: bytes,
		}); err != nil {
			t.Fatalf("EnfileirarArquivo: %v", err)
		}
		return v.ID
	}

	preferida := criarCopia("preferida", 900<<20)
	reserva := criarCopia("reserva", 800<<20)

	// A preferida vem primeiro na lista: é ela que deve ganhar.
	a, err := env.Store.ArquivoProntoDeAlguma(ctx, []int64{preferida, reserva})
	if err != nil {
		t.Fatalf("ArquivoProntoDeAlguma: %v", err)
	}
	if a.VariantID == nil || *a.VariantID != preferida {
		t.Fatal("a cópia da fonte preferida devia ganhar; a ordem da lista foi ignorada")
	}

	// Invertendo a ordem, a resposta acompanha — provando que é a lista que manda, e não
	// a ordem em que o banco resolveu devolver as linhas.
	a, err = env.Store.ArquivoProntoDeAlguma(ctx, []int64{reserva, preferida})
	if err != nil {
		t.Fatalf("ArquivoProntoDeAlguma invertido: %v", err)
	}
	if a.VariantID == nil || *a.VariantID != reserva {
		t.Fatal("a ordem da lista precisa mandar, e não a ordem do banco")
	}

	// Sem nenhuma cópia, ausência — e não erro: é o caminho normal do cache vazio.
	if _, err := env.Store.ArquivoProntoDeAlguma(ctx, []int64{999998, 999999}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sem cópia devia ser ErrNotFound; veio: %v", err)
	}
}

// TestUmaVariantePorFonte prende a regra que faz o failover ter sentido.
//
// Uma fonte declara o mesmo filme em várias pastas — romance, juvenil, drama — e cada
// declaração virava uma variante. Todas apontam para o MESMO servidor.
//
// Isso não é redundância: é o oposto. Se o servidor cai, as sete caem juntas, e o failover —
// que tenta três origens antes de desistir — gasta as três na mesma fonte morta sem nunca
// chegar na fonte que estava funcionando. O espectador espera três vezes o tempo de espera
// para receber um erro que a segunda fonte não daria.
func TestUmaVariantePorFonte(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Uma fonte de verdade, e nao a interna do acervo proprio: aquela nasce desabilitada,
	// e a consulta de reproducao ignora fonte desabilitada — como deve.
	var fonteID int64
	if err := env.Pool.QueryRow(ctx, `
		INSERT INTO sources (name, kind, base_url, priority, enabled)
		VALUES ('fonte de teste', 'm3u', 'http://exemplo.tld', 1, true)
		RETURNING id`).Scan(&fonteID); err != nil {
		t.Fatalf("criando a fonte: %v", err)
	}
	filme, err := env.Store.CreateContent(ctx, store.NewContent{
		Type: store.ContentMovie, Title: "Hipotese do Amor", NormalizedTitle: "hipotese do amor",
	})
	if err != nil {
		t.Fatalf("CreateContent: %v", err)
	}

	// O mesmo filme, na mesma fonte, em três pastas.
	for _, pasta := range []string{"romance", "juvenil", "drama"} {
		if _, err := env.Store.CreateVariant(ctx, store.NewVariant{
			SourceID: fonteID, TargetKind: "content", TargetID: filme.ID,
			ExternalID: "hip-" + pasta, OriginURL: "http://exemplo.tld/" + pasta + ".mp4",
			ContainerExt: "mp4",
		}); err != nil {
			t.Fatalf("CreateVariant(%s): %v", pasta, err)
		}
	}

	_, variantes, err := env.Store.ResolveContentForStream(ctx, filme.ID)
	if err != nil {
		t.Fatalf("ResolveContentForStream: %v", err)
	}
	if len(variantes) != 1 {
		t.Fatalf("três pastas da MESMA fonte deviam dar uma origem só; vieram %d — o "+
			"failover gastaria as tentativas todas no mesmo servidor", len(variantes))
	}
	if variantes[0].SourceID != fonteID {
		t.Fatalf("a origem devia ser da fonte cadastrada; veio %d", variantes[0].SourceID)
	}
}

// TestSessaoNasceComOResultadoDoCache prende a economia de uma ida ao banco.
//
// Eram duas escritas na MESMA linha, uma atrás da outra, no caminho do primeiro byte: o
// INSERT da sessão e um UPDATE logo depois só para dizer de onde o vídeo saiu. Servindo do
// disco isso pesava — a leitura do arquivo custa microssegundos, e cada escrita no banco
// custa milissegundos.
//
// Vazio continua virando 'passthrough', que é o padrão da coluna: uma sessão que não sabe de
// onde saiu não pode nascer mentindo que saiu do cache.
func TestSessaoNasceComOResultadoDoCache(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	abrir := func(resultado string) string {
		id, err := env.Store.OpenStream(ctx, store.NewStream{
			NodeID: "teste", ClientIP: "127.0.0.1", CacheResult: resultado,
		})
		if err != nil {
			t.Fatalf("OpenStream(%q): %v", resultado, err)
		}
		var lido string
		if err := env.Pool.QueryRow(ctx,
			`SELECT cache_result FROM streams WHERE id = $1`, id).Scan(&lido); err != nil {
			t.Fatalf("lendo cache_result: %v", err)
		}
		return lido
	}

	if got := abrir("hit"); got != "hit" {
		t.Fatalf("a sessão devia nascer como 'hit'; veio %q", got)
	}
	if got := abrir(""); got != "passthrough" {
		t.Fatalf("sem resultado, o padrão é 'passthrough'; veio %q", got)
	}
}

// TestPrioridadeDaFonteERespeitada responde a uma desconfiança específica: "a turbo é usada
// mais que a love, mesmo com prioridade pior".
//
// A ordem de reprodução é: escolha manual do administrador, depois prioridade da fonte,
// depois a mais antiga. Este teste prova a segunda parte — e o cuidado está em criar a fonte
// de PIOR prioridade primeiro, com id menor: se a consulta estivesse ordenando por id, ela
// passaria por acidente e o teste não valeria nada.
func TestPrioridadeDaFonteERespeitada(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	criarFonte := func(nome string, prioridade int) int64 {
		var id int64
		if err := env.Pool.QueryRow(ctx, `
			INSERT INTO sources (name, kind, base_url, priority, enabled)
			VALUES ($1, 'm3u', 'http://exemplo.tld', $2, true) RETURNING id`,
			nome, prioridade).Scan(&id); err != nil {
			t.Fatalf("criando a fonte %s: %v", nome, err)
		}
		return id
	}

	// A pior primeiro, de propósito: id menor, prioridade maior (pior).
	turbo := criarFonte("turbo", 9)
	love := criarFonte("love", 1)

	filme, err := env.Store.CreateContent(ctx, store.NewContent{
		Type: store.ContentMovie, Title: "Prioridade", NormalizedTitle: "prioridade",
	})
	if err != nil {
		t.Fatalf("CreateContent: %v", err)
	}
	for _, f := range []struct {
		id   int64
		nome string
	}{{turbo, "turbo"}, {love, "love"}} {
		if _, err := env.Store.CreateVariant(ctx, store.NewVariant{
			SourceID: f.id, TargetKind: "content", TargetID: filme.ID,
			ExternalID: "prio-" + f.nome, OriginURL: "http://exemplo.tld/" + f.nome,
			ContainerExt: "mp4",
		}); err != nil {
			t.Fatalf("CreateVariant(%s): %v", f.nome, err)
		}
	}

	_, variantes, err := env.Store.ResolveContentForStream(ctx, filme.ID)
	if err != nil {
		t.Fatalf("ResolveContentForStream: %v", err)
	}
	if len(variantes) != 2 {
		t.Fatalf("duas fontes, duas origens; vieram %d", len(variantes))
	}
	if variantes[0].SourceID != love {
		t.Fatalf("a fonte de melhor prioridade devia vir primeiro; veio %q",
			variantes[0].SourceName)
	}
	if variantes[1].SourceID != turbo {
		t.Fatalf("a de pior prioridade devia vir depois; veio %q", variantes[1].SourceName)
	}
}

// TestPodaDoHistoricoPreservaOAtivo cobre a faxina que faltava.
//
// A tabela de reproduções cresce e nunca encolhia: três mil por dia são um milhão de linhas
// por ano, mais sete índices sobre elas. Nada quebra de repente — o painel só fica mais lento
// a cada mês, e o disco some sem que ninguém associe as duas coisas.
//
// A regra que a poda não pode violar: uma sessão ATIVA nunca é apagada, por mais antiga que
// pareça. Ativa significa vaga ocupada no limite da credencial — apagá-la faria o contador
// nunca fechar, e o cliente ficaria bloqueado por uma reprodução que não existe mais.
func TestPodaDoHistoricoPreservaOAtivo(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	criar := func(estado string, idade time.Duration) int64 {
		id, err := env.Store.OpenStream(ctx, store.NewStream{NodeID: "teste", ClientIP: "1.2.3.4"})
		if err != nil {
			t.Fatalf("OpenStream: %v", err)
		}
		if _, err := env.Pool.Exec(ctx,
			`UPDATE streams SET started_at = now() - $2::interval, state = $3 WHERE id = $1`,
			id, idade.String(), estado); err != nil {
			t.Fatalf("envelhecendo a sessão: %v", err)
		}
		return id
	}

	antigaFechada := criar("closed", 100*24*time.Hour)
	antigaAtiva := criar("active", 100*24*time.Hour)
	recente := criar("closed", 2*24*time.Hour)

	n, err := env.Store.PodarHistoricoDeStreams(ctx, 90*24*time.Hour, 1000)
	if err != nil {
		t.Fatalf("PodarHistoricoDeStreams: %v", err)
	}
	if n != 1 {
		t.Fatalf("só a antiga FECHADA devia sair; saíram %d", n)
	}

	existe := func(id int64) bool {
		var n int
		if err := env.Pool.QueryRow(ctx,
			`SELECT count(*) FROM streams WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatalf("conferindo a sessão: %v", err)
		}
		return n == 1
	}
	if existe(antigaFechada) {
		t.Fatal("a sessão antiga e fechada devia ter sido podada")
	}
	if !existe(antigaAtiva) {
		t.Fatal("uma sessão ATIVA não pode ser podada: ela ocupa vaga no limite da credencial")
	}
	if !existe(recente) {
		t.Fatal("a sessão recente está dentro da retenção e devia continuar")
	}

	// Retenção zero é uma escolha legítima — guardar tudo — e não pode virar "apague tudo".
	if n, err := env.Store.PodarHistoricoDeStreams(ctx, 0, 1000); err != nil || n != 0 {
		t.Fatalf("retenção zero devia não apagar nada; veio %d, %v", n, err)
	}
}

// TestFilaDaClassificacaoExecuta cobre as consultas da classificação por gênero.
//
// Só entram títulos SEM pasta, e a classificação nunca sobrepõe uma decisão humana: quem
// organizou à mão organizou por um motivo, e um robô que passa por cima disso é pior que um
// robô que não passa.
func TestFilaDaClassificacaoExecuta(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	semPasta, err := env.Store.CreateContent(ctx, store.NewContent{
		Type: store.ContentMovie, Title: "Sem Pasta", NormalizedTitle: "sem pasta",
	})
	if err != nil {
		t.Fatalf("CreateContent: %v", err)
	}
	comPasta, err := env.Store.CreateContent(ctx, store.NewContent{
		Type: store.ContentMovie, Title: "Com Pasta", NormalizedTitle: "com pasta",
	})
	if err != nil {
		t.Fatalf("CreateContent: %v", err)
	}

	pasta, err := env.Store.CriarPrincipal(ctx, "Ação", "acao", store.ContentMovie)
	if err != nil {
		t.Fatalf("CriarPrincipal: %v", err)
	}
	if err := env.Store.DefinirCategoriaDoConteudo(ctx, comPasta.ID, pasta); err != nil {
		t.Fatalf("DefinirCategoriaDoConteudo: %v", err)
	}

	fila, err := env.Store.ConteudosSemCategoria(ctx, store.ContentMovie, 100)
	if err != nil {
		t.Fatalf("ConteudosSemCategoria: %v", err)
	}
	if len(fila) != 1 || fila[0].ID != semPasta.ID {
		t.Fatalf("a fila devia ter só o título sem pasta; veio %d item(ns)", len(fila))
	}

	filmes, _, err := env.Store.ContarSemCategoria(ctx)
	if err != nil {
		t.Fatalf("ContarSemCategoria: %v", err)
	}
	if filmes != 1 {
		t.Fatalf("a contagem devia ser 1; veio %d", filmes)
	}

	// A regra que protege o trabalho humano: um título que JÁ tem pasta não muda de lugar.
	outra, err := env.Store.CriarPrincipal(ctx, "Comédia", "comedia", store.ContentMovie)
	if err != nil {
		t.Fatalf("CriarPrincipal: %v", err)
	}
	if err := env.Store.DefinirCategoriaDoConteudo(ctx, comPasta.ID, outra); err != nil {
		t.Fatalf("DefinirCategoriaDoConteudo: %v", err)
	}
	var atual int64
	if err := env.Pool.QueryRow(ctx,
		`SELECT category_id FROM contents WHERE id = $1`, comPasta.ID).Scan(&atual); err != nil {
		t.Fatalf("lendo a categoria: %v", err)
	}
	if atual != pasta {
		t.Fatal("a classificação sobrepôs uma pasta já definida — decisão humana foi perdida")
	}
}
