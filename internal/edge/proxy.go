package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"vodmanager/internal/auth"
	"vodmanager/internal/ingest"
	"vodmanager/internal/store"
)

// bufferCopia é o tamanho do buffer por cliente.
//
// 256 KiB é grande o bastante para amortizar as chamadas de sistema e pequeno o bastante
// para que mil clientes simultâneos custem 256 MB — não o tamanho dos vídeos. É a
// aplicação prática do princípio "o disco é o buffer, a RAM não é".
const bufferCopia = 256 * 1024

// maxTentativas é quantas variantes são tentadas antes de desistir.
const maxTentativas = 3

// Resolver materializa a URL de origem de uma variante.
//
// Interface e não implementação concreta: o edge não conhece credenciais de fonte nem
// providers — ele pede a URL a quem sabe montá-la.
type Resolver interface {
	ResolveStreamURL(ctx context.Context, variant *store.SourceVariant) (string, error)
}

// Proxy entrega bytes de vídeo ao cliente.
type Proxy struct {
	store    *store.Store
	auth     *Authenticator
	resolver Resolver
	// acervo e nulo quando este processo nao guarda midia. Nulo significa "sempre a
	// fonte", que e o comportamento de sempre.
	acervo   Acervo
	log      *slog.Logger
	nodeID   string
	http     *http.Client
	conexoes *ContadorConexoes
	// tamanhoMinimo e o piso abaixo do qual uma resposta e tratada como aviso de
	// manutencao em vez de conteudo. Zero desliga a deteccao.
	tamanhoMinimo int64
	// contabilidade acumula usos e bytes por credencial fora do caminho dos bytes.
	contabilidade *Contabilidade
}

// Conexoes expõe o contador para o painel mostrar quantas reproduções cada credencial
// tem agora.
func (p *Proxy) Conexoes() *ContadorConexoes { return p.conexoes }

// Options configura o proxy.
type Options struct {
	Store    *store.Store
	Auth     *Authenticator
	Resolver Resolver
	Acervo   Acervo
	Log      *slog.Logger
	NodeID   string
	// TamanhoMinimoDeVideo: abaixo disso a resposta e tratada como aviso de manutencao.
	// Zero usa o padrao; negativo desliga.
	TamanhoMinimoDeVideo int64
}

// New cria o proxy.
func New(opts Options) *Proxy {
	return &Proxy{
		store:         opts.Store,
		auth:          opts.Auth,
		resolver:      opts.Resolver,
		acervo:        opts.Acervo,
		log:           opts.Log,
		nodeID:        opts.NodeID,
		conexoes:      NovoContadorConexoes(),
		tamanhoMinimo: minimoOuPadrao(opts.TamanhoMinimoDeVideo),
		contabilidade: NovaContabilidade(opts.Store, opts.Log),
		http: &http.Client{
			// SEM prazo total: um filme leva horas para ser transmitido. O que protege
			// é o prazo para a fonte RESPONDER, não para terminar.
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				// Keep-alive generoso: um player que dá seek abre várias conexões à
				// mesma origem em sequência, e refazer TLS a cada seek dobra a latência.
				MaxIdleConns:          200,
				MaxIdleConnsPerHost:   20,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("cadeia de redirecionamentos longa demais")
				}
				return nil
			},
		},
	}
}

// pedido reúne o que já foi resolvido antes de começar a transmitir.
type pedido struct {
	alvo      *store.StreamTarget
	variantes []store.PlayableVariant
	credID    *int64
	clientIP  string
}

