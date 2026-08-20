package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"vodmanager/internal/roles"
	"vodmanager/internal/store"
)

// intervaloTick é de quanto em quanto tempo o scheduler procura fontes vencidas.
// A granularidade de agendamento é de um minuto, o que é suficiente para intervalos
// medidos em horas e mantém o custo em zero quando não há nada a fazer.
const intervaloTick = time.Minute

// staleRunTimeout é quanto tempo uma execução pode ficar "running" antes de ser
// considerada órfã de um processo que caiu.
const staleRunTimeout = 6 * time.Hour

// Scheduler dispara sincronizações no intervalo configurado de cada fonte.
type Scheduler struct {
	orch *Orchestrator
	log  *slog.Logger

	// Uma sincronização por vez no processo inteiro. Duas fontes sincronizando juntas
	// dobrariam o uso de CPU e de banda sem ganho: o gargalo é a fonte, não nós.
	semaforo chan struct{}

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewScheduler cria o agendador.
func NewScheduler(orch *Orchestrator, log *slog.Logger) *Scheduler {
	return &Scheduler{
		orch:     orch,
		log:      log,
		semaforo: make(chan struct{}, 1),
	}
}

// Name identifica o módulo.
func (s *Scheduler) Name() string { return "sync" }

// Roles: a sincronização é plano de controle, roda apenas no Manager.
func (s *Scheduler) Roles() []roles.Role { return []roles.Role{roles.RoleManager} }

// Start sobe o laço de agendamento.
func (s *Scheduler) Start(ctx context.Context) error {
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel

	// Execuções penduradas por queda do processo bloqueariam a fonte, por causa do índice
	// de "uma run ativa por fonte". As deste nó são liberadas incondicionalmente: se
	// estamos subindo agora, nenhuma execução nossa sobreviveu.
	if n, err := s.orch.store.ReleaseStaleRuns(ctx, s.orch.nodeID, staleRunTimeout); err != nil {
		s.log.Warn("falha ao liberar execuções travadas", "erro", err)
	} else if n > 0 {
		s.log.Warn("execuções travadas liberadas na partida", "quantidade", n)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(intervaloTick)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				s.tick(loopCtx)
			}
		}
	}()

	s.log.Info("agendador de sincronização ativo", "intervalo_de_verificacao", intervaloTick.String())
	return nil
}

// Stop encerra o laço e espera a sincronização em andamento terminar.
func (s *Scheduler) Stop(context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	return nil
}

// tick procura fontes vencidas e sincroniza uma de cada vez.
func (s *Scheduler) tick(ctx context.Context) {
	vencidas, err := s.orch.store.ListSourcesDue(ctx)
	if err != nil {
		s.log.Warn("falha ao consultar fontes vencidas", "erro", err)
		return
	}
	for _, fonte := range vencidas {
		select {
		case <-ctx.Done():
			return
		case s.semaforo <- struct{}{}:
		}

		_, err := s.orch.SyncSource(ctx, fonte.ID, "scheduled")
		<-s.semaforo

		if err != nil && !errors.Is(err, ErrJaEmExecucao) {
			s.log.Error("sincronização agendada falhou",
				"source_id", fonte.ID, "fonte", fonte.Name, "erro", err)
		}
	}
}

// SyncNow dispara uma sincronização manual e devolve IMEDIATAMENTE a execução aberta.
//
// O trabalho continua em segundo plano: um catálogo com milhares de itens leva minutos, e
// segurar a requisição HTTP até o fim estouraria o timeout do navegador e perderia o
// progresso se a aba fosse fechada. O painel acompanha por GET /sync/runs/{id}.
//
// Quando já há uma sincronização desta MESMA fonte rodando, devolvemos a execução
// existente em vez de um erro: o agendador pode ter iniciado sozinho (uma fonte nova está
// vencida desde o cadastro), e o administrador que clica em Sincronizar quer ver o
// progresso, não uma recusa.
func (s *Scheduler) SyncNow(ctx context.Context, sourceID int64) (*store.SyncRun, error) {
	select {
	case s.semaforo <- struct{}{}:
	default:
		if run, err := s.orch.store.GetRunningSyncRun(ctx, sourceID); err == nil {
			return run, nil
		}
		outra, _ := s.orch.store.ListRunningSyncRuns(ctx)
		if len(outra) > 0 {
			return nil, fmt.Errorf("a fonte %q está sincronizando agora; aguarde ela terminar",
				outra[0].SourceName)
		}
		return nil, errors.New("há outra sincronização em andamento; aguarde ela terminar")
	}

	run, err := s.orch.SyncSourceAsync(sourceID, "manual", func() { <-s.semaforo })
	if err != nil {
		<-s.semaforo // a execução nem chegou a começar: devolve a vaga
		if errors.Is(err, ErrJaEmExecucao) {
			if existente, e := s.orch.store.GetRunningSyncRun(ctx, sourceID); e == nil {
				return existente, nil
			}
		}
		return nil, err
	}
	return run, nil
}

// Orchestrator expõe o orquestrador para os handlers da API.
func (s *Scheduler) Orchestrator() *Orchestrator { return s.orch }

// Store expõe o store, para os handlers que precisam consultar execuções.
func (o *Orchestrator) Store() *store.Store { return o.store }
