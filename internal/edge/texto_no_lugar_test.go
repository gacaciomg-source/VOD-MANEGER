package edge

import "testing"

// A fonte que responde "200 OK" com "invalid stream" no corpo tem que perder a vez para a
// próxima. E o que é vídeo de verdade — inclusive mal rotulado — tem que continuar passando.
func TestRespostaDeTextoNoLugarDoVideo(t *testing.T) {
	recusar := []string{
		"text/html", "text/html; charset=UTF-8", "TEXT/PLAIN", "application/json",
		" text/xml ", "application/xml;charset=utf-8",
	}
	for _, tipo := range recusar {
		if !respostaDeTextoNoLugarDoVideo(tipo) {
			t.Errorf("%q passou como vídeo; o player receberia uma página de erro", tipo)
		}
	}
	aceitar := []string{
		"", "video/mp4", "video/x-matroska", "application/octet-stream", "video/mp2t",
		"application/vnd.apple.mpegurl",
	}
	for _, tipo := range aceitar {
		if respostaDeTextoNoLugarDoVideo(tipo) {
			t.Errorf("%q foi recusado; é assim que fonte entrega vídeo de verdade", tipo)
		}
	}
}
