package edge

import "testing"

// A extensão do arquivo não pode importar: clientes diferentes montam o link com
// extensões diferentes, e recusar por causa disso quebraria a reprodução sem motivo.
func TestIDDoArquivoAceitaQualquerExtensao(t *testing.T) {
	casos := map[string]int64{
		"2178.mp4":  2178,
		"2178.mkv":  2178,
		"2178.ts":   2178,
		"2178.avi":  2178,
		"2178":      2178,
		"1":         1,
		"999999999": 999999999,
		// Nome com mais de um ponto: vale o que vem antes do ÚLTIMO ponto.
		"2178.parte.mp4": 0,
	}
	for arquivo, esperado := range casos {
		id, ok := idDoArquivo(arquivo)
		if esperado == 0 {
			if ok {
				t.Errorf("idDoArquivo(%q) aceitou, esperava recusa", arquivo)
			}
			continue
		}
		if !ok || id != esperado {
			t.Errorf("idDoArquivo(%q) = %d,%v — esperava %d,true", arquivo, id, ok, esperado)
		}
	}
}

// O que precisa ser recusado, para não virar consulta ao banco com lixo.
func TestIDDoArquivoRecusaInvalidos(t *testing.T) {
	for _, arquivo := range []string{
		"", ".mp4", "abc.mp4", "0.mp4", "-5.mp4", "12a.mp4", "12 34.mp4",
	} {
		if _, ok := idDoArquivo(arquivo); ok {
			t.Errorf("idDoArquivo(%q) deveria ter sido recusado", arquivo)
		}
	}
}
