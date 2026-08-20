package integration

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"vodmanager/internal/cryptobox"
	"vodmanager/internal/edge"
)

// chaveDeTesteFixa mantém a mesma chave em todo o teste, para que um autenticador criado
// no teste produza as mesmas assinaturas do que roda no servidor.
var chaveDeTesteFixa []byte

func chaveDeTeste(t *testing.T) []byte {
	t.Helper()
	if chaveDeTesteFixa != nil {
		return chaveDeTesteFixa
	}
	encoded, err := cryptobox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	chave, err := cryptobox.ParseKey(encoded)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	chaveDeTesteFixa = chave
	return chaveDeTesteFixa
}

// chiComStreaming monta um roteador só com as rotas do plano de dados.
func chiComStreaming(p *edge.Proxy) http.Handler {
	r := chi.NewRouter()
	p.Rotas(r, false)
	return r
}
