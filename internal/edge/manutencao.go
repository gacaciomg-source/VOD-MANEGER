package edge

import (
	"net/http"
	"strconv"
	"strings"
)

// Detecção do vídeo de manutenção.
//
// # O problema
//
// Uma fonte em manutenção não devolve erro. Ela devolve HTTP 200, com um vídeo — um aviso
// de dez segundos dizendo "estamos em manutenção", no lugar do filme de duas horas.
//
// Para o proxy isso é indistinguível de sucesso: a conexão abriu, os bytes fluíram, a
// transmissão terminou sem falha nenhuma. O failover, que existe justamente para trocar de
// origem quando uma falha, nunca chega a ser acionado — porque nada falhou. O espectador vê
// dez segundos de aviso e o filme "acaba", enquanto a segunda fonte, que tem o conteúdo de
// verdade, fica ali sem ser tentada.
//
// # A detecção
//
// Pelo tamanho anunciado, e não pelo conteúdo. Um aviso de dez segundos tem alguns
// megabytes; um filme tem gigabytes. A diferença é de três ordens de grandeza, o que
// permite um limiar folgado — nada que erre por pouco.
//
// Olhar o tamanho tem uma vantagem que olhar o conteúdo não teria: ele vem no CABEÇALHO,
// antes de qualquer byte de vídeo sair para o cliente. É o único momento em que ainda dá
// para trocar de origem — depois do primeiro byte, trocar produziria vídeo corrompido.
//
// # Por que não analisar a duração
//
// Seria mais preciso e é inviável aqui: exigiria baixar e interpretar o cabeçalho do
// contêiner de cada reprodução, antes de responder. Pagaríamos esse custo em toda
// reprodução legítima para pegar o caso raro.

// tamanhoMinimoPadrao é o piso abaixo do qual uma resposta é tratada como aviso.
//
// # Por que 100 MB, e não um número menor
//
// "Vídeo de manutenção" faz pensar em algo minúsculo, e nem sempre é: um aviso de dez
// segundos gravado em alta definição passa fácil dos 20 MB. Um limiar baixo demais não pega
// o caso que existe de verdade — e um limiar que não pega nada é pior que não ter.
//
// # O que se arrisca subindo
//
// Conteúdo legítimo abaixo do limiar passa a ser pulado. Episódio curto e muito comprimido
// — meia hora de desenho em 480p — chega perto dos 100 MB.
//
// O estrago disso é pequeno, e é de propósito: pular não é recusar. A próxima origem é
// tentada, e se TODAS forem pequenas a última é servida assim mesmo. O custo real de um
// palpite errado são alguns segundos a mais até o primeiro byte, e não um filme que não
// abre.
//
// É essa assimetria que permite um limiar generoso: errar para mais custa latência, errar
// para menos custa o espectador vendo dez segundos de aviso e achando que o filme acabou.
//
// Ajustável por VODM_VIDEO_MINIMO_MB. O registro anota `bytes_anunciados` a cada recusa —
// é por ele que se descobre o tamanho real do aviso de cada fonte e se afina o número.
const tamanhoMinimoPadrao int64 = 100 << 20

// tamanhoTotalDaMidia lê o tamanho do ARQUIVO INTEIRO anunciado pela fonte.
//
// Não o tamanho da resposta: quando o player pede um pedaço (`Range`), a resposta é do
// tamanho do pedaço, e confundir os dois faria toda busca no meio do filme parecer um vídeo
// de dez segundos. O tamanho inteiro vem no `Content-Range`, depois da barra.
//
// Devolve -1 quando a fonte não anuncia. Não saber não é motivo para recusar: fontes que
// transmitem sem anunciar tamanho existem, e recusá-las derrubaria conteúdo bom.
func tamanhoTotalDaMidia(resp *http.Response) int64 {
	if faixa := resp.Header.Get("Content-Range"); faixa != "" {
		// Formato: "bytes 0-1023/2000000000"
		if i := strings.LastIndex(faixa, "/"); i >= 0 && i+1 < len(faixa) {
			total := strings.TrimSpace(faixa[i+1:])
			if total != "*" {
				if n, err := strconv.ParseInt(total, 10, 64); err == nil && n > 0 {
					return n
				}
			}
		}
		// Content-Range presente e ilegível: sem palpite.
		return -1
	}
	if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
		return resp.ContentLength
	}
	return -1
}

// pareceVideoDeManutencao decide se a resposta é curta demais para ser o conteúdo.
func pareceVideoDeManutencao(resp *http.Response, limiar int64) (int64, bool) {
	if limiar <= 0 {
		return 0, false
	}
	total := tamanhoTotalDaMidia(resp)
	if total <= 0 {
		return total, false
	}
	return total, total < limiar
}

// temOutraOrigemParaTentar informa se recusar esta ainda deixa alternativa.
//
// É a trava que impede a detecção de virar um problema pior que o que resolve: um palpite
// errado sobre a ÚLTIMA origem trocaria dez segundos de aviso por tela preta. Diante da
// dúvida, com nada melhor para oferecer, entregamos o que há.
func temOutraOrigemParaTentar[T any](variantes []T, atual, tentativas int) bool {
	return atual+1 < len(variantes) && tentativas < maxTentativas
}

// minimoOuPadrao resolve o limiar configurado.
//
// Zero significa "não configurado" e recebe o padrão; negativo é a forma explícita de
// desligar a detecção. Sem essa distinção não haveria como desligá-la — zero teria de
// significar as duas coisas.
func minimoOuPadrao(configurado int64) int64 {
	if configurado == 0 {
		return tamanhoMinimoPadrao
	}
	if configurado < 0 {
		return 0
	}
	return configurado
}