// ServeContent atende uma requisição de vídeo já autenticada e resolvida.
//
// O caminho até o primeiro byte é deliberadamente curto: nenhuma escrita no banco, nenhum
// processamento de mídia, nenhuma inspeção do conteúdo. Só escolher a fonte, abrir a
// conexão e copiar.
func (p *Proxy) ServeContent(w http.ResponseWriter, r *http.Request, ped pedido) {
	inicio := time.Now()

	if len(ped.variantes) == 0 {
		http.Error(w, "nenhuma origem disponível para este conteúdo", http.StatusNotFound)
		return
	}

	// O próximo episódio é enfileirado no COMEÇO deste, e não no fim.
	//
	// No fim não servia para nada, e a razão é simples: quem termina o episódio 20 abre o 21
	// em segundos. Um download que começa nesse instante chega tarde por definição — o
	// espectador espera exatamente o que o adiantamento existia para evitar.
	//
	// Começando agora, o 21 tem a duração inteira do 20 para descer. É a diferença entre
	// quarenta minutos de vantagem e nenhuma.
	p.adiantarProximoEpisodio(r, ped)

	// O acervo vem antes da fonte.
	//
	// A consulta é um índice parcial e uma linha — barata o bastante para caber no caminho
	// do primeiro byte. Se houver cópia pronta e o armazenamento responder, o vídeo sai
	// daqui e a fonte nem é procurada.
	//
	// Qualquer problema devolve `false` ANTES de escrever para o cliente, e o fluxo segue
	// para a fonte como sempre fez. É a garantia que torna ligar o acervo uma decisão sem
	// risco: no pior caso ele não ajuda; em caso nenhum ele atrapalha.
	if p.acervo != nil {
		ids := make([]int64, len(ped.variantes))
		for i := range ped.variantes {
			ids[i] = ped.variantes[i].ID
		}
		if arquivo := p.acervo.CopiaProntaDeAlguma(r.Context(), ids); arquivo != nil {
			if p.servirDoAcervo(w, r, ped, arquivo, inicio) {
				return
			}
		}
	}

	streamID, err := p.abrirSessao(r, ped, "")
	if err != nil {
		p.log.Warn("não foi possível registrar a sessão", "erro", err)
	}

	var (
		ultimoErro error
		tentativas int
		usada      *store.PlayableVariant
		resp       *http.Response
		cancelar   context.CancelFunc
		fechada    bool
		// serviuAviso: entregamos algo que parece aviso de manutencao por nao haver
		// alternativa. A entrega deu certo; o conteudo e que provavelmente nao e o filme.
		serviuAviso bool
	)

	// O fechamento da sessão precisa ser GARANTIDO, e não escrito no fim da função.
	//
	// Havia um caminho — o cliente desistir enquanto tentávamos as origens — que saía
	// por um return sem fechar. A linha ficava marcada como 'ativa' para sempre, com zero
	// bytes e sem primeiro byte, e o painel mostrava reproduções que já não existiam. Um
	// cliente que insiste acumulava uma linha fantasma por tentativa.
	fechar := func(bytes int64, ttfb, status int, estado, errCode string, variantID *int64) {
		if fechada {
			return
		}
		fechada = true
		p.fecharSessao(streamID, bytes, ttfb, status, estado, errCode, tentativas, variantID)
	}
	// Rede de segurança: qualquer saída não prevista fecha a sessão como abandono.
	defer func() {
		fechar(0, 0, 499, "error", "cliente_desistiu_antes_do_video", nil)
	}()

	// Failover ANTES do primeiro byte (decisão D3). Depois que um byte saiu para o
	// cliente, trocar de fonte produziria vídeo corrompido — arquivos diferentes têm
	// offsets diferentes.
	for i := range ped.variantes {
		if tentativas >= maxTentativas {
			break
		}
		if r.Context().Err() != nil {
			return // o cliente desistiu antes de começarmos
		}
		v := &ped.variantes[i]
		tentativas++

		origem, cancelarOrigem, err := p.abrirOrigem(r, v)
		if err != nil {
			ultimoErro = err
			p.log.Warn("origem falhou, tentando a próxima",
				"variant_id", v.ID, "fonte", v.SourceName, "erro", err)
			continue
		}

		// Fonte em manutenção devolve um aviso de dez segundos, com HTTP 200.
		//
		// Nada falhou, então o failover nunca seria acionado: o espectador veria o aviso,
		// o "filme" acabaria, e a segunda fonte — que tem o conteúdo de verdade — ficaria
		// sem ser tentada. O tamanho anunciado denuncia isso ANTES do primeiro byte, que é
		// o último momento em que ainda dá para trocar de origem.
		if total, suspeito := pareceVideoDeManutencao(origem, p.tamanhoMinimo); suspeito {
			// A última origem é servida MESMO ASSIM.
			//
			// Dez segundos de aviso são ruins; nada é pior. E o palpite pode estar errado
			// — um episódio curto de verdade existe. Diante da dúvida, com nada melhor
			// para oferecer, entregamos o que há.
			if temOutraOrigemParaTentar(ped.variantes, i, tentativas) {
				origem.Body.Close()
				cancelarOrigem()
				ultimoErro = fmt.Errorf(
					"a fonte %s devolveu %d bytes, curto demais para um vídeo — provável aviso de manutenção",
					v.SourceName, total)
				p.log.Warn("origem devolveu vídeo curto demais; provável manutenção",
					"variant_id", v.ID, "fonte", v.SourceName,
					"bytes_anunciados", total, "minimo", p.tamanhoMinimo)
				continue
			}
			// Servido, mas REGISTRADO. Sem isto, o aviso de manutencao era o unico desfecho
			// do sistema que nao deixava rastro: o log dizia, e a tela nao — e quem administra
			// nao tinha como saber QUAIS titulos estavam caindo nisso, nem quantos.
			serviuAviso = true
			p.log.Warn("vídeo curto demais, mas é a última origem disponível; servindo assim mesmo",
				"variant_id", v.ID, "fonte", v.SourceName, "bytes_anunciados", total)
		}

		usada, resp, cancelar = v, origem, cancelarOrigem
		break
	}

	if resp == nil {
		fechar(0, 0, http.StatusBadGateway, "error", "todas_as_origens_falharam", nil)
		// Tentativa frustrada também é uso: sem contá-la, uma credencial que só gera
		// erro apareceria no painel como se nunca tivesse sido usada.
		p.contabilidade.Registrar(ped.credID, 0)
		msg := "nenhuma origem respondeu"
		if ultimoErro != nil {
			msg = ingest.RedactString(ultimoErro.Error())
		}
		http.Error(w, msg, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	defer cancelar()

	// Repassa os cabeçalhos que o player precisa para posicionar e dar seek.
	copiarCabecalhos(w.Header(), resp.Header)
	w.Header().Set("Accept-Ranges", "bytes")

	// A fonte pode ter IGNORADO o Range e devolvido o arquivo inteiro.
	//
	// Repassar isso entrega ao player o começo do filme quando ele pediu a continuação — e
	// com um 200 dizendo "aqui está tudo". Para o player, é um arquivo novo começando: ele
	// volta ao início. Como a fonte se comporta igual toda vez, o mesmo filme trava sempre
	// no mesmo lugar, que é exatamente o relato.
	corpoDaFonte, status, corrigido := corpoNaPosicaoPedida(w, r, resp)
	if corrigido {
		p.log.Warn("a fonte ignorou o Range; posicionamos por conta própria",
			"variant_id", usada.ID, "fonte", usada.SourceName,
			"pedido", r.Header.Get("Range"),
			"descartado", inicioPedido(r.Header.Get("Range")))
	}
	w.WriteHeader(status)

	ttfb := int(time.Since(inicio).Milliseconds())

	// Direct Stream: cópia pura, sem transcodificar, sem inspecionar. O buffer é o único
	// custo de memória por cliente.
	// Vigia do primeiro byte: uma fonte que aceita a conexão e nunca envia nada deixaria
	// a reprodução presa para sempre, segurando vaga na credencial e conexão na origem.
	// O escritor vem ANTES do vigia porque o vigia consulta o escritor.
	//
	// É o que distingue um player pausado — a cópia parada esperando o cliente aceitar
	// bytes — de uma fonte que morreu no meio. Sem essa consulta, um filme pausado por
	// dois minutos seria cortado como se a fonte tivesse travado.
	destino := &escritorDoCliente{destino: w}
	corpo, pararVigia, fonteTravou := vigiar(corpoDaFonte, cancelar, destino.EsperandoCliente)
	defer pararVigia()

	// A captura: guardar enquanto entrega.
	//
	// Sem ela o filme desce da fonte duas vezes — uma para quem assistiu, outra depois para
	// o baixador. Aqui a passagem do espectador É o download, e a segunda descida deixa de
	// existir.
	//
	// A gravação acontece numa rotina separada, alimentada por uma fila curta. Se o
	// armazenamento não acompanhar, a captura é abandonada na hora e a transmissão segue
	// intocada: quem assiste nunca espera pelo disco.
	var captura CapturaDoAcervo
	if p.acervo != nil {
		captura = p.acervo.TalvezCapturar(context.WithoutCancel(r.Context()),
			usada, ped.alvo, inicioPedido(r.Header.Get("Range")), resp.ContentLength,
			usada.ContainerExt)
	}

	var saida io.Writer = destino
	if captura != nil {
		saida = io.MultiWriter(destino, captura)

		// "baixando e servindo" na tela: a fonte alimenta o espectador e o acervo na mesma
		// descida. Distinguir isso de uma passagem comum é o que mostra que a próxima
		// reprodução deste filme já vai sair do disco.
		//
		// Numa rotina própria, e não aqui: é um rótulo de tela, e a decisão de capturar só
		// se conhece DEPOIS de a sessão já ter sido aberta. Fazer o espectador esperar uma
		// segunda escrita no banco por causa disso seria pagar caro por informação nenhuma.
		if streamID > 0 {
			go func(id int64) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := p.store.MarcarResultadoDoCache(ctx, id, "miss"); err != nil {
					p.log.Warn("falha ao marcar a captura na sessão", "stream_id", id, "erro", err)
				}
			}(streamID)
		}
	}

	buf := make([]byte, bufferCopia)
	enviados, errCopia := io.CopyBuffer(saida, corpo, buf)

	estado, codigoErro := "closed", ""
	// O aviso de manutenção servido por falta de alternativa aparece nas Falhas.
	//
	// A entrega em si deu certo, então o estado continua "closed" — inflar a contagem de
	// erros por algo que funcionou tornaria a tela menos confiável, não mais. Mas o código
	// fica, porque a informação que importa não é "falhou": é QUAIS títulos estão entregando
	// um aviso no lugar do filme, e de qual fonte.
	//
	// Sem isto, era o único desfecho do sistema que não deixava rastro: o log dizia, a tela
	// não, e quem administra descobria pelo cliente.
	if serviuAviso {
		codigoErro = "video_de_manutencao"
	}
	if errCopia != nil {
		// Cliente que fecha o player no meio é o caso mais comum e não é falha nossa.
		switch {
		case fonteTravou():
			// Fomos nós que cortamos: a fonte parou de enviar.
			//
			// Os dois casos ficam separados no código de erro porque pedem coisas
			// diferentes de quem for olhar. Nunca começar costuma ser credencial ou link
			// morto — problema de cadastro. Parar no meio é a fonte engasgando sob carga,
			// e aparece em rajadas no mesmo horário.
			if enviados > 0 {
				estado, codigoErro = "error", "fonte_parou_no_meio"
				p.log.Warn("a fonte parou de enviar no meio da transmissão",
					"variant_id", usada.ID, "fonte", usada.SourceName,
					"bytes", enviados, "prazo", prazoSemDados.String())
			} else {
				estado, codigoErro = "error", "fonte_nao_enviou_dados"
				p.log.Warn("fonte aceitou a conexão e não enviou nada",
					"variant_id", usada.ID, "fonte", usada.SourceName,
					"prazo", prazoPrimeiroByte.String())
			}
		case destino.falhou, errors.Is(errCopia, context.Canceled), r.Context().Err() != nil:
			// A falha foi ao escrever PARA o cliente: ele fechou o player ou deu seek.
			// É o comportamento normal de quem assiste, não uma falha de entrega.
			codigoErro = "cliente_desconectou"
		default:
			estado, codigoErro = "error", "falha_na_copia"
			p.log.Warn("transmissão interrompida",
				"variant_id", usada.ID, "bytes", enviados, "erro", errCopia)
		}
	} else if faltou := resp.ContentLength - enviados; resp.ContentLength > 0 && faltou > 0 {
		// A fonte fechou a conexão ANTES de entregar o que ela mesma anunciou.
		//
		// Este era o desfecho invisível mais grave do sistema, e a razão é sutil: quando a
		// origem encerra a conexão, io.Copy devolve EOF — que em Go é ausência de erro. Uma
		// entrega cortada em 40% do filme era gravada como conclusão bem-sucedida, e a
		// reprodução aparecia nos números ao lado das que terminaram inteiras.
		//
		// Do lado de quem assiste, o filme simplesmente para no meio e o player volta ao
		// começo. Do lado de quem administra, não havia o que olhar: o painel dizia que
		// tudo correu bem.
		//
		// É a falha mais comum de fonte de IPTV sob carga — e era justamente a que os
		// nossos registros não sabiam contar.
		estado, codigoErro = "error", "fonte_entregou_menos"
		p.log.Warn("a fonte encerrou antes de entregar o que anunciou",
			"variant_id", usada.ID, "fonte", usada.SourceName,
			"entregue", enviados, "anunciado", resp.ContentLength, "faltou", faltou)
	}

	fechar(enviados, ttfb, status, estado, codigoErro, &usada.ID)
	// Consumo da credencial: acumulado em memória e gravado em lote. É o que alimenta
	// as colunas de usos e transferido no painel.
	p.contabilidade.Registrar(ped.credID, enviados)

	// A cópia é enfileirada DEPOIS da entrega, e só quando ela deu certo.
	//
	// Depois, porque enfileirar antes atrasaria o primeiro byte por uma decisão que não
	// interessa a quem está esperando o filme começar. E só quando deu certo, porque
	// guardar uma cópia de uma origem que acabou de falhar seria guardar o defeito.
	//
	// O contexto é o de fundo, não o da requisição: este já foi cancelado quando o cliente
	// fechou o player, e usá-lo faria a fila nunca receber nada de quem assiste até o fim.
	if captura != nil {
		// Completo se, e somente se, os bytes fecham com o que a fonte anunciou.
		//
		// Contar com `estado == "closed"` foi um erro caro: esse campo continua "closed"
		// quando o CLIENTE desconecta — só o código de erro muda, porque fechar o player
		// não é falha de entrega. O resultado foi o cache guardar como pronto os primeiros
		// cem quilobytes de cada filme, e passar a servi-los no lugar da fonte. O painel
		// mostrava tudo verde, e nada abria.
		//
		// Bytes não têm essa ambiguidade.
		completo := codigoErro == "" && resp.ContentLength > 0 && enviados == resp.ContentLength
		captura.Fechar(completo)
	} else if p.acervo != nil && estado == "closed" && enviados > 0 {
		p.acervo.TalvezGuardar(context.WithoutCancel(r.Context()), usada, ped.alvo)
	}

	p.log.Info("stream concluído",
		"content_id", ped.alvo.ContentID, "fonte", usada.SourceName,
		"bytes", enviados, "ttfb_ms", ttfb, "tentativas", tentativas,
		"duracao", time.Since(inicio).Round(time.Millisecond).String())
}

// abrirOrigem resolve a URL e abre a conexão com a fonte, repassando o Range do cliente.
func (p *Proxy) abrirOrigem(r *http.Request, v *store.PlayableVariant) (*http.Response, context.CancelFunc, error) {
	variant := &store.SourceVariant{
		ID: v.ID, SourceID: v.SourceID, OriginURL: v.OriginURL,
		StreamRef: v.StreamRef, ContainerExt: v.ContainerExt,
	}
	url, err := p.resolver.ResolveStreamURL(r.Context(), variant)
	if err != nil {
		return nil, nil, fmt.Errorf("resolvendo URL da fonte %s: %w", v.SourceName, err)
	}

	// Contexto próprio, filho do da requisição: é ele que o vigia cancela quando a fonte
	// aceita a conexão e não envia byte nenhum.
	ctx, cancelar := context.WithCancel(r.Context())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancelar()
		return nil, nil, fmt.Errorf("montando requisição: %w", err)
	}
	// O Range do cliente é repassado À FONTE. É o que permite seek sem baixar o arquivo
	// inteiro, e é a razão de o player conseguir pular para o meio do filme.
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	req.Header.Set("User-Agent", "VODManager/1.0")
	req.Header.Set("Accept", "*/*")

	resp, err := p.http.Do(req)
	if err != nil {
		cancelar()
		return nil, nil, fmt.Errorf("conectando à fonte %s: %w", v.SourceName, redigir(err))
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		cancelar()
		return nil, nil, fmt.Errorf("a fonte %s respondeu %s", v.SourceName, resp.Status)
	}
	return resp, cancelar, nil
}

