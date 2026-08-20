package edge

import (
	"sync"

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
