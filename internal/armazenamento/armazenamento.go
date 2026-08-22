// Package armazenamento guarda e devolve arquivos de mídia.
//
// # O contrato, e por que ele é tão estreito
//
// Um backend precisa de quatro coisas: gravar, abrir a partir de um ponto, apagar e dizer
// quanto espaço tem. Nada além disso.
//
// A restrição não é estética. O plano de dados serve vídeo com Range — o cliente pede "os
// bytes a partir de 3.500.000" quando arrasta a barra de progresso, e faz isso o tempo
// todo. Um backend que só soubesse "devolver o arquivo inteiro" obrigaria a baixar 2 GB
// para entregar os 4 MB que o espectador pediu. Por isso `Abrir` recebe um deslocamento: é
// o único jeito de um armazenamento remoto participar sem se tornar o gargalo.
//
// # O que NÃO está aqui
//
// Nada decide o que guardar, quando guardar ou o que apagar. Isso é política, vive em
// outra camada, e muda: hoje é "guarde as fontes marcadas", amanhã pode ser "guarde o que
// for pedido três vezes". Um backend que soubesse disso teria de ser reescrito a cada
// mudança de ideia.
package armazenamento

import (
	"context"
	"errors"
	"io"
	"sort"
	"strconv"
	"sync"
)

// Erros que os chamadores distinguem.
var (
	// ErrNaoEncontrado: o localizador não corresponde a nada. Para o cache, não é falha —
	// é a deixa para buscar na fonte.
	ErrNaoEncontrado = errors.New("arquivo não encontrado no armazenamento")
	// ErrSemEspaco separa "o armazenamento encheu" de "deu erro". O primeiro tem
	// tratamento (limpar e tentar de novo); o segundo, não.
	ErrSemEspaco = errors.New("sem espaço no armazenamento")
)

// Backend guarda arquivos em algum lugar.
//
// As implementações precisam ser seguras para uso concorrente: o plano de dados serve
// vários espectadores ao mesmo tempo, e o baixador trabalha em paralelo com eles.
type Backend interface {
	// Nome identifica o backend no banco e na tela. É o valor da coluna `backend`.
	Nome() string

	// Guardar grava o conteúdo e devolve o localizador para recuperá-lo depois.
	//
	// `sugestao` é um nome legível — o backend pode usá-lo, adaptá-lo ou ignorá-lo. O
	// localizador devolvido é a única coisa que o resto do sistema guarda.
	//
	// Recebe um io.Reader, e não um caminho, porque o conteúdo quase nunca está em disco
	// quando esta função é chamada: ele está chegando pela rede. Passar por um arquivo
	// temporário exigiria espaço livre igual ao vídeo inteiro — que é exatamente o que não
	// há quando o destino é a nuvem justamente por falta de disco.
	Guardar(ctx context.Context, sugestao string, conteudo io.Reader, bytesEsperados int64) (Localizacao, error)

	// Abrir devolve o conteúdo a partir de um deslocamento em bytes.
	//
	// deslocamento 0 lê do começo. O chamador fecha o ReadCloser.
	Abrir(ctx context.Context, localizador string, deslocamento int64) (io.ReadCloser, error)

	// Apagar remove o arquivo. Apagar o que já não existe não é erro: a limpeza precisa
	// ser repetível sem virar uma sequência de falhas.
	Apagar(ctx context.Context, localizador string) error

	// Espaco informa quanto cabe ainda.
	Espaco(ctx context.Context) (Espaco, error)
}

// Localizacao é o que fica guardado no banco depois de uma gravação.
type Localizacao struct {
	// Localizador é opaco para o resto do sistema: um caminho, um id, uma chave. Só o
	// backend que o produziu sabe lê-lo.
	Localizador string
	// Bytes é o tamanho realmente gravado — que pode diferir do anunciado pela origem, e
	// é o número em que se confia para contabilizar espaço.
	Bytes int64
}

// Espaco descreve a ocupação de um backend.
//
// Total zero significa "não sei" — é o caso de armazenamentos que não anunciam limite. Quem
// consome precisa tratar isso como "sem limite conhecido", e não como "cheio".
type Espaco struct {
	Total     int64
	Livre     int64
	Usado     int64
	Ilimitado bool
}

// ChaveLocal identifica o disco desta máquina no registro.
const ChaveLocal = "local"

// ChaveDaNuvem identifica uma CONTA de nuvem no registro.
//
// A chave é a conta, e não o provedor, porque há várias contas do mesmo provedor: sete
// Drives são sete backends distintos, cada um com token, cota e pasta próprios. Chavear por
// "gdrive" faria o segundo cadastro substituir o primeiro no registro — e os arquivos do
// primeiro passariam a ser procurados na conta errada.
func ChaveDaNuvem(id int64) string {
	return "nuvem:" + strconv.FormatInt(id, 10)
}

// Registro guarda os backends já montados, por chave.
//
// É um cache, não uma configuração: as contas de nuvem são cadastradas e removidas pelo
// painel a qualquer momento, e montar uma delas custa uma troca de token com o provedor.
// Montar a cada arquivo servido seria pagar essa ida e volta em todo pedido de reprodução.
//
// Guardar e Remover existem porque a lista muda em execução — o registro precisa aprender a
// conta nova sem reiniciar o serviço, e esquecer a removida sem continuar servindo dela.
type Registro struct {
	mu       sync.RWMutex
	backends map[string]Backend
}

// NovoRegistro monta o registro com os backends já conhecidos na inicialização.
func NovoRegistro(backends ...Backend) *Registro {
	r := &Registro{backends: make(map[string]Backend, len(backends)+1)}
	for _, b := range backends {
		if b != nil {
			r.backends[b.Nome()] = b
		}
	}
	return r
}

// Guardar registra um backend sob uma chave, substituindo o que houvesse.
//
// Substituir é o comportamento certo: quando o token de uma conta é renovado, o backend
// novo tem de tomar o lugar do antigo. Recusar deixaria o registro servindo de uma conta
// que já não autentica.
func (r *Registro) Guardar(chave string, b Backend) {
	if b == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[chave] = b
}

// Remover esquece um backend. Usado quando a conta é removida ou desativada.
func (r *Registro) Remover(chave string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.backends, chave)
}

// Obter devolve um backend pela chave.
func (r *Registro) Obter(chave string) (Backend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.backends[chave]
	return b, ok
}

// Chaves lista o que está montado, para a tela mostrar o que existe de fato — e não o que
// existiria se estivesse configurado.
func (r *Registro) Chaves() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.backends))
	for chave := range r.backends {
		out = append(out, chave)
	}
	sort.Strings(out)
	return out
}
