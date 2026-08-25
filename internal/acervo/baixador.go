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
func (b *Baixador) Start(context.Context) error {
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

		trabalhou := b.umaCopia(ctx, numero)
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
		if errors.Is(err, context.Canceled) {
			if e := b.store.DevolverAFila(context.Background(), arquivo.ID); e != nil {
				b.log.Warn("falha ao devolver a cópia à fila", "arquivo_id", arquivo.ID, "erro", e)
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

func (b *Baixador) copiar(ctx context.Context, arquivo *store.ArquivoGuardado) error {
	if arquivo.VariantID == nil {
		return errors.New("cópia sem variante de origem")
	}
	variante, err := b.store.GetVariant(ctx, *arquivo.VariantID)
	if err != nil {
		return fmt.Errorf("buscando a variante: %w", err)
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
