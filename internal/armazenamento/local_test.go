package armazenamento

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func novoLocalDeTeste(t *testing.T) *Local {
	t.Helper()
	l, err := NovoLocal(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NovoLocal: %v", err)
	}
	return l
}

func TestGuardarEAbrirDevolveOMesmoConteudo(t *testing.T) {
	l := novoLocalDeTeste(t)
	ctx := context.Background()
	original := []byte("os bytes de um filme, em miniatura")

	loc, err := l.Guardar(ctx, "Interestelar (2014).mp4", bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatalf("Guardar: %v", err)
	}
	if loc.Bytes != int64(len(original)) {
		t.Errorf("Bytes = %d, queria %d", loc.Bytes, len(original))
	}

	r, err := l.Abrir(ctx, loc.Localizador, 0)
	if err != nil {
		t.Fatalf("Abrir: %v", err)
	}
	defer r.Close()
	lido, _ := io.ReadAll(r)
	if !bytes.Equal(lido, original) {
		t.Errorf("leu %q, queria %q", lido, original)
	}
}

// TestAbrirComDeslocamento cobre o caminho que o vídeo usa o tempo todo.
//
// Arrastar a barra de progresso vira um Range, e um Range vira uma leitura a partir de um
// ponto. Se este caminho estivesse errado, o filme tocaria do começo — e o espectador
// pensaria que o player travou.
func TestAbrirComDeslocamento(t *testing.T) {
	l := novoLocalDeTeste(t)
	ctx := context.Background()
	original := []byte("0123456789")

	loc, err := l.Guardar(ctx, "trecho.mp4", bytes.NewReader(original), 0)
	if err != nil {
		t.Fatalf("Guardar: %v", err)
	}

	r, err := l.Abrir(ctx, loc.Localizador, 4)
	if err != nil {
		t.Fatalf("Abrir: %v", err)
	}
	defer r.Close()
	lido, _ := io.ReadAll(r)
	if string(lido) != "456789" {
		t.Errorf("leu %q, queria \"456789\"", lido)
	}
}

// TestLocalizadorNaoEscapaDaPasta é a guarda que mais importa deste pacote.
//
// O localizador vem do BANCO, e o banco recebe dados que passaram por upload, por nome de
// arquivo de fonte e por URL. Se um `../` fosse aceito, a limpeza de cache — que é
// automática e roda como serviço — viraria uma forma de apagar arquivos do sistema.
//
// Vale para leitura e para remoção, não só para gravação: quem grava e quem lê podem ser
// versões diferentes do código, e o banco sobrevive às duas.
func TestLocalizadorNaoEscapaDaPasta(t *testing.T) {
	l := novoLocalDeTeste(t)
	ctx := context.Background()

	// Um arquivo fora da pasta, que nenhuma das tentativas pode tocar.
	fora := filepath.Join(filepath.Dir(l.Raiz()), "nao-me-toque.txt")
	if err := os.WriteFile(fora, []byte("intacto"), 0o600); err != nil {
		t.Fatalf("preparando o arquivo de fora: %v", err)
	}

	tentativas := []string{
		"../nao-me-toque.txt",
		"../../etc/passwd",
		"sub/../../nao-me-toque.txt",
		"",
	}

	for _, alvo := range tentativas {
		t.Run("abrir "+alvo, func(t *testing.T) {
			if _, err := l.Abrir(ctx, alvo, 0); err == nil {
				t.Fatalf("Abrir(%q) foi aceito e não deveria", alvo)
			}
		})
		t.Run("apagar "+alvo, func(t *testing.T) {
			// Apagar o que não existe não é erro — então aqui o que se confere não é o
			// retorno, e sim o arquivo continuar de pé.
			_ = l.Apagar(ctx, alvo)
			if _, err := os.Stat(fora); err != nil {
				t.Fatalf("o arquivo de fora foi apagado por Apagar(%q)", alvo)
			}
		})
	}
}

