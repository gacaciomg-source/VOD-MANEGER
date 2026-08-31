package armazenamento

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A retomada existe por uma razão medida: as fontes deste sistema cortam a entrega no meio o
// tempo todo. Sem ela, um filme de dois gigabytes cortado aos oitenta por cento custa dois
// gigabytes de novo — e a tentativa seguinte tem a mesma chance de ser cortada no mesmo lugar.
func TestRetomadaContinuaDeOndeParou(t *testing.T) {
	disco, err := NovoLocal(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NovoLocal: %v", err)
	}
	ctx := context.Background()
	const nome = "filme.mp4"

	// Nada gravado ainda.
	if n := disco.ParcialDe(nome); n != 0 {
		t.Fatalf("sem parcial, devia ser zero; veio %d", n)
	}

	// Primeira metade. O parcial FICA — é o que a retomada aproveita.
	if _, err := disco.Continuar(ctx, nome, strings.NewReader("01234"), 10, 0); err != nil {
		// Continuar finaliza ao terminar de ler, então esta chamada já renomeia. Para
		// simular a interrupção, escrevemos o parcial direto no teste abaixo.
		t.Fatalf("Continuar: %v", err)
	}

	// A segunda parte, sobre um parcial deixado por uma tentativa anterior.
	const outro = "serie.mp4"
	if err := escreverParcial(disco, outro, "abcde"); err != nil {
		t.Fatalf("preparando o parcial: %v", err)
	}
	if n := disco.ParcialDe(outro); n != 5 {
		t.Fatalf("o parcial devia ter 5 bytes; veio %d", n)
	}

	loc, err := disco.Continuar(ctx, outro, strings.NewReader("fghij"), 10, 5)
	if err != nil {
		t.Fatalf("Continuar retomando: %v", err)
	}
	if loc.Bytes != 10 {
		t.Fatalf("o total devia contar os bytes antigos e os novos: esperava 10, veio %d", loc.Bytes)
	}

	corpo, err := disco.Abrir(ctx, loc.Localizador, 0)
	if err != nil {
		t.Fatalf("Abrir: %v", err)
	}
	defer corpo.Close()
	lido, _ := io.ReadAll(corpo)
	if string(lido) != "abcdefghij" {
		t.Fatalf("as duas metades deviam formar o arquivo inteiro; veio %q", lido)
	}
}

// DescartarParcial existe para o caso em que a fonte ignora a faixa pedida. Continuar sobre
// bytes que não correspondem produziria um vídeo corrompido que PARECE completo — tamanho
// certo, estado pronto, imagem quebrada no meio. É a única falha aqui que ninguém descobriria.
func TestDescartarParcialLimpaAMetadeInutil(t *testing.T) {
	disco, err := NovoLocal(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NovoLocal: %v", err)
	}
	const nome = "descartavel.mp4"
	if err := escreverParcial(disco, nome, "lixo"); err != nil {
		t.Fatalf("preparando o parcial: %v", err)
	}
	if disco.ParcialDe(nome) == 0 {
		t.Fatal("o parcial devia existir antes do descarte")
	}
	disco.DescartarParcial(nome)
	if n := disco.ParcialDe(nome); n != 0 {
		t.Fatalf("o parcial devia ter sido apagado; sobraram %d bytes", n)
	}
}

// escreverParcial simula uma cópia interrompida por uma tentativa anterior.
func escreverParcial(l *Local, nome, conteudo string) error {
	return os.WriteFile(filepath.Join(l.Raiz(), nomeEstavel(nome)+".parcial"),
		[]byte(conteudo), 0o640)
}
