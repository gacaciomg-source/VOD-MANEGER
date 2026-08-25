package edge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vodmanager/internal/store"
)

// Servir a partir do acervo.
//
// # Por que não dá para usar http.ServeContent
//
// Ele resolve Range sozinho, mas exige um io.ReadSeeker — e o armazenamento em nuvem não
// tem um. Lá, "posicionar em 3.500.000" é um cabeçalho numa requisição HTTP, não um
// deslocamento num descritor de arquivo. Envolver isso num ReadSeeker faria cada `Seek`
// virar uma reconexão silenciosa, e o `ServeContent` dá vários por resposta.
//
// Então o Range é interpretado aqui, uma vez, e vira um único deslocamento na abertura.

// Acervo é o que o proxy precisa saber sobre as cópias guardadas.
//
// Interface, e não o serviço concreto, pelo mesmo motivo do Resolver: o plano de dados não
// conhece backends de armazenamento nem credenciais de nuvem. Ele pergunta se há cópia e
// pede os bytes.
type Acervo interface {
	CopiaPronta(ctx context.Context, variantID int64) *store.ArquivoGuardado
	Abrir(ctx context.Context, arquivo *store.ArquivoGuardado, deslocamento int64) (io.ReadCloser, error)
	RegistrarAcesso(ctx context.Context, id int64)
	// TalvezGuardar e chamado DEPOIS de uma entrega bem-sucedida vinda da fonte. Nao
	// devolve erro: nada do que ele decide pode impedir o video de sair.
	TalvezGuardar(ctx context.Context, v *store.PlayableVariant, alvo *store.StreamTarget)
	// TalvezCapturar comeca a gravar esta transmissao no acervo, quando ela puder
	// alimentar uma copia. Devolve nulo quando nao — o caso comum, e nao e falha.
	TalvezCapturar(ctx context.Context, v *store.PlayableVariant, alvo *store.StreamTarget,
		inicio, tamanho int64, ext string) CapturaDoAcervo
}

// faixa é o pedaço do arquivo que o cliente pediu.
type faixa struct {
	inicio int64
	// fim é inclusivo, como no cabeçalho HTTP. -1 significa "até o final".
	fim     int64
	parcial bool
}

// lerFaixa interpreta o cabeçalho Range.
//
// Só a forma `bytes=N-` e `bytes=N-M`, que é o que os players de vídeo usam. As formas
// exóticas do padrão — múltiplas faixas, sufixo `bytes=-500` — não aparecem em reprodução
// de vídeo, e responder a elas errado seria pior que ignorá-las: quem não entende o Range
// devolve o arquivo inteiro, que é uma resposta correta.
func lerFaixa(cabecalho string, tamanho int64) (faixa, bool) {
	if cabecalho == "" || !strings.HasPrefix(cabecalho, "bytes=") {
		return faixa{inicio: 0, fim: tamanho - 1}, true
	}
	spec := strings.TrimPrefix(cabecalho, "bytes=")
	if strings.Contains(spec, ",") {
		return faixa{inicio: 0, fim: tamanho - 1}, true
	}
	partes := strings.SplitN(spec, "-", 2)
	if len(partes) != 2 || partes[0] == "" {
		return faixa{inicio: 0, fim: tamanho - 1}, true
	}

	inicio, err := strconv.ParseInt(partes[0], 10, 64)
	if err != nil || inicio < 0 {
		return faixa{}, false
	}
	// Pedir a partir do fim do arquivo não é erro de digitação: alguns players sondam o
	// limite. A resposta certa é 416, e não um corpo vazio com 206 — este último faria o
	// player esperar bytes que nunca viriam.
	if inicio >= tamanho {
		return faixa{}, false
	}

	fim := tamanho - 1
	if partes[1] != "" {
		f, err := strconv.ParseInt(partes[1], 10, 64)
		if err != nil {
			return faixa{}, false
		}
		if f < fim {
			fim = f
		}
		if f < inicio {
			return faixa{}, false
		}
	}
	return faixa{inicio: inicio, fim: fim, parcial: true}, true
}

