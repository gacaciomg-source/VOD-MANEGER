package edge

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"vodmanager/internal/roles"
	"vodmanager/internal/store"
)

// Module adapta o proxy ao registro de módulos.
//
// Papéis manager E node: o plano de dados é justamente o que roda num Node.
type Module struct{ proxy *Proxy }

// NewModule cria o módulo do plano de dados.
func NewModule(p *Proxy) *Module { return &Module{proxy: p} }

// Name identifica o módulo.
func (m *Module) Name() string { return "edge" }

// Roles: o edge roda tanto no Manager quanto num Node dedicado.
func (m *Module) Roles() []roles.Role { return []roles.Role{roles.RoleManager, roles.RoleNode} }

// Start não precisa fazer nada: as rotas são montadas pelo servidor HTTP.
func (m *Module) Start(context.Context) error { return nil }

// Stop idem.
func (m *Module) Stop(context.Context) error { return nil }

// Rotas monta os endpoints públicos de streaming.
//
// Dois formatos, por motivos diferentes:
//
//	/movie/{usuario}/{senha}/{id}.{ext}   formato do protocolo Xtream, que o XC_VM entende
//	/stream/{id}?exp=&sig=                URL assinada e temporária, para player avulso
//
// O primeiro é estável e revogável (decisão D7); o segundo não exige criar credencial.
func (p *Proxy) Rotas(r chi.Router, confiarProxy bool) {
	r.Get("/movie/{usuario}/{senha}/{arquivo}", p.handleMovie(confiarProxy))
	r.Head("/movie/{usuario}/{senha}/{arquivo}", p.handleMovie(confiarProxy))
	r.Get("/series/{usuario}/{senha}/{arquivo}", p.handleSeries(confiarProxy))
	r.Head("/series/{usuario}/{senha}/{arquivo}", p.handleSeries(confiarProxy))

	r.Get("/stream/{id}", p.handleAssinado(confiarProxy, false))
	r.Head("/stream/{id}", p.handleAssinado(confiarProxy, false))
	r.Get("/stream/e/{id}", p.handleAssinado(confiarProxy, true))
	r.Head("/stream/e/{id}", p.handleAssinado(confiarProxy, true))
}

func (p *Proxy) handleMovie(confiarProxy bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.servirComCredencial(w, r, confiarProxy, false)
	}
}

func (p *Proxy) handleSeries(confiarProxy bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.servirComCredencial(w, r, confiarProxy, true)
	}
}

// servirComCredencial atende o formato /movie/{user}/{pass}/{id}.{ext}.
func (p *Proxy) servirComCredencial(w http.ResponseWriter, r *http.Request, confiarProxy, ehEpisodio bool) {
	ip := clientIP(r, confiarProxy)

	cred, err := p.auth.Autenticar(r.Context(),
		chi.URLParam(r, "usuario"), chi.URLParam(r, "senha"), ip)
	if err != nil {
		// O caminho pedido vai junto porque a causa mais comum de recusa não é senha
		// errada: é um cliente que montou a URL de um jeito diferente do que
		// entregamos. Sem ver o que foi pedido, isso vira adivinhação.
		p.log.Warn("acesso a vídeo negado",
			"caminho", r.URL.Path, "ip", ip, "usuario", chi.URLParam(r, "usuario"),
			"motivo", err)
		p.negarAcesso(w, err)
		return
	}

	// Limite de reproduções simultâneas desta credencial. É o que impede um cliente de
	// repassar a senha para dez pessoas às suas custas.
	liberar, ok := p.conexoes.Ocupar(cred)
	if !ok {
		w.Header().Set("Retry-After", "30")
		http.Error(w,
			"limite de reproduções simultâneas atingido para esta credencial",
			http.StatusTooManyRequests)
		return
	}
	defer liberar()

	id, ok := idDoArquivo(chi.URLParam(r, "arquivo"))
	if !ok {
		// Credencial válida e nome de arquivo que não vira número: o cliente alterou o
		// link que entregamos. É exatamente o caso que precisa aparecer no log.
		p.log.Warn("nome de arquivo inesperado na URL de vídeo",
			"caminho", r.URL.Path, "arquivo", chi.URLParam(r, "arquivo"),
			"credencial", cred.Name)
		http.Error(w, "identificador inválido", http.StatusBadRequest)
		return
	}

	ped, err := p.resolverPedido(r.Context(), id, ehEpisodio)
	if err != nil {
		p.responderResolucao(w, err)
		return
	}
	ped.credID = &cred.ID
	ped.clientIP = ip

	p.ServeContent(w, r, *ped)
}

