package api

import "testing"

// TestEnderecoAceitoPeloGoogle fixa a regra que produziu um erro sem contexto.
//
// O Google recusa retorno em HTTP puro com "Erro 400: invalid_request" — uma tela dele, sem
// dizer o motivo, depois de a pessoa já ter criado o projeto, ativado a API e preenchido
// tudo no painel. Conferir antes troca isso por uma frase que diz o que fazer.
func TestEnderecoAceitoPeloGoogle(t *testing.T) {
	casos := []struct {
		endereco string
		aceito   bool
	}{
		{"https://vod.exemplo.com/api/v1/nuvens/oauth/retorno", true},
		{"http://vod.exemplo.com/api/v1/nuvens/oauth/retorno", false},
		{"http://198.51.100.10:8080/api/v1/nuvens/oauth/retorno", false},
		// localhost é a exceção que o próprio Google abre: ali não há rede entre o
		// navegador e o servidor para alguém interceptar.
		{"http://localhost:8080/api/v1/nuvens/oauth/retorno", true},
		{"http://127.0.0.1:8080/api/v1/nuvens/oauth/retorno", true},
		{"ftp://vod.exemplo.com/x", false},
		{"", false},
	}
	for _, c := range casos {
		if got := enderecoAceitoPeloGoogle(c.endereco); got != c.aceito {
			t.Errorf("enderecoAceitoPeloGoogle(%q) = %v, queria %v", c.endereco, got, c.aceito)
		}
	}
}
