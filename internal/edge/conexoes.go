package edge

import (
	"context"
	"sync"
	"time"

	"vodmanager/internal/store"
)

// ContadorConexoes limita quantas reproduções simultâneas cada credencial pode ter.
//
// É o que torna a credencial vendável: sem limite, um cliente compartilha a senha com
// dez pessoas e você paga a banda de todas. Com limite, a décima primeira recebe recusa.
//
// Vive em memória, no processo que serve os bytes — que é justamente onde a conexão está.
// Quando houver múltiplos nós, o contador migra para a interface Coordinator, como o
// single-flight do cache.
type ContadorConexoes struct {
	mu     sync.Mutex
	ativas map[int64]int
}

// NovoContadorConexoes cria o contador.
func NovoContadorConexoes() *ContadorConexoes {
	return &ContadorConexoes{ativas: map[int64]int{}}
}

// Liberar devolve a vaga ocupada por uma conexão.
type Liberar func()

// Ocupar tenta reservar uma vaga para a credencial.
//
// Devolve false quando o limite já foi atingido. Credencial sem limite configurado passa
// sempre — mas continua sendo contada, para o painel poder mostrar quantas reproduções
// simultâneas cada cliente tem.
func (c *ContadorConexoes) Ocupar(cred *store.StreamCredential) (Liberar, bool) {
	if cred == nil {
		return func() {}, true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	atuais := c.ativas[cred.ID]
	if cred.MaxConnections != nil && atuais >= *cred.MaxConnections {
		return nil, false
	}
	c.ativas[cred.ID] = atuais + 1

	var umaVez sync.Once
	return func() {
		umaVez.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if n := c.ativas[cred.ID]; n <= 1 {
				delete(c.ativas, cred.ID)
			} else {
				c.ativas[cred.ID] = n - 1
			}
		})
	}, true
}

// Ativas informa quantas reproduções uma credencial tem neste momento.
func (c *ContadorConexoes) Ativas(credID int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ativas[credID]
}

// Snapshot devolve a contagem de todas as credenciais.
func (c *ContadorConexoes) Snapshot() map[int64]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	copia := make(map[int64]int, len(c.ativas))
	for k, v := range c.ativas {
		copia[k] = v
	}
	return copia
}

// esperaPorVaga é quanto tempo uma reprodução aguarda uma vaga antes de ser recusada.
//
// # O problema que isto resolve
//
// Quem consome não abre UMA conexão por filme. Um painel que faz direct stream lê em
// pedaços: abre, puxa um trecho, fecha, reabre — dezenas de vezes durante um filme.
//
// A cada reabertura, a conexão anterior ainda pode estar terminando de fechar. Por uma
// fração de segundo, a MESMA pessoa ocupa duas vagas. Se a credencial estiver no limite, a
// nova é recusada com 429 e o espectador vê o vídeo morrer — não porque haja gente demais
// assistindo, mas porque duas conexões dele se cruzaram no tempo.
//
// É por isso que a falha é INTERMITENTE: ela depende de as duas se sobreporem, o que
// acontece às vezes.
//
// # Por que esperar resolve
//
// A vaga que está segurando o limite pertence a uma conexão que já está morrendo. Esperar
// alguns segundos por ela transforma uma falha dura numa pequena demora — e a demora é
// invisível, porque acontece antes do primeiro byte.
//
// Não afrouxa o limite: quem de fato tem gente demais assistindo continua sendo recusado,
// só que três segundos depois. O limite continua valendo para o que ele existe para conter.
var esperaPorVaga = 3 * time.Second

// intervaloDeTentativa é de quanto em quanto tempo a vaga é reconsultada.
//
// Curto, porque o que se espera é o fim de uma conexão que já está fechando — coisa de
// milissegundos. Um intervalo longo transformaria uma espera de 50ms numa de meio segundo.
const intervaloDeTentativa = 50 * time.Millisecond

// OcuparComEspera tenta reservar uma vaga, aguardando brevemente se não houver.
//
// Devolve false quando o prazo acaba sem vaga — aí a recusa é legítima: não é sobreposição,
// é limite de verdade.
//
// Respeita o cancelamento: um cliente que desiste enquanto espera não deve segurar uma
// goroutine até o fim do prazo.
func (c *ContadorConexoes) OcuparComEspera(ctx context.Context, cred *store.StreamCredential) (Liberar, bool) {
	if liberar, ok := c.Ocupar(cred); ok {
		return liberar, true
	}

	limite := time.NewTimer(esperaPorVaga)
	defer limite.Stop()
	tic := time.NewTicker(intervaloDeTentativa)
	defer tic.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-limite.C:
			return nil, false
		case <-tic.C:
			if liberar, ok := c.Ocupar(cred); ok {
				return liberar, true
			}
		}
	}
}

// esperaPorVagaParaTeste troca o prazo. O valor de produção é de segundos, e um teste que o
// respeitasse levaria segundos para provar um comportamento de milissegundos.
func esperaPorVagaParaTeste(novo time.Duration) time.Duration {
	anterior := esperaPorVaga
	esperaPorVaga = novo
	return anterior
}
