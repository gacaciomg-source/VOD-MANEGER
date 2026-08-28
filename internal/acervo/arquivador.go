package acervo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"vodmanager/internal/armazenamento"
	"vodmanager/internal/store"
)

// Arquivar: mover o frio do disco para a nuvem.
//
// # As duas camadas
//
// O disco desta máquina é rápido e pequeno. A nuvem é lenta e grande. Nenhuma das duas
// resolve sozinha — e a divisão entre elas não é por tipo de conteúdo, é por TEMPERATURA.
//
// O que está sendo assistido agora fica no disco, onde a leitura custa milissegundos. O que
// ninguém pede há dias vai para a nuvem, onde a lentidão não incomoda ninguém justamente
// porque ninguém está esperando.
//
// # Por que isso também protege a nuvem
//
// O Google limita quanto um MESMO arquivo pode ser baixado, e bloqueia quando passa. Um
// cache inteiro na nuvem entrega os títulos populares de lá — que são exatamente os que
// estouram o limite primeiro, e cujo bloqueio dói mais.
//
// Mandando só o frio, o arquivo popular nunca chega lá. O limite deixa de ser um risco,
// porque o que está na nuvem é, por definição, o que quase ninguém pede.
//
// # O que o disco pequeno vira
//
// Área de passagem, e não acervo. Um conteúdo novo desce da fonte para o disco, é servido
// rápido enquanto está quente, e desce para a nuvem quando esfria. O disco nunca precisa
// caber tudo — só o que está em uso.
//
// # A ordem que não pode ser invertida
//
// Grava na nuvem, muda o apontamento, apaga do disco. Qualquer outra ordem cria uma janela
// em que o banco diz "pronto" e o arquivo não está em lugar nenhum — e o espectador recebe
// erro no lugar do filme. Duplicado nas duas pontas por um instante é o preço, e é barato.

func (b *Baixador) moverParaNuvem(ctx context.Context, arquivo *store.ArquivoGuardado,
	disco armazenamento.Backend) error {

	s := b.servico
	nuvem, err := s.store.NuvemParaGravar(ctx, arquivo.Bytes)
	if err != nil {
		return fmt.Errorf("nenhuma conta de nuvem pode receber agora: %w", err)
	}
	destino, err := s.BackendDaNuvem(ctx, nuvem.ID)
	if err != nil {
		return err
	}

	origem, err := disco.Abrir(ctx, arquivo.Localizador, 0)
	if err != nil {
		return fmt.Errorf("abrindo a cópia local: %w", err)
	}
	defer origem.Close()

	local, err := destino.Guardar(ctx, arquivo.Localizador, origem, arquivo.Bytes)
	if err != nil {
		return fmt.Errorf("gravando na nuvem: %w", err)
	}

	// Meio arquivo na nuvem seria pior que nenhum: ele passaria a ser servido no lugar do
	// bom, e o corte apareceria toda vez. Descarta e mantém o local.
	if local.Bytes != arquivo.Bytes {
		_ = destino.Apagar(context.Background(), local.Localizador)
		return fmt.Errorf("a nuvem recebeu %d de %d bytes; a cópia foi descartada",
			local.Bytes, arquivo.Bytes)
	}

	// A partir daqui o arquivo existe nos DOIS lugares. Apontar para a nuvem antes de
	// apagar do disco é o que garante que nunca haja um instante sem nenhuma cópia.
	if err := s.store.MudarDeCamada(ctx, arquivo.ID, nuvem.ID, local.Localizador, local.Bytes); err != nil {
		_ = destino.Apagar(context.Background(), local.Localizador)
		return fmt.Errorf("apontando para a nuvem: %w", err)
	}

	if err := disco.Apagar(context.Background(), arquivo.Localizador); err != nil {
		// O arquivo já está servindo da nuvem; isto é espaço não liberado, não uma falha
		// de entrega. Vira aviso e não erro porque não há nada a desfazer.
		b.log.Warn("cópia arquivada, mas o arquivo local permaneceu",
			"arquivo_id", arquivo.ID, "localizador", arquivo.Localizador, "erro", err)
	}

	b.log.Info("cópia arquivada na nuvem — o disco local foi liberado",
		"arquivo_id", arquivo.ID, "conta", nuvem.Nome,
		"bytes", local.Bytes, "acessos", arquivo.Acessos)
	return nil
}

