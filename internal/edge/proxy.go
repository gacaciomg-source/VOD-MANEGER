package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

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
		for i := range ped.variantes {
			arquivo := p.acervo.CopiaPronta(r.Context(), ped.variantes[i].ID)
			if arquivo == nil {
				continue
			}
			if p.servirDoAcervo(w, r, ped, arquivo, inicio) {
				return
			}
			break
		}
	}

	streamID, err := p.abrirSessao(r, ped)
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
	w.WriteHeader(resp.StatusCode)

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
	corpo, pararVigia, fonteTravou := vigiar(resp.Body, cancelar, destino.EsperandoCliente)
	defer pararVigia()

	buf := make([]byte, bufferCopia)
	enviados, errCopia := io.CopyBuffer(destino, corpo, buf)

	estado, codigoErro := "closed", ""
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
	}

	fechar(enviados, ttfb, resp.StatusCode, estado, codigoErro, &usada.ID)
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
	if p.acervo != nil && estado == "closed" && enviados > 0 {
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

func (p *Proxy) abrirSessao(r *http.Request, ped pedido) (int64, error) {
	nova := store.NewStream{
		NodeID:       p.nodeID,
		CredentialID: ped.credID,
		ClientIP:     ped.clientIP,
		UserAgent:    r.UserAgent(),
		RangeHeader:  r.Header.Get("Range"),
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
func clientIP(r *http.Request, confiarProxy bool) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if confiarProxy {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				primeiro, _, _ := strings.Cut(xff, ",")
				if v := strings.TrimSpace(primeiro); v != "" {
					return v
				}
			}
		}
	}
	return host
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