// TestGuardarNaoSobrescreveNomeIgual: dois filmes com o mesmo título existem o tempo todo.
//
// Sem o sufixo aleatório, o segundo sobrescreveria o primeiro — e o banco continuaria
// apontando os dois para o mesmo arquivo, entregando o filme errado a metade dos
// espectadores.
func TestGuardarNaoSobrescreveNomeIgual(t *testing.T) {
	l := novoLocalDeTeste(t)
	ctx := context.Background()

	a, err := l.Guardar(ctx, "Duna.mp4", strings.NewReader("primeiro"), 0)
	if err != nil {
		t.Fatalf("Guardar: %v", err)
	}
	b, err := l.Guardar(ctx, "Duna.mp4", strings.NewReader("segundo"), 0)
	if err != nil {
		t.Fatalf("Guardar: %v", err)
	}
	if a.Localizador == b.Localizador {
		t.Fatalf("os dois arquivos ficaram no mesmo lugar: %q", a.Localizador)
	}

	r, _ := l.Abrir(ctx, a.Localizador, 0)
	defer r.Close()
	if lido, _ := io.ReadAll(r); string(lido) != "primeiro" {
		t.Errorf("o primeiro arquivo virou %q", lido)
	}
}

// TestGuardarInterrompidoNaoDeixaArquivoValido.
//
// Um download que morre pela metade não pode ficar no lugar do bom com nome de arquivo
// válido: quem o lesse depois entregaria meio filme, e o banco diria "pronto".
func TestGuardarInterrompidoNaoDeixaArquivoValido(t *testing.T) {
	l := novoLocalDeTeste(t)
	ctx := context.Background()

	_, err := l.Guardar(ctx, "cortado.mp4", &leitorQueFalha{ate: 5}, 0)
	if err == nil {
		t.Fatal("Guardar aceitou um conteúdo que falhou no meio")
	}

	restantes, _ := os.ReadDir(l.Raiz())
	if len(restantes) != 0 {
		nomes := make([]string, 0, len(restantes))
		for _, e := range restantes {
			nomes = append(nomes, e.Name())
		}
		t.Errorf("sobrou lixo na pasta: %v", nomes)
	}
}

// TestGuardarRespeitaOCancelamento: um espectador que desiste, ou o serviço parando, não
// pode deixar a cópia correndo até o fim de um arquivo de 2 GB.
func TestGuardarRespeitaOCancelamento(t *testing.T) {
	l := novoLocalDeTeste(t)
	ctx, cancelar := context.WithCancel(context.Background())
	cancelar()

	_, err := l.Guardar(ctx, "abandonado.mp4", strings.NewReader("qualquer coisa"), 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("erro = %v, queria context.Canceled", err)
	}
}

func TestAbrirInexistenteDizQueNaoExiste(t *testing.T) {
	l := novoLocalDeTeste(t)
	_, err := l.Abrir(context.Background(), "nunca-existiu.mp4", 0)
	if !errors.Is(err, ErrNaoEncontrado) {
		t.Fatalf("erro = %v, queria ErrNaoEncontrado", err)
	}
}

// TestNomeSeguroLimpaSugestao: os títulos do catálogo trazem barras, acentos e dois-pontos.
func TestNomeSeguroLimpaSugestao(t *testing.T) {
	casos := []struct{ entrada, proibido string }{
		{"../../etc/passwd", "/"},
		{"Filme: A Missão / Parte 2.mkv", "/"},
		{"nome\\com\\barra.mp4", "\\"},
	}
	for _, c := range casos {
		nome := nomeSeguro(c.entrada)
		if strings.Contains(nome, c.proibido) {
			t.Errorf("nomeSeguro(%q) = %q, ainda contém %q", c.entrada, nome, c.proibido)
		}
		if strings.Contains(nome, "..") {
			t.Errorf("nomeSeguro(%q) = %q, ainda contém \"..\"", c.entrada, nome)
		}
	}
}

// leitorQueFalha entrega alguns bytes e então quebra, como uma conexão que cai.
type leitorQueFalha struct {
	ate       int
	entregues int
}

func (l *leitorQueFalha) Read(p []byte) (int, error) {
	if l.entregues >= l.ate {
		return 0, errors.New("a conexão com a fonte caiu")
	}
	n := len(p)
	if restante := l.ate - l.entregues; n > restante {
		n = restante
	}
	for i := range p[:n] {
		p[i] = 'x'
	}
	l.entregues += n
	return n, nil
}
