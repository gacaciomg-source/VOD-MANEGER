package edge

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// leitorMudo aceita a leitura e nunca devolve nada: é o comportamento da fonte que
// travou uma reprodução por 21 minutos com zero bytes entregues.
type leitorMudo struct{ ctx context.Context }

func (l leitorMudo) Read([]byte) (int, error) {
	<-l.ctx.Done()
	return 0, l.ctx.Err()
}

func TestVigiaCortaFonteQueNaoEnviaNada(t *testing.T) {
	// Prazo curto só para o teste: o valor de produção é minutos.
	original := prazoPrimeiroByteParaTeste(50 * time.Millisecond)
	defer prazoPrimeiroByteParaTeste(original)

	ctx, cancelar := context.WithCancel(context.Background())
	corpo, parar, travou := vigiarPrimeiroByte(leitorMudo{ctx}, cancelar)
	defer parar()

	inicio := time.Now()
	n, err := io.Copy(io.Discard, corpo)

	if n != 0 {
		t.Errorf("bytes = %d, esperava 0", n)
	}
	if err == nil {
		t.Fatal("a leitura deveria ter sido cortada")
	}
	if !travou() {
		t.Error("o corte precisa ser identificado como falha da fonte, não do cliente")
	}
	if d := time.Since(inicio); d > 2*time.Second {
		t.Errorf("demorou %s para cortar", d)
	}
}

// O prazo vale só até o primeiro byte: depois disso um filme pode levar horas, e cortar
// por lentidão derrubaria quem assiste de verdade numa rede ruim.
func TestVigiaNaoCortaDepoisDoPrimeiroByte(t *testing.T) {
	original := prazoPrimeiroByteParaTeste(80 * time.Millisecond)
	defer prazoPrimeiroByteParaTeste(original)

	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()

	// Entrega um byte imediatamente e depois fica em silêncio bem além do prazo.
	lento := &leitorLento{primeiro: true, ctx: ctx, pausa: 300 * time.Millisecond}
	corpo, parar, travou := vigiarPrimeiroByte(lento, cancelar)
	defer parar()

	n, err := io.Copy(io.Discard, corpo)
	if n != 1 {
		t.Errorf("bytes = %d, esperava 1", n)
	}
	if travou() {
		t.Error("o vigia cortou depois do primeiro byte — não deveria")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		t.Logf("erro final (aceitável): %v", err)
	}
}

type leitorLento struct {
	primeiro bool
	ctx      context.Context
	pausa    time.Duration
}

func (l *leitorLento) Read(p []byte) (int, error) {
	if l.primeiro {
		l.primeiro = false
		p[0] = 'X'
		return 1, nil
	}
	select {
	case <-time.After(l.pausa):
		return 0, io.EOF
	case <-l.ctx.Done():
		return 0, l.ctx.Err()
	}
}

// erroDeEscrita simula o cliente indo embora no meio da transmissão: o player fecha, e a
// escrita para ele falha com "broken pipe".
type escritorQueFalha struct{ apos int }

func (e *escritorQueFalha) Write(p []byte) (int, error) {
	if e.apos <= 0 {
		return 0, errors.New("write: broken pipe")
	}
	e.apos--
	return len(p), nil
}

// A falha ao escrever PARA o cliente precisa ser distinguível da falha ao ler da fonte.
//
// Sem essa distinção, um espectador que fecha o player ou dá seek era contado como erro de
// entrega — foi o que inflou a contagem de falhas do painel e escondeu os problemas reais.
func TestFalhaAoEscreverParaOClienteEhMarcada(t *testing.T) {
	// Zero escritas bem-sucedidas: a primeira já falha, como um player que fechou.
	destino := &escritorDoCliente{destino: &escritorQueFalha{apos: 0}}

	_, err := io.Copy(destino, strings.NewReader(strings.Repeat("x", 100000)))
	if err == nil {
		t.Fatal("esperava erro de escrita")
	}
	if !destino.falhou {
		t.Error("a falha do lado do cliente não foi marcada")
	}
}

// Quando a escrita ao cliente vai bem, o marcador precisa continuar limpo — senão toda
// transmissão normal seria classificada como cliente desconectado.
func TestEscritaBemSucedidaNaoMarcaFalha(t *testing.T) {
	destino := &escritorDoCliente{destino: io.Discard}

	n, err := io.Copy(destino, strings.NewReader("conteudo de teste"))
	if err != nil || n == 0 {
		t.Fatalf("cópia falhou: n=%d err=%v", n, err)
	}
	if destino.falhou {
		t.Error("marcou falha numa escrita bem-sucedida")
	}
}

