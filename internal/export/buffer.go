package export

import (
	"bufio"
	"io"
)

// bufferHTTP acumula a escrita antes de ir para o socket.
//
// Sem ele, cada item do catálogo viraria uma escrita de rede independente: com 250 mil
// episódios, isso é 250 mil chamadas de sistema para entregar uma única resposta.
type bufferHTTP struct {
	*bufio.Writer
}

func novoBufferHTTP(w io.Writer) *bufferHTTP {
	return &bufferHTTP{Writer: bufio.NewWriterSize(w, tamanhoBuffer)}
}
