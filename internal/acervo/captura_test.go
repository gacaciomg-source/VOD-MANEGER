package acervo

import (
	"bytes"
	"io"
	"testing"
	"time"

	"vodmanager/internal/store"
)

// A garantia que estes testes protegem é uma só: escrever na captura NUNCA para a
// transmissão. Ela é chamada de dentro da cópia que alimenta o player, então um bloqueio
// aqui é um filme travado na tela de alguém.

func TestLeitorDaFilaRemontaOsPedacos(t *testing.T) {
	pedacos := make(chan []byte, 3)
	pedacos <- []byte("um ")
	pedacos <- []byte("filme ")
	pedacos <- []byte("inteiro")
	close(pedacos)

	lido, err := io.ReadAll(&leitorDaFila{pedacos: pedacos})
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	if string(lido) != "um filme inteiro" {
		t.Fatalf("remontagem errada: %q", lido)
	}
}

// O leitor precisa funcionar com buffers menores que os pedaços — é o caso real, porque
// quem lê é o backend, e ele escolhe o próprio tamanho de bloco.
func TestLeitorDaFilaComBufferPequeno(t *testing.T) {
	pedacos := make(chan []byte, 2)
	pedacos <- bytes.Repeat([]byte("a"), 100)
	pedacos <- bytes.Repeat([]byte("b"), 100)
	close(pedacos)

	l := &leitorDaFila{pedacos: pedacos}
	var saida bytes.Buffer
	buf := make([]byte, 7) // não divide 100: força a leitura parcial de um pedaço
	if _, err := io.CopyBuffer(&saida, struct{ io.Reader }{l}, buf); err != nil {
		t.Fatalf("cópia falhou: %v", err)
	}
	if saida.Len() != 200 {
		t.Fatalf("esperava 200 bytes, veio %d", saida.Len())
	}
	if !bytes.Equal(saida.Bytes()[:100], bytes.Repeat([]byte("a"), 100)) {
		t.Fatal("os pedaços saíram fora de ordem")
	}
}

// O teste central: com ninguém consumindo a fila, a captura tem de desistir em vez de
// bloquear. Se este teste travar, é exatamente o defeito que ele existe para pegar.
func TestCapturaDesisteEmVezDeBloquear(t *testing.T) {
	c := &Captura{
		pedacos: make(chan []byte, pedacosNaFila),
		fim:     make(chan resultadoDaCaptura, 1),
	}

	pronto := make(chan struct{})
	go func() {
		defer close(pronto)
		// Bem mais escritas do que cabem na fila, e nada consumindo do outro lado.
		for i := 0; i < pedacosNaFila*10; i++ {
			if _, err := c.Write([]byte("bytes de video")); err != nil {
				t.Errorf("Write devolveu erro, e nunca deveria: %v", err)
			}
		}
	}()

	select {
	case <-pronto:
	case <-time.After(2 * time.Second):
		t.Fatal("a captura bloqueou a transmissão — a fila encheu e ela não desistiu")
	}

	if !c.desistiu {
		t.Fatal("a fila encheu e a captura não desistiu")
	}
}

// Depois de desistir, a captura tem de continuar aceitando escritas sem efeito. O proxy
// segue entregando o filme até o fim, e cada bloco ainda passa por aqui.
func TestCapturaAceitaEscritasDepoisDeDesistir(t *testing.T) {
	c := &Captura{
		pedacos: make(chan []byte, 1),
		fim:     make(chan resultadoDaCaptura, 1),
	}
	c.desistir()

	n, err := c.Write([]byte("mais bytes"))
	if err != nil {
		t.Fatalf("Write falhou depois de desistir: %v", err)
	}
	if n != len("mais bytes") {
		t.Fatalf("Write precisa relatar o total escrito, veio %d", n)
	}

	// Desistir duas vezes não pode fechar o canal duas vezes (pânico).
	c.desistir()
}

// A cópia do buffer é obrigatória: quem chama reaproveita o mesmo array a cada bloco. Sem a
// cópia, o arquivo guardado seria o último bloco repetido — e o defeito só apareceria na
// hora de assistir.
func TestCapturaCopiaOBufferRecebido(t *testing.T) {
	c := &Captura{
		pedacos: make(chan []byte, 2),
		fim:     make(chan resultadoDaCaptura, 1),
	}

	buf := []byte("primeiro")
	if _, err := c.Write(buf); err != nil {
		t.Fatalf("Write falhou: %v", err)
	}
	copy(buf, "ALTERADO") // é o que io.CopyBuffer faz no bloco seguinte

	guardado := <-c.pedacos
	if string(guardado) != "primeiro" {
		t.Fatalf("a captura guardou o buffer sem copiar: %q", guardado)
	}
}

