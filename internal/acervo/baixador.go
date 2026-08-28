package acervo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"vodmanager/internal/armazenamento"
	"vodmanager/internal/roles"
	"vodmanager/internal/store"
)

// O baixador: quem transforma a fila em acervo.
//
// # Onde ele fica no fluxo
//
// A reprodução ENFILEIRA — ela não baixa. É deliberado: quem está esperando o filme começar
// não pode pagar o custo de uma cópia de 2 GB. A reprodução deixa o pedido registrado e
// segue entregando da fonte, como sempre.
//
// Este módulo consome a fila depois, fora do caminho de ninguém.
//
// # Por que devagar de propósito
//
// Baixar é a operação mais cara do sistema: consome a mesma banda da fonte que os
// espectadores estão usando naquele instante. Um baixador ganancioso melhora o amanhã às
// custas do agora — e ninguém agradece por um filme rápido na semana que vem se o de hoje
// travou.
//
// Daí as duas travas: poucos trabalhadores, e um intervalo entre as consultas à fila. Um
// acervo que se enche em uma semana sem ninguém perceber é melhor que um que se enche numa
// noite derrubando todo mundo.

// trabalhadores é quantas cópias acontecem ao mesmo tempo.
//
// Dois, e não dez. Cada cópia é uma conexão a mais na fonte — e a fonte é o recurso mais
// escasso e o mais fácil de irritar. Duas cópias somadas ao tráfego dos espectadores ainda
// cabem na conta de qualquer fonte; dez fariam a fonte cortar o acesso, e aí o prejuízo não
// seria só do cache.
const trabalhadores = 2

// intervaloDaFila é quanto se espera antes de olhar a fila de novo, quando ela está vazia.
//
// A fila fica vazia a maior parte do tempo. Consultá-la em laço apertado seria uma consulta
// por segundo, para sempre, para não encontrar nada.
const intervaloDaFila = 30 * time.Second

// intervaloDoProgresso é de quanto em quanto tempo o andamento é gravado.
//
// Uma escrita a cada 5 segundos, e não a cada bloco: um vídeo de 2 GB tem milhares de
// blocos, e uma escrita no banco por bloco custaria mais que a própria cópia.
const intervaloDoProgresso = 5 * time.Second

// Resolvedor materializa a URL de origem de uma variante.
//
// Interface e não implementação concreta, pelo mesmo motivo do plano de dados: este pacote
// não conhece credenciais de fonte nem provedores — ele pede a URL a quem sabe montá-la.
type Resolvedor interface {
	ResolveStreamURL(ctx context.Context, variant *store.SourceVariant) (string, error)
}

// Baixador consome a fila de cópias.
type Baixador struct {
	servico    *Servico
	store      *store.Store
	resolvedor Resolvedor
	log        *slog.Logger
	http       *http.Client

	parar chan struct{}
	fim   sync.WaitGroup
	errCh chan error

	// medidoEm marca a ultima medicao das contas de nuvem. Tocado so pelo trabalhador 1,
	// que e o unico que mede — por isso nao precisa de trava.
	medidoEm time.Time
}