// liberarEspaco é a resposta do sistema a um armazenamento apertado.
//
// # A escada, em ordem de perda
//
//  1. Mover o frio do disco para a nuvem. Nada se perde: o filme continua no acervo, só
//     mais longe.
//  2. Sem nuvem — ou com a nuvem cheia —, apagar o mais frio do disco. Perde-se a cópia, e
//     o custo é uma releitura da fonte se alguém pedir de novo.
//  3. Nuvem apertada: apagar o mais frio de lá também. Espaço de nuvem também acaba, e um
//     acervo que só cresce vira conta que só sobe.
//
// A ordem é essa porque a perda é crescente. Apagar antes de tentar mover jogaria fora um
// filme que tinha para onde ir.
//
// # O que nunca entra nesta escada
//
// O acervo próprio. Ele não existe em lugar nenhum além desta instalação, então apagá-lo é
// perda definitiva — e essa decisão não é de um processo de fundo às três da manhã. Quando
// faltar espaço e só restar ele, o sistema para de guardar e a pergunta aparece no painel.
//
// Devolve true quando fez alguma coisa.
func (b *Baixador) liberarEspaco(ctx context.Context) bool {
	s := b.servico
	pol := s.PoliticaAtual(ctx)
	if !pol.Ligado {
		return false
	}

	disco, temDisco := s.registro.Obter(armazenamento.ChaveLocal)

	// Arquivar sem esperar o aperto.
	//
	// O padrão é reagir: só move quando o disco encheu. Com disco pequeno isso chega tarde —
	// o aperto já significa cópias falhando por falta de espaço, e o arquivamento passa a
	// competir com os downloads em vez de abrir caminho para eles.
	//
	// Ligado, o disco fica permanentemente folgado: tudo que passou da carência já desceu, e
	// o espaço está livre para o que estiver quente agora. Custa banda — cada arquivo sobe
	// para a nuvem mesmo quando não precisava — e é uma troca que só quem conhece o próprio
	// disco pode fazer.
	if temDisco && pol.ArquivarSempre && b.arquivarUm(ctx, pol, disco) {
		return true
	}

	if temDisco && !s.HaOndeGuardar(ctx, pol, store.BackendLocal, disco) {
		// Degrau 1: mover para a nuvem, se houver conta que receba.
		if b.arquivarUm(ctx, pol, disco) {
			return true
		}
		// Degrau 2: sem para onde mover, apagar o mais frio.
		if b.apagarOMaisFrio(ctx, pol, store.BackendLocal, disco) {
			return true
		}
	}

	// Degrau 3: a nuvem também tem limite.
	return b.limparNuvemCheia(ctx, pol)
}

// arquivarUm executa o degrau 1. False quando não há conta de nuvem que possa receber.
func (b *Baixador) arquivarUm(ctx context.Context, pol Politica, disco armazenamento.Backend) bool {
	s := b.servico
	if _, err := s.store.NuvemParaGravar(ctx, 0); err != nil {
		// Nenhuma conta cadastrada, ou todas cheias. Não é falha: é a instalação que
		// escolheu não usar nuvem, e o degrau seguinte cuida dela.
		return false
	}

	arquivo, err := s.store.CandidatoParaArquivar(ctx, pol.IdadeMinima)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			b.log.Warn("falha ao escolher cópia para arquivar", "erro", err)
		}
		return false
	}
	if err := b.moverParaNuvem(ctx, arquivo, disco); err != nil {
		b.log.Warn("falha ao arquivar na nuvem; a cópia local foi mantida",
			"arquivo_id", arquivo.ID, "erro", err)
		return false
	}
	return true
}

// apagarOMaisFrio executa os degraus 2 e 3.
// apagarOMaisFrio escolhe e apaga a cópia mais fria de um armazenamento.
func (b *Baixador) apagarOMaisFrio(ctx context.Context, pol Politica, backend string,
	onde armazenamento.Backend) bool {

	arquivo, err := b.servico.store.CandidatoParaLimpeza(ctx, pol.IdadeMinima, backend)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			b.log.Warn("falha ao escolher cópia para apagar", "erro", err)
		}
		// Sem candidato com o armazenamento cheio significa uma de duas coisas, e as duas
		// são estado legítimo: tudo é recente demais (a carência está segurando), ou o que
		// resta é acervo próprio e protegido, que a limpeza não toca. Em ambos, o sistema
		// para de guardar e segue servindo — que é o comportamento correto.
		return false
	}
	return b.apagarCopia(ctx, arquivo, onde, backend)
}