// handleAssinado atende o formato /stream/{id}?exp=&sig=.
func (p *Proxy) handleAssinado(confiarProxy, ehEpisodio bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if err := p.auth.VerificarAssinatura(r.URL.Path, q.Get("exp"), q.Get("sig")); err != nil {
			p.negarAcesso(w, err)
			return
		}

		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "identificador inválido", http.StatusBadRequest)
			return
		}

		ped, err := p.resolverPedido(r.Context(), id, ehEpisodio)
		if err != nil {
			p.responderResolucao(w, err)
			return
		}
		ped.clientIP = clientIP(r, confiarProxy)

		p.ServeContent(w, r, *ped)
	}
}

func (p *Proxy) resolverPedido(ctx context.Context, id int64, ehEpisodio bool) (*pedido, error) {
	var (
		alvo      *store.StreamTarget
		variantes []store.PlayableVariant
		err       error
	)
	if ehEpisodio {
		alvo, variantes, err = p.store.ResolveEpisodeForStream(ctx, id)
	} else {
		alvo, variantes, err = p.store.ResolveContentForStream(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	return &pedido{alvo: alvo, variantes: variantes}, nil
}

func (p *Proxy) responderResolucao(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "conteúdo não encontrado", http.StatusNotFound)
		return
	}
	p.log.Error("falha ao resolver conteúdo para stream", "erro", err)
	http.Error(w, "erro interno", http.StatusInternalServerError)
}

// negarAcesso responde a uma falha de autenticação.
//
// Credencial inválida e credencial revogada dão respostas distintas de propósito: quem
// configurou o XC_VM precisa saber se errou a senha ou se o acesso foi cortado.
func (p *Proxy) negarAcesso(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrCotaEsgotada):
		// 402 é o código de "pagamento necessário", e é exatamente a situação: o cliente
		// consumiu o pacote. Distinguir de revogada importa para quem atende — são
		// conversas diferentes com o cliente.
		http.Error(w, "cota de banda esgotada", http.StatusPaymentRequired)
	case errors.Is(err, ErrCredencialRevogada):
		http.Error(w, "credencial revogada ou expirada", http.StatusForbidden)
	case errors.Is(err, ErrOrigemNaoPermitida):
		http.Error(w, "origem não autorizada", http.StatusForbidden)
	default:
		http.Error(w, "credencial inválida", http.StatusUnauthorized)
	}
}

// idDoArquivo extrai o identificador de "12345.mp4".
func idDoArquivo(arquivo string) (int64, bool) {
	nome := arquivo
	if idx := strings.LastIndexByte(nome, '.'); idx > 0 {
		nome = nome[:idx]
	}
	id, err := strconv.ParseInt(nome, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// LinkPublico monta a URL que um cliente usa para pedir um conteúdo.
//
// É o link que vai para o XC_VM. Ele aponta para o VOD Manager e nunca revela a fonte.
func LinkPublico(base, usuario, senha string, alvo *store.StreamTarget) string {
	segmento := "movie"
	if alvo.Kind == store.TargetEpisode {
		segmento = "series"
	}
	ext := alvo.Extension
	if ext == "" {
		ext = "mp4"
	}
	return strings.TrimRight(base, "/") + "/" + segmento + "/" +
		usuario + "/" + senha + "/" + strconv.FormatInt(alvo.ID, 10) + "." + ext
}