// servirDoAcervo entrega o vídeo a partir da cópia guardada.
//
// Devolve `false` quando não conseguiu ANTES de escrever qualquer coisa para o cliente —
// aí o chamador segue para a fonte, e o espectador não percebe nada. Depois do primeiro
// byte não há volta: trocar de origem no meio produziria vídeo corrompido, porque arquivos
// diferentes têm deslocamentos diferentes.
func (p *Proxy) servirDoAcervo(w http.ResponseWriter, r *http.Request, ped pedido,
	arquivo *store.ArquivoGuardado, inicio time.Time) bool {

	if arquivo.Bytes <= 0 {
		return false
	}

	f, ok := lerFaixa(r.Header.Get("Range"), arquivo.Bytes)
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", arquivo.Bytes))
		http.Error(w, "faixa fora do arquivo", http.StatusRequestedRangeNotSatisfiable)
		return true
	}

	corpo, err := p.acervo.Abrir(r.Context(), arquivo, f.inicio)
	if err != nil {
		// A cópia sumiu do disco, a conta de nuvem está fora, o token venceu. Nada disso
		// pode virar erro para o espectador enquanto a fonte existir.
		p.log.Warn("acervo indisponível; caindo para a fonte",
			"arquivo_id", arquivo.ID, "erro", err)
		return false
	}
	defer corpo.Close()

	tamanho := f.fim - f.inicio + 1
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(tamanho, 10))
	w.Header().Set("Content-Type", tipoDoConteudo(arquivo.ContainerExt))
	// X-Cache diz de onde veio. Existe para o diagnóstico da pergunta que sempre aparece
	// quando o acervo é ligado: "isto está mesmo saindo do disco?".
	w.Header().Set("X-Cache", "acervo")

	status := http.StatusOK
	if f.parcial {
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", f.inicio, f.fim, arquivo.Bytes))
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)

	streamID, err := p.abrirSessao(r, ped)
	if err != nil {
		p.log.Warn("não foi possível registrar a sessão", "erro", err)
	} else if err := p.store.MarcarResultadoDoCache(r.Context(), streamID, "hit"); err != nil {
		// Só o rótulo da tela depende disto. Falhar aqui não pode interromper uma
		// reprodução que já está saindo perfeitamente do disco.
		p.log.Warn("falha ao marcar a entrega como cache", "stream_id", streamID, "erro", err)
	}
	ttfb := int(time.Since(inicio).Milliseconds())

	destino := &escritorDoCliente{destino: w}
	buf := make([]byte, bufferCopia)
	enviados, errCopia := io.CopyBuffer(destino, io.LimitReader(corpo, tamanho), buf)

	estado, codigoErro := "closed", ""
	if errCopia != nil && !destino.falhou {
		// Falha LENDO do acervo, não escrevendo para o cliente. Vale registrar: um disco
		// com setor ruim ou uma nuvem instável aparecem aqui antes de aparecerem em
		// qualquer outro lugar.
		estado, codigoErro = "error", "falha_no_acervo"
		p.log.Warn("falha ao ler do acervo",
			"arquivo_id", arquivo.ID, "bytes", enviados, "erro", errCopia)
	} else if errCopia != nil {
		codigoErro = "cliente_desconectou"
	}

	p.fecharSessao(streamID, enviados, ttfb, status, estado, codigoErro, 0, arquivo.VariantID)
	p.contabilidade.Registrar(ped.credID, enviados)
	p.acervo.RegistrarAcesso(r.Context(), arquivo.ID)

	p.log.Info("stream servido do acervo",
		"content_id", ped.alvo.ContentID, "arquivo_id", arquivo.ID,
		"onde", arquivo.Backend, "bytes", enviados, "ttfb_ms", ttfb)
	return true
}

// tipoDoConteudo adivinha pelo contêiner.
//
// Errar aqui não impede a reprodução — os players decidem pelo conteúdo — mas um tipo certo
// evita que um navegador tente baixar em vez de tocar.
func tipoDoConteudo(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "mkv":
		return "video/x-matroska"
	case "avi":
		return "video/x-msvideo"
	case "ts":
		return "video/mp2t"
	case "webm":
		return "video/webm"
	default:
		return "video/mp4"
	}
}

// CapturaDoAcervo é uma cópia sendo gravada a partir da transmissão em curso.
//
// Escrever nela nunca bloqueia e nunca falha: ela é alimentada de dentro da cópia que
// abastece o player, e as duas coisas virariam pausa no vídeo.
type CapturaDoAcervo interface {
	io.Writer
	// Fechar decide o destino da cópia. `completo` diz se a fonte entregou tudo o que
	// anunciou — só nesse caso a cópia vira acervo.
	Fechar(completo bool)
}