// apagarCopia remove o arquivo e esquece a linha, nessa ordem.
//
// Apagar a linha primeiro deixaria o arquivo órfão no armazenamento, ocupando espaço que
// ninguém mais sabe que está ocupado — o mesmo defeito que a remoção manual tinha.
func (b *Baixador) apagarCopia(ctx context.Context, arquivo *store.ArquivoGuardado,
	onde armazenamento.Backend, backend string) bool {

	if err := onde.Apagar(ctx, arquivo.Localizador); err != nil &&
		!errors.Is(err, armazenamento.ErrNaoEncontrado) {
		// Já não estar lá é sucesso: o objetivo era que sumisse.
		b.log.Warn("falha ao apagar cópia fria", "arquivo_id", arquivo.ID, "erro", err)
		return false
	}
	if err := b.servico.store.EsquecerArquivo(ctx, arquivo.ID); err != nil {
		b.log.Warn("cópia apagada, mas a linha permaneceu", "arquivo_id", arquivo.ID, "erro", err)
		return false
	}

	b.log.Info("cópia fria apagada para abrir espaço",
		"arquivo_id", arquivo.ID, "onde", backend, "bytes", arquivo.Bytes,
		"acessos", arquivo.Acessos, "ultimo_acesso", arquivo.UltimoAcesso)
	return true
}

// limparNuvemCheia é o degrau 3: espaço de nuvem também acaba, e um acervo que só cresce
// vira conta que só sobe.
func (b *Baixador) limparNuvemCheia(ctx context.Context, pol Politica) bool {
	s := b.servico
	if _, err := s.store.NuvemParaGravar(ctx, 0); err == nil {
		// Ainda há conta com espaço: não há por que apagar nada de lá.
		return false
	}

	arquivo, err := s.store.CandidatoParaLimpeza(ctx, pol.IdadeMinima, store.BackendNuvem)
	if err != nil {
		return false
	}
	destino, err := s.Backend(ctx, arquivo)
	if err != nil {
		return false
	}
	return b.apagarCopia(ctx, arquivo, destino, store.BackendNuvem)
}

// intervaloDaMedicao é de quanto em quanto tempo o espaço das contas de nuvem é conferido.
//
// Quinze minutos, e não a cada uso: medir é uma ida ao provedor, e a resposta muda devagar —
// o que ocupa uma conta de nuvem é o que nós mesmos gravamos nela.
const intervaloDaMedicao = 15 * time.Minute

// medirNuvens pergunta a cada conta quanto espaço ela ainda tem.
//
// # Por que isto precisa existir
//
// As colunas de cota estavam no banco desde o começo e ninguém as preenchia. A tela dizia
// "ainda não medido" para sempre — e pior que o texto: `NuvemParaGravar` decide se uma conta
// pode receber comparando o tamanho pedido com o espaço livre, e sem medição essa comparação
// era sempre verdadeira. O sistema mandaria arquivos para uma conta cheia até o provedor
// recusar, um a um.
//
// Erros ficam registrados na própria conta, e não só no log: uma conta com token vencido
// falha em tudo, e a tela precisa dizer isso onde a pessoa vai procurar.
func (b *Baixador) medirNuvens(ctx context.Context) {
	nuvens, err := b.store.ListarNuvens(ctx)
	if err != nil {
		b.log.Warn("falha ao listar contas de nuvem para medir", "erro", err)
		return
	}

	for i := range nuvens {
		n := &nuvens[i]
		if !n.Ativa {
			continue
		}
		destino, err := b.servico.BackendDaNuvem(ctx, n.ID)
		if err != nil {
			// BackendDaNuvem já registra o motivo na conta. Aqui só seguimos.
			continue
		}
		esp, err := destino.Espaco(ctx)
		if err != nil {
			if e := b.store.AnotarErroDaNuvem(ctx, n.ID, "não foi possível medir o espaço: "+err.Error()); e != nil {
				b.log.Warn("falha ao registrar erro de medição", "conta", n.Nome, "erro", e)
			}
			continue
		}
		// Ilimitado é uma resposta legítima — contas empresariais existem. Guardado como
		// total zero, que é como o resto do sistema já lê "não há teto".
		totais := esp.Total
		if esp.Ilimitado {
			totais = 0
		}
		if err := b.store.AnotarCotaDaNuvem(ctx, n.ID, esp.Usado, totais); err != nil {
			b.log.Warn("falha ao anotar a cota da conta", "conta", n.Nome, "erro", err)
			continue
		}
		b.log.Info("espaço da conta de nuvem medido",
			"conta", n.Nome, "usado", esp.Usado, "total", totais, "ilimitado", esp.Ilimitado)
	}
}
