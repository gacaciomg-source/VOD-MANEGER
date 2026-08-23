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

// prazoSemDados é quanto a fonte pode ficar sem enviar NADA no meio da transmissão.
//
// # O buraco que este prazo fecha
//
// O vigia cobria só o primeiro byte. Depois dele, uma fonte que enviasse um byte e
// travasse deixava a reprodução presa PARA SEMPRE — segurando uma vaga do limite da
// credencial e uma conexão na fonte.
//
// O estrago não fica no espectador que travou. As vagas presas se acumulam, e quando
// enchem o limite da credencial, o cliente inteiro passa a receber "limite de reproduções
// atingido" — e o player, diante disso, reinicia do começo. É por isso que o sintoma
// relatado não é "um filme travou", e sim "os vídeos reiniciam sozinhos depois de um
// tempo".
//
// # Por que ele não corta quem assiste devagar
//
// Lentidão e parada são coisas diferentes, e o código distingue as duas: um espectador em
// rede ruim continua RECEBENDO bytes, só que devagar — e cada byte recebido reinicia a
// contagem. Ficar dois minutos sem um único byte, com o cliente pronto para receber, é
// fonte morta, não rede lenta.
//
// # E quem pausa o filme?
//
// Esse é o caso que fazia este prazo parecer impossível. Com o player pausado, o cliente
// para de aceitar bytes, a escrita bloqueia, e a leitura na fonte para junto — do lado de
// fora, é idêntico a uma fonte travada.
//
// A diferença está em ONDE a cópia está parada: pausado, ela está bloqueada ESCREVENDO
// para o cliente; travada, bloqueada LENDO da fonte. O vigia consulta isso antes de cortar,
// e uma pausa de horas não é interrompida.
var prazoSemDados = 2 * time.Minute

// intervaloDeRonda é de quanto em quanto tempo o vigia confere.
//
// Uma ronda periódica em vez de um temporizador rearmado a cada leitura: um filme de 2 GB
// tem milhares de leituras, e reprogramar um relógio em cada uma custa mais que olhar o
// relógio de dez em dez segundos.
const intervaloDeRonda = 10 * time.Second

// leitorVigiado aborta a leitura quando a fonte para de enviar.
//
// O caso original foi observado em produção: uma reprodução aberta há 21 minutos, com zero
// bytes entregues e nenhum primeiro byte. Sem um limite, essa conexão fica presa para
// sempre — segurando uma vaga do limite da credencial e uma conexão na fonte, que costuma
// ser o recurso mais escasso.
type leitorVigiado struct {
	origem   io.Reader
	cancelar context.CancelFunc
	comecou  atomic.Bool
	// ultimoDado é o instante da última leitura com bytes, em nanossegundos.
	ultimoDado atomic.Int64
	// disparou registra que fomos NÓS que cortamos. Sem isso, o cancelamento seria
	// indistinguível de um cliente fechando o player — e a falha da fonte apareceria no
	// painel como comportamento normal do espectador.
	disparou atomic.Bool
	// esperandoCliente diz se a cópia está parada esperando o CLIENTE aceitar bytes.
	// É o que separa "o player está pausado" de "a fonte morreu".
	esperandoCliente func() bool
	parar            chan struct{}
	parou            atomic.Bool
}

// vigiarPrimeiroByte embrulha o corpo da resposta e põe o vigia de pé.
//
// Devolve também a função de encerramento, que precisa ser chamada ao fim da cópia para a
// ronda não sobreviver à requisição.
func vigiarPrimeiroByte(corpo io.Reader, cancelar context.CancelFunc) (io.Reader, func(), func() bool) {
	return vigiar(corpo, cancelar, nil)
}

// vigiar monta o vigia com a consulta de "estamos esperando o cliente?".
//
// `esperandoCliente` nulo significa "nunca estamos" — é o que os testes usam, e o que faz o
// vigia se comportar como o de antes quando não há escritor a consultar.
func vigiar(corpo io.Reader, cancelar context.CancelFunc, esperandoCliente func() bool) (io.Reader, func(), func() bool) {
	l := &leitorVigiado{
		origem:           corpo,
		cancelar:         cancelar,
		esperandoCliente: esperandoCliente,
		parar:            make(chan struct{}),
	}
	l.ultimoDado.Store(time.Now().UnixNano())
	go l.rondar()

	return l, func() {
		if l.parou.CompareAndSwap(false, true) {
			close(l.parar)
		}
	}, l.disparou.Load
}

// rondar confere periodicamente se a transmissão progrediu.
func (l *leitorVigiado) rondar() {
	intervalo := intervaloDeRonda
	// Num prazo curto — o dos testes — uma ronda de dez segundos nunca chegaria a rodar.
	if menor := prazoPrimeiroByte / 4; menor < intervalo {
		intervalo = menor
	}
	if menor := prazoSemDados / 4; menor < intervalo {
		intervalo = menor
	}
	if intervalo <= 0 {
		intervalo = time.Millisecond
	}

	tic := time.NewTicker(intervalo)
	defer tic.Stop()

	for {
		select {
		case <-l.parar:
			return
		case <-tic.C:
			prazo := prazoSemDados
			if !l.comecou.Load() {
				// Antes do primeiro byte o prazo é outro, e mais curto: uma fonte que
				// aceita a conexão e não começa nunca não merece dois minutos.
				prazo = prazoPrimeiroByte
			} else if l.esperandoCliente != nil && l.esperandoCliente() {
				// O player está pausado, ou a rede do espectador engasgou. Não somos nós
				// que temos de decidir quanto tempo alguém pode ficar com o filme parado.
				continue
			}

			parado := time.Since(time.Unix(0, l.ultimoDado.Load()))
			if parado >= prazo {
				l.disparou.Store(true)
				l.cancelar()
				return
			}
		}
	}
}

func (l *leitorVigiado) Read(p []byte) (int, error) {
	n, err := l.origem.Read(p)
	if n > 0 {
		l.comecou.Store(true)
		l.ultimoDado.Store(time.Now().UnixNano())
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

// prazoSemDadosParaTeste troca o prazo do meio da transmissão.
func prazoSemDadosParaTeste(novo time.Duration) time.Duration {
	anterior := prazoSemDados
	prazoSemDados = novo
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
	// escrevendo fica ligado enquanto a escrita para o cliente está em curso.
	//
	// É o sinal que o vigia consulta para não confundir um player pausado com uma fonte
	// morta: pausado, a cópia fica presa AQUI, esperando o cliente aceitar bytes.
	escrevendo atomic.Bool
}

func (e *escritorDoCliente) Write(p []byte) (int, error) {
	e.escrevendo.Store(true)
	n, err := e.destino.Write(p)
	e.escrevendo.Store(false)
	if err != nil {
		e.falhou = true
	}
	return n, err
}

// EsperandoCliente informa se a cópia está parada esperando o espectador.
func (e *escritorDoCliente) EsperandoCliente() bool { return e.escrevendo.Load() }