// cabecalhosRepassados são os que o player precisa e que não vazam nada da origem.
//
// Lista explícita em vez de copiar tudo: cabeçalhos da fonte podem conter identificação
// do servidor de origem, cookies e tokens — nada disso deve chegar ao cliente.
var cabecalhosRepassados = []string{
	"Content-Type",
	"Content-Length",
	"Content-Range",
	"Last-Modified",
	"ETag",
}

func copiarCabecalhos(destino, origem http.Header) {
	for _, h := range cabecalhosRepassados {
		if v := origem.Get(h); v != "" {
			destino.Set(h, v)
		}
	}
	if destino.Get("Content-Type") == "" {
		destino.Set("Content-Type", "video/mp4")
	}
}

func (p *Proxy) abrirSessao(r *http.Request, ped pedido, resultado string) (int64, error) {
	nova := store.NewStream{
		NodeID:       p.nodeID,
		CredentialID: ped.credID,
		ClientIP:     ped.clientIP,
		UserAgent:    r.UserAgent(),
		RangeHeader:  r.Header.Get("Range"),
		CacheResult:  resultado,
	}
	if ped.alvo.Kind == store.TargetEpisode {
		nova.EpisodeID = ped.alvo.EpisodeID
	}
	if ped.alvo.ContentID > 0 {
		id := ped.alvo.ContentID
		nova.ContentID = &id
	}
	if len(ped.variantes) > 0 {
		sid := ped.variantes[0].SourceID
		nova.SourceID = &sid
	}
	// Contexto PRÓPRIO, desligado do cliente. Se ele desistir no meio deste INSERT, o
	// Postgres confirma a linha mas o driver devolve erro por cancelamento — ficaríamos
	// sem o id de uma sessão que existe, e ela permaneceria 'ativa' para sempre.
	//
	// Este registro é a NOSSA contabilidade, não a requisição do cliente: ele precisa
	// terminar mesmo quando o cliente vai embora.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	return p.store.OpenStream(ctx, nova)
}

