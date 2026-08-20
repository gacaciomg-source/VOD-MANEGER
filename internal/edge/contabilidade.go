package edge

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"vodmanager/internal/store"
)

// intervaloDescarga é de quanto em quanto tempo o consumo acumulado vai para o banco.
//
// Cinco segundos é o atraso máximo entre o cliente consumir e o painel mostrar. Curto o
// bastante para o administrador não achar que a contagem está quebrada, longo o bastante
// para transformar milhares de escritas numa só por credencial.
const intervaloDescarga = 5 * time.Second

// Contabilidade acumula o consumo por credencial e grava em lote.
//
// Sem ela, cada requisição de vídeo viraria um UPDATE na linha da credencial — e como
// todos os espectadores de um mesmo cliente compartilham a mesma linha, cada seek de cada
// player disputaria o mesmo bloqueio. Com cem pessoas assistindo, a contabilidade
// passaria a limitar a entrega do vídeo.
//
// O acúmulo fica em memória: um desligamento abrupto perde no máximo os últimos segundos
// de contagem. É uma troca deliberada — a contagem é para o administrador se orientar,
// não é registro financeiro.
type Contabilidade struct {
	store *store.Store
	log   *slog.Logger

	mu        sync.Mutex
	pendentes map[int64]*acumulado

	parar chan struct{}
	fim   sync.Once
}

type acumulado struct {
	usos  int
	bytes int64
}

// NovaContabilidade cria o acumulador e inicia a descarga periódica.
func NovaContabilidade(st *store.Store, log *slog.Logger) *Contabilidade {
	c := &Contabilidade{
		store:     st,
		log:       log,
		pendentes: map[int64]*acumulado{},
		parar:     make(chan struct{}),
	}
	go c.laco()
	return c
}

// Registrar soma o consumo de uma requisição concluída.
//
// É chamada depois que a resposta terminou, nunca antes do primeiro byte.
func (c *Contabilidade) Registrar(credencialID *int64, bytes int64) {
	// Nulo é o caso do link temporário assinado, que não pertence a credencial nenhuma.
	if credencialID == nil || *credencialID == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	a := c.pendentes[*credencialID]
	if a == nil {
		a = &acumulado{}
		c.pendentes[*credencialID] = a
	}
	a.usos++
	a.bytes += bytes
}

func (c *Contabilidade) laco() {
	t := time.NewTicker(intervaloDescarga)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			c.Descarregar()
		case <-c.parar:
			return
		}
	}
}

// Descarregar grava o acumulado. Exportada porque o desligamento e os testes precisam
// forçar a escrita sem esperar o próximo tique.
func (c *Contabilidade) Descarregar() {
	c.mu.Lock()
	lote := c.pendentes
	c.pendentes = map[int64]*acumulado{}
	c.mu.Unlock()

	if len(lote) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for id, a := range lote {
		if err := c.store.TouchStreamCredential(ctx, id, a.usos, a.bytes); err != nil {
			c.log.Warn("não foi possível gravar o consumo da credencial",
				"credencial_id", id, "usos", a.usos, "bytes", a.bytes, "erro", err)
			// Devolve para a próxima rodada: perder a contagem por uma falha
			// momentânea do banco seria pior que contar alguns segundos atrasado.
			c.devolver(id, a)
		}
	}
}

func (c *Contabilidade) devolver(id int64, a *acumulado) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if atual := c.pendentes[id]; atual != nil {
		atual.usos += a.usos
		atual.bytes += a.bytes
		return
	}
	c.pendentes[id] = a
}

// Fechar descarrega o que restou e encerra a rotina de fundo.
func (c *Contabilidade) Fechar() {
	c.fim.Do(func() {
		close(c.parar)
		c.Descarregar()
	})
}