// NovoBaixador cria o módulo.
func NovoBaixador(s *Servico, st *store.Store, r Resolvedor, log *slog.Logger) *Baixador {
	return &Baixador{
		servico: s, store: st, resolvedor: r, log: log,
		parar: make(chan struct{}),
		errCh: make(chan error, 1),
		http: &http.Client{
			// SEM prazo total: uma cópia de 40 GB leva horas. O que protege é o prazo para a
			// fonte RESPONDER, não para a transferência terminar.
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout: 15 * time.Second, KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:          10,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

// Name identifica o módulo.
func (b *Baixador) Name() string { return "baixador" }

// Roles: só o Manager consome a fila. Num Node, baixar duplicaria o trabalho.
func (b *Baixador) Roles() []roles.Role { return []roles.Role{roles.RoleManager} }

// Err devolve o canal de erro fatal. O baixador não tem nenhum: uma cópia que falha é uma
// linha marcada com erro, não um motivo para derrubar o serviço.
func (b *Baixador) Err() <-chan error { return b.errCh }

// Start sobe os trabalhadores.
func (b *Baixador) Start(ctx context.Context) error {
	// Resíduo de uma queda anterior: cópias marcadas como em curso, sem ninguém as fazendo.
	// A fila não enxerga esse estado, então elas ficariam paradas para sempre.
	if n, err := b.store.DestravarCopiasPendentes(ctx); err != nil {
		b.log.Warn("falha ao destravar cópias da execução anterior", "erro", err)
	} else if n > 0 {
		b.log.Warn("cópias interrompidas devolvidas à fila", "quantidade", n)
	}

	// Copias truncadas de uma versao anterior: inertes na reproducao, mas ocupando a
	// variante — o que impediria o titulo de ser copiado de novo, para sempre.
	if n, err := b.store.MarcarCopiasTruncadas(ctx); err != nil {
		b.log.Warn("falha ao marcar cópias truncadas", "erro", err)
	} else if n > 0 {
		b.log.Warn("cópias truncadas enviadas para remoção; os títulos voltam à fila",
			"quantidade", n)
	}

	for i := 0; i < trabalhadores; i++ {
		b.fim.Add(1)
		go b.trabalhar(i + 1)
	}
	b.log.Info("baixador do acervo no ar", "trabalhadores", trabalhadores)
	return nil
}

// Stop encerra os trabalhadores.
//
// Espera as cópias em curso terminarem de reagir ao cancelamento, mas não espera elas
// concluírem: uma cópia interrompida volta para a fila e recomeça depois.
func (b *Baixador) Stop(ctx context.Context) error {
	close(b.parar)
	pronto := make(chan struct{})
	go func() { b.fim.Wait(); close(pronto) }()
	select {
	case <-pronto:
	case <-ctx.Done():
	}
	return nil
}

func (b *Baixador) trabalhar(numero int) {
	defer b.fim.Done()

	for {
		select {
		case <-b.parar:
			return
		default:
		}

		// Contexto ligado ao encerramento do módulo: parar o serviço aborta a cópia em
		// curso em vez de esperar horas por um arquivo de 40 GB.
		ctx, cancelar := context.WithCancel(context.Background())
		go func() {
			select {
			case <-b.parar:
				cancelar()
			case <-ctx.Done():
			}
		}()

		// A medição das contas de nuvem só no trabalhador 1: ela é a mesma para todos, e dois
		// trabalhadores medindo dobrariam as idas ao provedor sem nenhum ganho.
		if numero == 1 && time.Since(b.medidoEm) >= intervaloDaMedicao {
			b.medidoEm = time.Now()
			b.medirNuvens(ctx)
		}

		// Remoções ANTES de cópias, sempre.
		//
		// Quem mandou apagar está quase sempre tentando abrir espaço, e fazê-lo esperar o
		// fim de um download de 2 GB é entregar o oposto do que ele pediu. Apagar também é
		// barato — segundos contra horas.
		trabalhou := b.umaRemocao(ctx)
		if !trabalhou {
			// Arquivar antes de copiar, e não depois.
			//
			// Só acontece com o disco apertado — e nesse estado uma cópia nova não caberia
			// de qualquer forma. Mover o frio para a nuvem primeiro é o que devolve espaço
			// para a cópia seguinte, em vez de deixá-la falhar por falta dele.
			trabalhou = b.liberarEspaco(ctx)
		}
		if !trabalhou {
			trabalhou = b.umaCopia(ctx, numero)
		}
		cancelar()

		if trabalhou {
			// Havia trabalho: procura o próximo imediatamente.
			continue
		}
		select {
		case <-b.parar:
			return
		case <-time.After(intervaloDaFila):
		}
	}
}

// umaCopia pega um item da fila e o executa. Devolve false quando não havia nada.
func (b *Baixador) umaCopia(ctx context.Context, numero int) bool {
	arquivo, err := b.store.TomarDaFila(ctx)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			b.log.Warn("falha ao consultar a fila do acervo", "erro", err)
		}
		return false
	}

	inicio := time.Now()
	b.log.Info("cópia iniciada",
		"trabalhador", numero, "arquivo_id", arquivo.ID, "variante", arquivo.VariantID)

	if err := b.copiar(ctx, arquivo); err != nil {
		// Cancelamento é desligamento do serviço, não falha da cópia: devolver à fila faz
		// ela recomeçar na próxima subida, em vez de ficar marcada como erro para sempre.
		// Falta de espaço também volta para a fila, e não vira erro.
		//
		// Marcar erro aqui aposentaria o título: a linha ficaria vermelha para sempre, e
		// quando a limpeza abrisse espaço minutos depois ninguém a traria de volta. O
		// resultado seria um acervo que para de crescer no primeiro aperto e não retoma.
		//
		// Não há risco de laço apertado: a fila só é consultada a cada 30 segundos, e a
		// limpeza roda antes das cópias no mesmo trabalhador.
		if errors.Is(err, context.Canceled) || errors.Is(err, armazenamento.ErrSemEspaco) {
			if e := b.store.DevolverAFila(context.Background(), arquivo.ID); e != nil {
				b.log.Warn("falha ao devolver a cópia à fila", "arquivo_id", arquivo.ID, "erro", e)
			}
			if errors.Is(err, armazenamento.ErrSemEspaco) {
				b.log.Warn("sem espaço para esta cópia; ela volta à fila e espera a limpeza",
					"arquivo_id", arquivo.ID, "erro", err)
			}
			return false
		}
		b.log.Warn("cópia falhou", "arquivo_id", arquivo.ID, "erro", err)
		if e := b.store.FalharArquivo(context.Background(), arquivo.ID, err.Error()); e != nil {
			b.log.Warn("falha ao registrar o erro da cópia", "arquivo_id", arquivo.ID, "erro", e)
		}
		return true
	}

	b.log.Info("cópia concluída",
		"arquivo_id", arquivo.ID, "duracao", time.Since(inicio).Round(time.Second).String())
	return true
}

// copiar baixa o arquivo, tentando as origens em ordem até uma servir.
//
// # Por que o baixador também precisa de failover
//
// A reprodução tenta até três origens antes de desistir. O baixador conhecia UMA, e a
// consequência apareceu em produção: uma fonte respondeu 403 ao robô e o título ficou
// marcado com erro para sempre — mesmo com outra fonte, na mesma lista, servindo o filme
// inteiro sem reclamar.
//
// O 403 é o caso mais comum e o mais enganoso: fontes distinguem o espectador do robô por
// cabeçalho, horário ou taxa de acesso. Uma que recusa a cópia pode estar entregando vídeo
// normalmente naquele instante.
func (b *Baixador) copiar(ctx context.Context, arquivo *store.ArquivoGuardado) error {
	if arquivo.VariantID == nil {
		return errors.New("cópia sem variante de origem")
	}

	origens := b.origensDe(ctx, arquivo)
	var ultimoErro error
	for i := range origens {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := b.copiarDe(ctx, arquivo, &origens[i])
		if err == nil {
			return nil
		}
		// Sem espaço não é problema da origem: trocar de fonte não faz caber. Sobe na hora,
		// para o chamador poder devolver à fila em vez de queimar as demais origens.
		if errors.Is(err, armazenamento.ErrSemEspaco) || errors.Is(err, context.Canceled) {
			return err
		}
		ultimoErro = err
		if len(origens) > 1 {
			b.log.Warn("origem falhou na cópia, tentando a próxima",
				"arquivo_id", arquivo.ID, "fonte", origens[i].SourceName, "erro", err)
		}
	}
	if ultimoErro == nil {
		return errors.New("nenhuma origem disponível para esta cópia")
	}
	return ultimoErro
}

// origensDe lista as origens a tentar, começando pela que foi registrada.
//
// Em caso de falha ao consultar as irmãs, segue com a original: uma consulta a menos não
// pode impedir a cópia de acontecer.
func (b *Baixador) origensDe(ctx context.Context, arquivo *store.ArquivoGuardado) []store.PlayableVariant {
	irmas, err := b.store.VariantesDoAlvo(ctx, arquivo.TargetKind, arquivo.TargetID)
	if err != nil || len(irmas) == 0 {
		v, err := b.store.GetVariant(ctx, *arquivo.VariantID)
		if err != nil {
			return nil
		}
		return []store.PlayableVariant{{
			ID: v.ID, SourceID: v.SourceID, OriginURL: v.OriginURL,
			StreamRef: v.StreamRef, ContainerExt: v.ContainerExt,
		}}
	}

	// A variante registrada vem primeiro: é a que a reprodução escolheu, e por isso a mais
	// provável de servir. As demais entram atrás, na ordem de prioridade.
	for i := range irmas {
		if irmas[i].ID == *arquivo.VariantID && i != 0 {
			irmas[0], irmas[i] = irmas[i], irmas[0]
			break
		}
	}
	return irmas
}

func (b *Baixador) copiarDe(ctx context.Context, arquivo *store.ArquivoGuardado,
	v *store.PlayableVariant) error {

	variante := &store.SourceVariant{
		ID: v.ID, SourceID: v.SourceID, OriginURL: v.OriginURL,
		StreamRef: v.StreamRef, ContainerExt: v.ContainerExt,
	}
	url, err := b.resolvedor.ResolveStreamURL(ctx, variante)
	if err != nil {
		return fmt.Errorf("resolvendo a URL da fonte: %w", err)
	}

	destino, err := b.servico.Backend(ctx, arquivo)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "VODManager/1.0")
	req.Header.Set("Accept", "*/*")

	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("conectando à fonte: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("a fonte respondeu %s", resp.Status)
	}

	// O tamanho anunciado vai para o banco antes da cópia começar: é dele que a tela tira a
	// porcentagem, e sem ele o progresso seria um número subindo sem fim à vista.
	if resp.ContentLength > 0 {
		if err := b.store.AnotarTamanhoTotal(ctx, arquivo.ID, resp.ContentLength); err != nil {
			b.log.Warn("falha ao anotar o tamanho", "arquivo_id", arquivo.ID, "erro", err)
		}
	}

	nome := variante.DeclaredTitle
	if nome == "" {
		nome = fmt.Sprintf("acervo-%d", arquivo.ID)
	}
	if arquivo.ContainerExt != "" {
		nome += "." + arquivo.ContainerExt
	}

	// O corpo passa por um contador que grava o andamento de tempos em tempos. Envolver o
	// leitor, em vez de o backend reportar, mantém o progresso funcionando igual no disco e
	// na nuvem — nenhum dos dois precisa saber que alguém está acompanhando.
	acompanhado := &leitorComProgresso{
		origem:  resp.Body,
		anotar:  func(n int64) { _ = b.store.AnotarProgresso(context.Background(), arquivo.ID, n) },
		periodo: intervaloDoProgresso,
	}

	local, err := destino.Guardar(ctx, nome, acompanhado, resp.ContentLength)
	if err != nil {
		if errors.Is(err, armazenamento.ErrSemEspaco) {
			return fmt.Errorf("sem espaço no destino: %w", err)
		}
		// "unexpected EOF" é o que o Go diz quando a fonte fecha a conexão antes de
		// entregar o Content-Length que ela mesma anunciou. Repassado cru, vira uma linha
		// no painel que parece defeito do armazenamento — e manda procurar no lugar errado.
		//
		// É a falha mais comum de fonte de IPTV sob carga, e a mesma que corta o filme no
		// meio de quem está assistindo. Aqui ela não causa dano: a cópia parcial já foi
		// apagada, e a fonte continua servindo como antes.
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("a fonte encerrou antes de entregar os %d bytes que anunciou "+
				"(entregou cerca de %d); nada foi guardado", resp.ContentLength, acompanhado.total)
		}
		return err
	}

	// A fonte pode ter encerrado antes de entregar o que anunciou. Guardar meio filme como
	// se estivesse pronto é pior que não guardar: ele passaria a ser servido no lugar da
	// fonte, e o espectador veria o corte toda vez.
	if resp.ContentLength > 0 && local.Bytes < resp.ContentLength {
		_ = destino.Apagar(context.Background(), local.Localizador)
		return fmt.Errorf("a fonte entregou %d de %d bytes; a cópia foi descartada",
			local.Bytes, resp.ContentLength)
	}

	return b.store.ConcluirArquivo(context.Background(), arquivo.ID, local.Localizador, local.Bytes)
}

