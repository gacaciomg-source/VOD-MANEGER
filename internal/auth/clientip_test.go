package auth

import (
	"net/http/httptest"
	"testing"
)

// TestClientIPSoConfiaNoProxyLocal fixa a fronteira que separa "o proxy me disse" de
// "o cliente me disse".
//
// X-Forwarded-For é escrito pelo cliente e reescrito pelo proxy — o cabeçalho sozinho não
// prova nada. A porta da aplicação fica aberta ao mundo de propósito, para que os links já
// entregues continuem funcionando; então honrar o cabeçalho sem olhar de onde ele veio
// deixaria qualquer pessoa escolher o IP com que aparece. Bastaria isso para furar o limite
// de telas simultâneas por credencial e para envenenar o registro de falhas com um endereço
// inventado.
//
// A regra é o vizinho imediato: o nginx desta máquina fala de 127.0.0.1, e quem chega
// direto na porta não fala.
func TestClientIPSoConfiaNoProxyLocal(t *testing.T) {
	casos := []struct {
		nome         string
		remoteAddr   string
		xff          string
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
			nome:         "cabeçalho forjado por quem chega direto na porta é ignorado",
			remoteAddr:   "198.51.100.44:41000",
			xff:          "203.0.113.7",
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
			nome:         "a cadeia de proxies devolve o primeiro, que é o cliente",
			remoteAddr:   "127.0.0.1:52134",
			xff:          "203.0.113.7, 70.41.3.18",
			confiar:      true,
			querEndereco: "203.0.113.7",
		},
		{
			nome:         "sem cabeçalho, vale quem abriu a conexão",
			remoteAddr:   "198.51.100.44:41000",
			xff:          "",
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
			if got := ClientIP(r, c.confiar); got != c.querEndereco {
				t.Fatalf("ClientIP = %q, queria %q", got, c.querEndereco)
			}
		})
	}
}
