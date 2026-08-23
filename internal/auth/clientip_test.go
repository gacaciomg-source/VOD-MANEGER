package auth

import (
	"net/http/httptest"
	"testing"
)

// TestClientIPSoConfiaNoProxyLocal fixa a fronteira que separa "o proxy me disse" de
// "o cliente me disse".
//
// X-Forwarded-For é escrito pelo cliente e ACRESCENTADO pelo proxy — o nginx monta
// "<o que o cliente mandou>, <quem de fato conectou>". Isso tem duas consequências que o
// código precisa respeitar:
//
//  1. o cabeçalho só vale quando quem entregou a requisição foi o proxy da própria máquina;
//  2. o valor que vale é o ÚLTIMO, não o primeiro — o primeiro é texto escolhido pelo
//     cliente.
//
// A porta da aplicação fica aberta ao mundo de propósito, para os links já entregues
// continuarem funcionando. Errar qualquer um dos dois pontos deixa qualquer pessoa escolher
// o IP com que aparece: o bastante para furar a restrição por faixa de IP de uma credencial
// e para envenenar o registro de reproduções.
func TestClientIPSoConfiaNoProxyLocal(t *testing.T) {
	casos := []struct {
		nome         string
		remoteAddr   string
		xff          string
		realIP       string
		confiar      bool
		querEndereco string
	}{
		{
			nome:         "atrás do nginx local, o cabeçalho vale",
			remoteAddr:   "127.0.0.1:52134",
			xff:          "203.0.113.7",
			confiar:      true,
			querEndereco: "203.0.113.7",
		},
		{
			// O ataque: o cliente manda o próprio X-Forwarded-For, o nginx acrescenta o
			// endereço real ao FIM, e quem pega o primeiro elemento acredita na invenção.
			nome:         "cabeçalho forjado pelo cliente não vence o que o nginx observou",
			remoteAddr:   "127.0.0.1:52134",
			xff:          "1.2.3.4, 203.0.113.7",
			confiar:      true,
			querEndereco: "203.0.113.7",
		},
		{
			// X-Real-IP é SOBRESCRITO pelo nginx, então o cliente não o influencia de
			// forma alguma. Por isso ele vem antes.
			nome:         "X-Real-IP tem precedência",
			remoteAddr:   "127.0.0.1:52134",
			xff:          "1.2.3.4, 203.0.113.7",
			realIP:       "198.51.100.9",
			confiar:      true,
			querEndereco: "198.51.100.9",
		},
		{
			nome:         "cabeçalho de quem chega direto na porta é ignorado",
			remoteAddr:   "198.51.100.44:41000",
			xff:          "203.0.113.7",
			realIP:       "203.0.113.7",
			confiar:      true,
			querEndereco: "198.51.100.44",
		},
		{
			nome:         "sem proxy configurado, o cabeçalho nunca vale",
			remoteAddr:   "127.0.0.1:52134",
			xff:          "203.0.113.7",
			confiar:      false,
			querEndereco: "127.0.0.1",
		},
		{
			nome:         "sem cabeçalho, vale quem abriu a conexão",
			remoteAddr:   "198.51.100.44:41000",
			confiar:      true,
			querEndereco: "198.51.100.44",
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remoteAddr
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if c.realIP != "" {
				r.Header.Set("X-Real-IP", c.realIP)
			}
			if got := ClientIP(r, c.confiar); got != c.querEndereco {
				t.Fatalf("ClientIP = %q, queria %q", got, c.querEndereco)
			}
		})
	}
}