// leitorComProgresso conta os bytes que passam e grava o andamento de tempos em tempos.
type leitorComProgresso struct {
	origem  io.Reader
	anotar  func(int64)
	periodo time.Duration

	total    int64
	ultimaEm time.Time
}

func (l *leitorComProgresso) Read(p []byte) (int, error) {
	n, err := l.origem.Read(p)
	if n > 0 {
		l.total += int64(n)
		if time.Since(l.ultimaEm) >= l.periodo {
			l.ultimaEm = time.Now()
			l.anotar(l.total)
		}
	}
	return n, err
}

// umaRemocao apaga um arquivo marcado para remoção. Devolve false quando não havia nenhum.
//
// # Por que apagar é em duas etapas
//
// O arquivo existe em dois lugares: no armazenamento e no banco. Apagar a linha primeiro
// deixaria o arquivo órfão no disco — ocupando espaço que ninguém mais sabe que está
// ocupado, e que só apareceria como uma diferença inexplicável entre o que o painel diz e o
// que o `df` mostra.
//
// Então: apaga no armazenamento, e só então esquece a linha. A ordem inversa é irreversível;
// esta, no pior caso, deixa a linha para a próxima rodada.
func (b *Baixador) umaRemocao(ctx context.Context) bool {
	arquivo, err := b.store.TomarParaRemocao(ctx)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			b.log.Warn("falha ao consultar a fila de remoção", "erro", err)
		}
		return false
	}

	// Sem localizador não há o que apagar no armazenamento: é uma cópia que nunca chegou a
	// existir em disco — enfileirada e cancelada, ou que falhou antes do primeiro byte.
	if arquivo.Localizador != "" {
		destino, err := b.servico.Backend(ctx, arquivo)
		if err != nil {
			// Conta de nuvem desativada, disco não montado. A linha fica marcada e volta na
			// próxima rodada: perder a referência agora criaria o órfão que a ordem das
			// etapas existe para evitar.
			b.log.Warn("armazenamento indisponível para remover; será tentado de novo",
				"arquivo_id", arquivo.ID, "erro", err)
			return false
		}
		if err := destino.Apagar(ctx, arquivo.Localizador); err != nil &&
			!errors.Is(err, armazenamento.ErrNaoEncontrado) {
			// Já não estar lá é sucesso: o objetivo era que sumisse.
			b.log.Warn("falha ao apagar do armazenamento; será tentado de novo",
				"arquivo_id", arquivo.ID, "localizador", arquivo.Localizador, "erro", err)
			return false
		}
	}

	if err := b.store.EsquecerArquivo(ctx, arquivo.ID); err != nil {
		b.log.Warn("arquivo apagado, mas a linha permaneceu", "arquivo_id", arquivo.ID, "erro", err)
		return false
	}
	b.log.Info("arquivo removido do acervo",
		"arquivo_id", arquivo.ID, "bytes", arquivo.Bytes, "onde", arquivo.Backend)
	return true
}