// leitorQueParaNoMeio entrega alguns bytes e depois emudece para sempre.
//
// É a fonte que engasga sob carga: ela responde, começa a enviar, e simplesmente para. Do
// lado do espectador o filme congela; do lado do servidor a conexão fica presa.
type leitorQueParaNoMeio struct {
	ctx      context.Context
	restante int
}

func (l *leitorQueParaNoMeio) Read(p []byte) (int, error) {
	if l.restante > 0 {
		n := len(p)
		if n > l.restante {
			n = l.restante
		}
		for i := range p[:n] {
			p[i] = 'v'
		}
		l.restante -= n
		return n, nil
	}
	<-l.ctx.Done()
	return 0, l.ctx.Err()
}

// TestVigiaCortaFonteQueParaNoMeio é a guarda do defeito que travava reproduções.
//
// O vigia cobria só o primeiro byte. Depois dele, uma fonte que enviasse um byte e
// travasse deixava a conexão presa PARA SEMPRE — segurando uma vaga do limite da
// credencial e uma conexão na fonte.
//
// O estrago não ficava no espectador que travou: as vagas presas se acumulavam até encher
// o limite da credencial, e daí o cliente INTEIRO passava a receber "limite de reproduções
// atingido". O player, diante disso, reinicia do começo — e o relato que chega não é "um
// filme travou", é "os vídeos reiniciam sozinhos depois de um tempo".
func TestVigiaCortaFonteQueParaNoMeio(t *testing.T) {
	original := prazoSemDadosParaTeste(50 * time.Millisecond)
	defer prazoSemDadosParaTeste(original)

	ctx, cancelar := context.WithCancel(context.Background())
	fonte := &leitorQueParaNoMeio{ctx: ctx, restante: 1024}
	corpo, parar, travou := vigiar(fonte, cancelar, nil)
	defer parar()

	inicio := time.Now()
	n, err := io.Copy(io.Discard, corpo)

	if n != 1024 {
		t.Errorf("bytes entregues = %d, esperava 1024", n)
	}
	if err == nil {
		t.Fatal("a leitura deveria ter sido cortada depois de a fonte emudecer")
	}
	if !travou() {
		t.Error("o corte precisa ser identificado como falha da fonte, não do cliente")
	}
	if decorrido := time.Since(inicio); decorrido > 2*time.Second {
		t.Errorf("demorou %s para cortar; o prazo era 50ms", decorrido)
	}
}

// TestVigiaNaoCortaClientePausado é o contrapeso do teste acima.
//
// Com o player pausado, o cliente para de aceitar bytes, a escrita bloqueia, e a leitura na
// fonte para junto. Do lado de fora é IDÊNTICO a uma fonte travada — e cortar aí derrubaria
// quem só apertou pause.
//
// A diferença está em onde a cópia está parada: pausado, ela está bloqueada escrevendo para
// o cliente. É isso que o vigia consulta antes de cortar.
func TestVigiaNaoCortaClientePausado(t *testing.T) {
	original := prazoSemDadosParaTeste(30 * time.Millisecond)
	defer prazoSemDadosParaTeste(original)

	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()

	fonte := &leitorQueParaNoMeio{ctx: ctx, restante: 512}
	// O escritor está sempre "no meio de uma escrita": é o player pausado.
	pausado := func() bool { return true }
	corpo, parar, travou := vigiar(fonte, cancelar, pausado)
	defer parar()

	// Lê o que a fonte tem e depois espera bem mais que o prazo. Nada pode ser cortado.
	lido, _ := io.ReadAll(io.LimitReader(corpo, 512))
	if len(lido) != 512 {
		t.Fatalf("leu %d bytes, esperava 512", len(lido))
	}
	time.Sleep(150 * time.Millisecond)

	if travou() {
		t.Fatal("o vigia cortou um cliente pausado; ele só pode cortar fonte parada")
	}
	if ctx.Err() != nil {
		t.Fatal("o contexto foi cancelado com o cliente apenas pausado")
	}
}

// TestVigiaPararEncerraARonda: a ronda não pode sobreviver à requisição.
//
// Uma goroutine por reprodução que nunca termina é vazamento — e num servidor de vídeo isso
// se acumula depressa.
func TestVigiaPararEncerraARonda(t *testing.T) {
	_, cancelar := context.WithCancel(context.Background())
	defer cancelar()

	_, parar, _ := vigiar(strings.NewReader("conteudo"), cancelar, nil)
	parar()
	// Parar duas vezes não pode entrar em pânico: o caminho de erro chama o defer e o
	// caminho normal chama explicitamente.
	parar()
}
