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

// Registro reúne os backends disponíveis nesta instalação.
//
// Existe porque a escolha do destino é do administrador, por fonte e por arquivo, e muda
// sem reiniciar: o código que guarda pede "o backend chamado assim" em vez de carregar um
// deles na inicialização.
type Registro struct {
	backends map[string]Backend
}

// NovoRegistro monta o registro a partir dos backends configurados.
func NovoRegistro(backends ...Backend) *Registro {
	r := &Registro{backends: make(map[string]Backend, len(backends))}
	for _, b := range backends {
		if b != nil {
			r.backends[b.Nome()] = b
		}
	}
	return r
}

// Obter devolve um backend pelo nome.
func (r *Registro) Obter(nome string) (Backend, bool) {
	b, ok := r.backends[nome]
	return b, ok
}

// Nomes lista os backends disponíveis, para a tela oferecer o que existe — e não o que
// existiria se estivesse configurado.
func (r *Registro) Nomes() []string {
	out := make([]string, 0, len(r.backends))
	for nome := range r.backends {
		out = append(out, nome)
	}
	return out
}
