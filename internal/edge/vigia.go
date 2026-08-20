package edge

import (
	"context"
	"io"
	"sync/atomic"
	"time"
)

// prazoPrimeiroByte é quanto esperamos por QUALQUER byte de conteúdo antes de desistir.
//
// O prazo de cabeçalho da fonte já é de 20 segundos; chegar aqui significa que ela
// respondeu e depois parou de enviar. Numa reprodução saudável o primeiro byte sai em
// menos de um segundo, então 60 segundos não corta nada legítimo — só o que travou.
var prazoPrimeiroByte = 60 * time.Second

// leitorVigiado aborta a leitura quando a fonte aceita a conexão e não envia nada.
//
// O caso que ele resolve foi observado em produção: uma reprodução aberta há 21 minutos,
// com zero bytes entregues e nenhum primeiro byte. Sem um limite, essa conexão fica presa
// para sempre — segurando uma vaga do limite da credencial e uma conexão na fonte, que
// costuma ser o recurso mais escasso.
//
// O prazo vale SÓ até o primeiro byte. Depois disso a transmissão pode demorar horas, que
// é o normal de um filme, e nenhum relógio interfere: cortar no meio por lentidão
// derrubaria quem está assistindo de verdade numa rede ruim.
type leitorVigiado struct {
	origem       io.Reader
	cancelar     context.CancelFunc
	temporizador *time.Timer
	comecou      atomic.Bool
	// disparou registra que fomos NÓS que cortamos. Sem isso, o cancelamento seria
	// indistinguível de um cliente fechando o player — e a falha da fonte apareceria no
	// painel como comportamento normal do espectador.
	disparou atomic.Bool
}

// vigiarPrimeiroByte embrulha o corpo da resposta e arma o relógio.
//
// Devolve também a função de encerramento, que precisa ser chamada ao fim da cópia para o
// relógio não sobreviver à requisição.
func vigiarPrimeiroByte(corpo io.Reader, cancelar context.CancelFunc) (io.Reader, func(), func() bool) {
	l := &leitorVigiado{origem: corpo, cancelar: cancelar}
	l.temporizador = time.AfterFunc(prazoPrimeiroByte, func() {
		// Se o primeiro byte já passou, o relógio não tem mais nada a fazer.
		if !l.comecou.Load() {
			l.disparou.Store(true)
			cancelar()
		}
	})
	return l, func() { l.temporizador.Stop() }, l.disparou.Load
}

func (l *leitorVigiado) Read(p []byte) (int, error) {
	n, err := l.origem.Read(p)
	if n > 0 && l.comecou.CompareAndSwap(false, true) {
		// A partir daqui a transmissão está viva e pode levar o tempo que precisar.
		l.temporizador.Stop()
	}
	return n, err
}

// prazoPrimeiroByteParaTeste troca o prazo e devolve o valor anterior.
//
// Existe porque o valor de produção é de minutos: um teste que o respeitasse levaria
// minutos para provar um comportamento de milissegundos.
func prazoPrimeiroByteParaTeste(novo time.Duration) time.Duration {
	anterior := prazoPrimeiroByte
	prazoPrimeiroByte = novo
	return anterior
}

// escritorDoCliente registra se a falha aconteceu ao escrever PARA O CLIENTE.
//
// io.Copy devolve um erro só, e ele pode ter vindo de dois lados: da leitura na fonte ou
// da escrita no cliente. A diferença é tudo — "broken pipe" ao escrever significa que o
// espectador fechou o player ou deu seek, que é o comportamento mais comum de quem
// assiste. Tratar isso como falha de transmissão inflava a contagem de erros do painel e
// escondia os problemas reais no meio do ruído.
//
// Marcar o lado na hora da escrita é mais confiável que interpretar o texto do erro, que
// muda entre sistemas operacionais e versões.
type escritorDoCliente struct {
	destino io.Writer
	falhou  bool
}

func (e *escritorDoCliente) Write(p []byte) (int, error) {
	n, err := e.destino.Write(p)
	if err != nil {
		e.falhou = true
	}
	return n, err
}