func (p *Proxy) fecharSessao(id int64, bytes int64, ttfb, status int, estado, errCode string, tentativas int, variantID *int64) {
	if id == 0 {
		return
	}
	// Contexto próprio: a requisição do cliente já acabou, e é justamente agora que
	// precisamos gravar o resultado dela.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.store.CloseStream(ctx, id, bytes, ttfb, status, estado, errCode, tentativas, variantID); err != nil {
		p.log.Warn("não foi possível fechar a sessão de stream", "stream_id", id, "erro", err)
	}
}

func redigir(err error) error {
	if err == nil {
		return nil
	}
	limpo := ingest.RedactString(err.Error())
	if limpo == err.Error() {
		return err
	}
	return fmt.Errorf("%s", limpo)
}

// clientIP extrai o IP do cliente.
//
// X-Forwarded-For só é aceito quando quem entregou a requisição foi o proxy da própria
// máquina. O cabeçalho é escrito pelo cliente e reescrito pelo proxy — ele não prova nada
// sozinho, e a porta da aplicação continua aberta ao mundo de propósito. Sem a exigência
// do loopback, qualquer pessoa escolheria o IP com que aparece, e o limite de telas
// simultâneas por credencial deixaria de significar coisa alguma.
// Uma implementação só, em internal/auth.
//
// Havia duas cópias desta regra — uma aqui, outra lá — e elas precisavam concordar sobre
// qual endereço do X-Forwarded-For usar. Não concordar significaria a restrição por faixa
// de IP de uma credencial julgar por um endereço e o limite de reproduções contar por
// outro: dois comportamentos incoerentes que ninguém conseguiria explicar.
//
// Duas cópias de uma regra de segurança é uma cópia demais.
func clientIP(r *http.Request, confiarProxy bool) string {
	return auth.ClientIP(r, confiarProxy)
}

// ClientIP expõe a extração do IP do cliente para os outros planos de dados (a saída
// M3U e a API Xtream), que precisam da mesma regra de confiança no proxy reverso.
func ClientIP(r *http.Request, confiarProxy bool) string { return clientIP(r, confiarProxy) }

// Fechar encerra a contabilidade, gravando o que ainda estava acumulado.
func (p *Proxy) Fechar() {
	if p.contabilidade != nil {
		p.contabilidade.Fechar()
	}
}

// Contabilidade expõe o acumulador. Usado pelo painel e pelos testes para forçar a
// gravação sem esperar o intervalo de descarga.
func (p *Proxy) Contabilidade() *Contabilidade { return p.contabilidade }
