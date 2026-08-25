package edge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInicioPedido lê a posição que o player pediu.
func TestInicioPedido(t *testing.T) {
	casos := map[string]int64{
		"bytes=734000000-":   734000000,
		"bytes=1000-2000":    1000,
		"bytes=0-":           0, // pedir desde o começo não precisa de correção
		"":                   0,
		"bytes=-500":         0, // sufixo: players de vídeo não usam
		"bytes=0-99,200-299": 0, // múltiplas faixas: idem
		"segundos=10-":       0,
		"bytes=abc-":         0,
	}
	for cabecalho, quer := range casos {
		if got := inicioPedido(cabecalho); got != quer {
			t.Errorf("inicioPedido(%q) = %d, queria %d", cabecalho, got, quer)
		}
	}
}

// TestFonteQueIgnoraORangeEhCorrigida é a guarda do defeito que fazia o filme voltar ao
// começo.
//
// O player não baixa o filme inteiro de uma vez: quando o buffer acaba, ele pede a
// continuação. Uma fonte que ignora esse pedido devolve 200 e o arquivo INTEIRO, do byte
// zero.
//
// Repassando isso, o player pedia a continuação e recebia o começo do filme — com um 200
// dizendo "aqui está tudo". Para ele, é um arquivo novo começando: volta ao início. E como
// a fonte se comporta igual toda vez, o mesmo filme travava sempre no mesmo lugar.
func TestFonteQueIgnoraORangeEhCorrigida(t *testing.T) {
	const total = 1000
	const inicio = 400

	conteudo := strings.Repeat("A", inicio) + strings.Repeat("B", total-inicio)

	r := httptest.NewRequest(http.MethodGet, "/movie/u/s/1.mp4", nil)
	r.Header.Set("Range", "bytes=400-")
	w := httptest.NewRecorder()

	// A fonte ignora o Range: 200 com o arquivo inteiro.
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: total,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(conteudo)),
	}

	corpo, status, corrigido := corpoNaPosicaoPedida(w, r, resp)

	if !corrigido {
		t.Fatal("a correção não foi aplicada")
	}
	if status != http.StatusPartialContent {
		t.Errorf("status = %d, queria 206", status)
	}
	if faixa := w.Header().Get("Content-Range"); faixa != "bytes 400-999/1000" {
		t.Errorf("Content-Range = %q, queria \"bytes 400-999/1000\"", faixa)
	}
	if tam := w.Header().Get("Content-Length"); tam != "600" {
		t.Errorf("Content-Length = %q, queria \"600\"", tam)
	}

	// O corpo tem que começar na posição pedida, e não no início do arquivo.
	restante, _ := io.ReadAll(corpo)
	if len(restante) != total-inicio {
		t.Fatalf("entregou %d bytes, queria %d", len(restante), total-inicio)
	}
	if strings.Contains(string(restante), "A") {
		t.Error("o corpo ainda contém o trecho anterior à posição pedida")
	}
}

// TestFonteQueRespeitaORangeNaoEhTocada: quando a fonte já respondeu 206, não há o que
// corrigir — e mexer seria estragar o que estava certo.
func TestFonteQueRespeitaORangeNaoEhTocada(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/movie/u/s/1.mp4", nil)
	r.Header.Set("Range", "bytes=400-")
	w := httptest.NewRecorder()

	resp := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: 600,
		Header:        http.Header{"Content-Range": {"bytes 400-999/1000"}},
		Body:          io.NopCloser(strings.NewReader(strings.Repeat("B", 600))),
	}

	_, status, corrigido := corpoNaPosicaoPedida(w, r, resp)
	if corrigido {
		t.Error("corrigiu uma resposta que já estava correta")
	}
	if status != http.StatusPartialContent {
		t.Errorf("status = %d, queria 206", status)
	}
}

// TestSemRangeNaoCorrige: reprodução do começo não pede posição nenhuma.
func TestSemRangeNaoCorrige(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/movie/u/s/1.mp4", nil)
	w := httptest.NewRecorder()

	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: 1000,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(strings.Repeat("A", 1000))),
	}

	_, status, corrigido := corpoNaPosicaoPedida(w, r, resp)
	if corrigido {
		t.Error("corrigiu uma reprodução que começou do zero")
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, queria 200", status)
	}
}

// TestSemTamanhoConhecidoNaoInventa: sem o tamanho total não há Content-Range válido, e um
// inválido confunde o player mais que a resposta original.
func TestSemTamanhoConhecidoNaoInventa(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/movie/u/s/1.mp4", nil)
	r.Header.Set("Range", "bytes=400-")
	w := httptest.NewRecorder()

	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: -1,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader("qualquer coisa")),
	}

	_, _, corrigido := corpoNaPosicaoPedida(w, r, resp)
	if corrigido {
		t.Error("corrigiu sem saber o tamanho total")
	}
}
