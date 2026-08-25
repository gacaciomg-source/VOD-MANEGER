package edge

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Quando a fonte ignora o Range que pedimos.
//
// # O sintoma
//
// O filme trava na metade e volta ao começo. Dar play de novo trava no mesmo ponto e volta
// ao começo outra vez — sempre igual, sempre reproduzível.
//
// # O que acontece
//
// O player não baixa o filme inteiro de uma vez. Quando o buffer acaba, ele pede a
// continuação: `Range: bytes=734000000-`. Nós repassamos esse pedido à fonte.
//
// Uma fonte que respeita o pedido devolve 206 e os bytes a partir daquele ponto. Uma fonte
// que NÃO respeita devolve 200 e o arquivo inteiro, do byte zero.
//
// Como só repassávamos o que a fonte respondesse, o player pedia a continuação e recebia o
// começo do filme — com um 200 dizendo "aqui está tudo". Para o player, isso é um arquivo
// novo começando. Ele volta ao início. E como a fonte se comporta igual toda vez, o mesmo
// filme trava sempre no mesmo lugar.
//
// # Por que não acontece com o acervo
//
// Servindo de uma cópia guardada, o Range é resolvido por nós: abrimos o arquivo no ponto
// pedido e respondemos 206 corretamente. O defeito só existe no caminho que atravessa até a
// fonte — que é exatamente o que se observa em produção.
//
// # O conserto
//
// Descartar os bytes que a fonte mandou a mais e responder o 206 que o player pediu. Custa a
// banda do trecho descartado, e é o único jeito de entregar a posição certa a partir de uma
// fonte que não sabe posicionar. A alternativa — repassar o 200 — é o defeito.

// inicioPedido devolve o primeiro byte que o cliente pediu.
//
// Zero quando não há Range, quando ele é de uma forma que players de vídeo não usam, ou
// quando pede desde o começo — nos três casos não há nada a corrigir.
func inicioPedido(cabecalho string) int64 {
	if !strings.HasPrefix(cabecalho, "bytes=") {
		return 0
	}
	spec := strings.TrimPrefix(cabecalho, "bytes=")
	if strings.Contains(spec, ",") {
		return 0
	}
	inicioTexto, _, achou := strings.Cut(spec, "-")
	if !achou || inicioTexto == "" {
		return 0
	}
	inicio, err := strconv.ParseInt(inicioTexto, 10, 64)
	if err != nil || inicio <= 0 {
		return 0
	}
	return inicio
}

// corpoNaPosicaoPedida ajusta a resposta da fonte ao Range que o cliente pediu.
//
// Devolve o corpo já posicionado e o status a responder. Quando a fonte respeitou o pedido —
// ou quando não havia pedido —, nada muda e o corpo volta como veio.
//
// O terceiro retorno diz se houve correção, para quem chama poder registrar qual fonte
// obrigou a isso: é uma informação de custo, não de erro.
func corpoNaPosicaoPedida(w http.ResponseWriter, r *http.Request, resp *http.Response) (io.Reader, int, bool) {
	inicio := inicioPedido(r.Header.Get("Range"))
	if inicio == 0 || resp.StatusCode != http.StatusOK {
		// Sem pedido de posição, ou a fonte já respondeu 206: nada a fazer.
		return resp.Body, resp.StatusCode, false
	}

	total := resp.ContentLength
	if total <= 0 || inicio >= total {
		// Sem saber o tamanho não dá para montar um Content-Range válido, e um Content-Range
		// inválido confunde o player mais que a resposta errada. Repassar como veio ao menos
		// mantém o comportamento conhecido.
		//
		// Pedir depois do fim do arquivo é o player sondando o limite; devolver o arquivo
		// inteiro seria pior que devolver a resposta que a fonte deu.
		return resp.Body, resp.StatusCode, false
	}

	// O trecho descartado é banda comprada da fonte e jogada fora. É o preço de posicionar
	// numa fonte que não posiciona — e ele é menor que o preço de não conseguir assistir.
	if _, err := io.CopyN(io.Discard, resp.Body, inicio); err != nil {
		// A fonte não tinha os bytes que anunciou. Repassar o que sobrou seria entregar
		// vídeo do lugar errado; melhor deixar o erro aparecer na cópia.
		return resp.Body, resp.StatusCode, false
	}

	// Os cabeçalhos de tamanho vieram da fonte e descrevem o arquivo INTEIRO. Reescrevê-los
	// é obrigatório: um Content-Length de arquivo inteiro numa resposta parcial faz o player
	// esperar bytes que não virão, e travar no fim.
	w.Header().Set("Content-Length", strconv.FormatInt(total-inicio, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", inicio, total-1, total))
	return resp.Body, http.StatusPartialContent, true
}