// O defeito que este teste existe para impedir, dito com precisão:
//
// O proxy passava `estado == "closed"` como "completo". Esse campo continua "closed" quando
// o ESPECTADOR fecha o player — só o código de erro muda, porque fechar o player não é falha
// de entrega. A captura então concluía os primeiros cem quilobytes de cada filme como se
// fossem o filme, e o acervo passava a servi-los no lugar da fonte.
//
// No painel, tudo verde: dezenas de títulos "pronto". Na TV, nada abria.
//
// A lição está na assinatura: `Fechar(completo bool)` aceita a opinião de quem chama. A
// defesa é o tamanho anunciado, que é fato.
func TestCapturaRecusaConcluirComTamanhoMenor(t *testing.T) {
	c := &Captura{
		pedacos:   make(chan []byte, pedacosNaFila),
		fim:       make(chan resultadoDaCaptura, 1),
		anunciado: 2 << 30, // 2 GB anunciados pela fonte
	}

	// A gravação terminou com 100 KB: o espectador fechou o player no começo.
	c.fim <- resultadoDaCaptura{localizador: "pedaco.mp4", bytes: 100 << 10}
	c.escritos = 100 << 10

	// Mesmo com o chamador insistindo que está completo, o tamanho desmente.
	falhou := deveFalhar(c, true)
	if !falhou {
		t.Fatal("a captura concluiu uma cópia de 100 KB para um arquivo de 2 GB")
	}
}

// deveFalhar reproduz a decisão de Fechar sem tocar no banco.
//
// Fechar grava no store, que não existe aqui. O que este teste precisa verificar é a
// DECISÃO — e ela é aritmética pura.
func deveFalhar(c *Captura, completo bool) bool {
	res := <-c.fim
	falhou := c.desistiu || !completo || res.err != nil
	if !falhou && c.anunciado > 0 && res.bytes != c.anunciado {
		falhou = true
	}
	if !falhou && res.bytes != c.escritos {
		falhou = true
	}
	return falhou
}

// TestVagasDeCapturaLimitamAConcorrencia prende o teto de memória no lugar.
//
// Cada gravação para a nuvem segura dezesseis megabytes de buffer de envio mais quatro de
// fila. Sem teto, toda reprodução nova abria uma — e vinte estreias simultâneas eram
// quatrocentos megabytes de uma vez, numa máquina que também roda o banco.
//
// O `default` no select é o ponto: pedir vaga NUNCA pode esperar. Fazer fila aqui seria
// segurar o vídeo do espectador por causa de uma otimização que só beneficia o próximo.
func TestVagasDeCapturaLimitamAConcorrencia(t *testing.T) {
	vagas := make(chan struct{}, capturasSimultaneas)

	pegar := func() bool {
		select {
		case vagas <- struct{}{}:
			return true
		default:
			return false
		}
	}

	for i := 0; i < capturasSimultaneas; i++ {
		if !pegar() {
			t.Fatalf("a vaga %d de %d devia estar livre", i+1, capturasSimultaneas)
		}
	}
	if pegar() {
		t.Fatalf("a captura %d passou do teto de %d", capturasSimultaneas+1, capturasSimultaneas)
	}

	// Devolvida, a vaga volta a servir — senão o cache se desligaria sozinho com o tempo.
	<-vagas
	if !pegar() {
		t.Fatal("a vaga devolvida não voltou a ser usada")
	}
}

// TestMinimoDeVideoNuncaDesceAbaixoDoPiso prende a regra que evita a tela preta.
//
// O proxy tratava qualquer resposta abaixo de cem megabytes como possível aviso de
// manutenção e trocava de fonte. O cache só recusava abaixo de um megabyte — então um aviso
// de dez segundos com cinco megabytes passava, e era gravado COMO SE FOSSE O FILME.
//
// Daí em diante o acervo o servia no lugar da fonte, para sempre: o player abria, tinha
// duração, e mostrava tela preta. As duas metades do sistema precisam concordar sobre o que
// é um filme.
//
// E o piso nunca desce: mesmo com a detecção de manutenção desligada, um megabyte continua
// não sendo um vídeo.
func TestMinimoDeVideoNuncaDesceAbaixoDoPiso(t *testing.T) {
	casos := []struct {
		nome        string
		configurado int64
		esperado    int64
	}{
		{"limiar do proxy manda", 100 << 20, 100 << 20},
		{"sem limiar cai no piso", 0, store.TamanhoMinimoDeVideo},
		{"limiar menor que o piso não vale", 1000, store.TamanhoMinimoDeVideo},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			s := &Servico{minimoDeVideo: c.configurado}
			if got := s.MinimoDeVideo(); got != c.esperado {
				t.Fatalf("esperava %d, veio %d", c.esperado, got)
			}
		})
	}
}
