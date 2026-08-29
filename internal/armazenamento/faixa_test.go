package armazenamento

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// O defeito que este teste prende, dito por inteiro:
//
// Pedimos ao Drive o filme a partir do byte N. Ele responde 200 — ou seja, IGNORA a faixa e
// manda do começo. Nós devolvíamos aquele corpo como se começasse em N, e o player recebia o
// início do filme quando tinha pedido a continuação.
//
// Como o player confia no que pediu, ele volta ao início. E como a fonte se comporta igual
// toda vez, o filme trava sempre no mesmo lugar — que é exatamente o relato que motivou isto.
//
// Já havia sido corrigido para as fontes. Voltou pelo acervo na nuvem, que não tinha a mesma
// proteção: o mesmo defeito por um caminho novo.
type fechavel struct{ io.Reader }

func (fechavel) Close() error { return nil }

func TestPosicionarQuandoAFaixaFoiIgnorada(t *testing.T) {
	const conteudo = "0123456789"

	t.Run("200 com deslocamento: descarta o começo", func(t *testing.T) {
		corpo, err := posicionarSeIgnorouAFaixa(
			fechavel{strings.NewReader(conteudo)}, http.StatusOK, 4, "conta")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		lido, _ := io.ReadAll(corpo)
		if string(lido) != "456789" {
			t.Fatalf("devia começar no byte 4; veio %q", lido)
		}
	})

	t.Run("206: o outro lado respeitou, não se toca", func(t *testing.T) {
		corpo, err := posicionarSeIgnorouAFaixa(
			fechavel{strings.NewReader("456789")}, http.StatusPartialContent, 4, "conta")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		lido, _ := io.ReadAll(corpo)
		if string(lido) != "456789" {
			t.Fatalf("um 206 já vem posicionado e não pode ser cortado de novo; veio %q", lido)
		}
	})

	t.Run("sem deslocamento: nada a fazer", func(t *testing.T) {
		corpo, err := posicionarSeIgnorouAFaixa(
			fechavel{strings.NewReader(conteudo)}, http.StatusOK, 0, "conta")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		lido, _ := io.ReadAll(corpo)
		if string(lido) != conteudo {
			t.Fatalf("sem deslocamento o corpo sai inteiro; veio %q", lido)
		}
	})

	t.Run("arquivo menor que o deslocamento: falha em vez de servir vazio", func(t *testing.T) {
		// Servir um corpo vazio faria o player esperar bytes que nunca viriam — pior que
		// um erro, porque não diz nada a ninguém.
		_, err := posicionarSeIgnorouAFaixa(
			fechavel{strings.NewReader("012")}, http.StatusOK, 100, "conta")
		if err == nil {
			t.Fatal("posicionar além do fim do arquivo devia falhar")
		}
	})
}
