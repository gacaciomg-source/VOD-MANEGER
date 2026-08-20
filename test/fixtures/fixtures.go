// Package fixtures localiza e lê os fixtures de teste da raiz do repositório.
//
// Os fixtures ficam em testdata/ na raiz — e não espalhados por pacote — porque é lá que
// você vai colocar as amostras anonimizadas das suas fontes, e porque o teste de
// detecção de credenciais precisa varrer tudo num lugar só.
package fixtures

import (
	"os"
	"path/filepath"
	"testing"
)

// Root devolve a raiz do repositório, subindo até encontrar go.mod.
func Root(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("não encontrei a raiz do repositório (go.mod)")
	return ""
}

// Dir devolve o caminho de testdata/ na raiz do repositório.
func Dir(t *testing.T) string {
	t.Helper()
	return filepath.Join(Root(t), "testdata")
}

// Read lê um fixture pelo caminho relativo a testdata/.
func Read(t *testing.T, rel string) []byte {
	t.Helper()
	caminho := filepath.Join(Dir(t), filepath.FromSlash(rel))
	data, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("lendo fixture %s: %v", rel, err)
	}
	return data
}

// Open abre um fixture para leitura em streaming.
func Open(t *testing.T, rel string) *os.File {
	t.Helper()
	caminho := filepath.Join(Dir(t), filepath.FromSlash(rel))
	f, err := os.Open(caminho)
	if err != nil {
		t.Fatalf("abrindo fixture %s: %v", rel, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
