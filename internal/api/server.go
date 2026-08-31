package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"vodmanager/internal/armazenamento"
	"vodmanager/internal/auth"
	"vodmanager/internal/cryptobox"
	"vodmanager/internal/edge"
	"vodmanager/internal/export"
	"vodmanager/internal/metrics"
	"vodmanager/internal/panel"
	"vodmanager/internal/roles"
	"vodmanager/internal/store"
	vsync "vodmanager/internal/sync"
	"vodmanager/internal/sysinfo"
)

// Deps são as dependências injetadas na API. Nada de estado global: é o que permite
// instanciar a API (ou não) conforme o papel do processo.
type Deps struct {
	Store  *store.Store
	Auth   *auth.Service
	Crypto *cryptobox.Box
	// Sync é nulo quando o processo não roda o plano de controle (papel node). Os
	// handlers que dependem dele respondem 503 em vez de estourar.
	Sync *vsync.Scheduler
	// StreamAuth e StreamProxy formam o plano de dados. Nulos quando o processo não
	// serve vídeo.
	StreamAuth  *edge.Authenticator
	StreamProxy *edge.Proxy
	// PublicBaseURL é o endereço pelo qual o mundo alcança este servidor. Necessário
	// quando há proxy reverso na frente: o Host da requisição interna não serve para
	// montar o link que vai para o XC_VM.
	PublicBaseURL string
	Log           *slog.Logger
	Metrics       *metrics.Registry
	NodeID        string
	CookieName    string
	CookieSecure  bool
	TrustProxy    bool
	Version       string
	// Armazenamento reúne os backends de acervo montados. Nulo quando este processo não
	// guarda mídia — os handlers do acervo respondem sem a parte de espaço em vez de
	// estourar.
	Armazenamento *armazenamento.Registro
	// Nuvens monta contas de nuvem sob demanda.
	//
	// Interface estreita, e nao o servico do acervo inteiro: o unico poder que a API
	// precisa aqui e falar com uma conta ja cadastrada. Passar o servico completo daria
	// ao plano de controle acesso a decisoes de guarda e limpeza que nao sao dele.
	Nuvens MontadorDeNuvens
	// Categorizador classifica conteudos por genero via TMDB. Nulo quando nao ha chave —
	// e nulo e um estado legitimo: o sistema inteiro funciona sem o recurso.
	Categorizador *vsync.Categorizador
	// Sistema mede o consumo de recursos da máquina. Nulo desliga a tela de sistema em
	// vez de derrubar o processo.
	Sistema *sysinfo.Coletor
}

// Server é a API administrativa.
type Server struct {
	deps   Deps
	router chi.Router
}

// NewServer monta o roteador.
func NewServer(deps Deps) *Server {
	if deps.CookieName == "" {
		deps.CookieName = "vodm_session"
	}
	s := &Server{deps: deps}
	s.router = s.routes()
	return s
}

// ServeHTTP faz do Server um http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.router.ServeHTTP(w, r) }

func (s *Server) routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(s.observe)

	// Endpoints operacionais: sem autenticação, porque o orquestrador precisa deles
	// antes de qualquer credencial existir. Nenhum deles expõe dado de negócio.
	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	if s.deps.Metrics != nil {
		r.Handle("/metrics", promhttp.HandlerFor(s.deps.Metrics.Gatherer(), promhttp.HandlerOpts{}))
	}

	// Rotas PÚBLICAS de streaming. Ficam fora de /api/v1 e não usam a sessão do painel:
	// elas têm autenticação própria, por credencial de saída ou URL assinada.
	if s.deps.StreamProxy != nil {
		s.deps.StreamProxy.Rotas(r, s.deps.TrustProxy)
	}

	// Saída pública do catálogo: lista M3U e API Xtream. Usa a mesma credencial de saída
	// do streaming de propósito — quem pode assistir pode listar, e revogar a credencial
	// corta as duas coisas de uma vez, sem deixar meia porta aberta.
	if s.deps.StreamAuth != nil {
		export.New(export.Deps{
			Store: s.deps.Store,
			Auth:  s.deps.StreamAuth,
			Log:   s.deps.Log,
			// Endereço de CONTEÚDO, não o do painel: a lista entregue ao cliente não
			// deve revelar por onde o sistema é administrado.
			BaseURL:    s.baseURLConteudo,
			TrustProxy: s.deps.TrustProxy,
		}).Rotas(r)
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/login", s.handleLogin)

		r.Group(func(r chi.Router) {
			r.Use(s.deps.Auth.Middleware(s.deps.CookieName, s.deny))

			r.Get("/auth/me", s.handleMe)
			r.Post("/auth/logout", s.handleLogout)
			// Cada um troca a própria senha; gestão de outros usuários é outra tela.
			r.Post("/auth/change-password", s.handleChangePassword)

			r.Get("/sources", s.handleListSources)
			r.Get("/sources/{id}", s.handleGetSource)
			r.Get("/events", s.handleListEvents)

			// Catálogo (leitura).
			r.Get("/contents", s.handleListContents)
			r.Get("/contents/{id}", s.handleGetContent)
			r.Get("/episodes/{id}", s.handleGetEpisode)
			r.Get("/categories", s.handleListCategories)
			r.Get("/source-categories", s.handleListSourceCategories)
			r.Get("/categorias/pendencias", s.handleListPendencias)
			r.Get("/categorias/apelidos", s.handleListApelidos)
			r.Get("/duplicatas", s.handleListDuplicatas)
			r.Get("/unresolved", s.handleListUnresolved)
			r.Get("/sync/runs", s.handleListSyncRuns)
			r.Get("/sync/runs/{id}", s.handleGetSyncRun)
			r.Get("/stats/dashboard", s.handleDashboard)
			r.Get("/maintenance/orphan-contents", s.handleOrphanPreview)
			r.Get("/settings", s.handleGetSettings)
			r.Get("/system", s.handleSystem)
			r.Get("/system/update", s.handleUpdateStatus)
			r.Get("/system/dominio", s.handleDominioStatus)
			r.Get("/system/migracao", s.handleMigracaoStatus)
			r.Get("/falhas", s.handleFalhas)
			r.Get("/acervo/estimativa", s.handleEstimativaDeArmazenamento)
			r.Get("/catalogo/classificacao", s.handleAndamentoDaClassificacao)
			r.Get("/trafego", s.handleTrafego)

			// Acervo: o que esta operação guarda, e onde.
			r.Get("/acervo", s.handleAcervoResumo)
			r.Get("/acervo/arquivos", s.handleListarArquivos)
			r.Get("/nuvens", s.handleListarNuvens)

			// Streaming: links de reprodução e credenciais de saída.
			r.Get("/contents/{id}/playback", s.handlePlaybackLinks)
			r.Get("/episodes/{id}/playback", s.handlePlaybackLinks)
			r.Get("/stream-credentials", s.handleListStreamCredentials)
			r.Get("/streams", s.handleListActiveStreams)

			// Escrita exige admin ou operator.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(s.deny, store.RoleAdmin, store.RoleOperator))
				r.Post("/sources", s.handleCreateSource)
				r.Patch("/sources/{id}", s.handleUpdateSource)
				r.Delete("/sources/{id}", s.handleDeleteSource)
				r.Post("/sources/reorder", s.handleReorderSources)
				r.Put("/sources/{id}/credentials", s.handleSetSourceCredential)
				r.Delete("/sources/{id}/credentials", s.handleDeleteSourceCredential)
				r.Post("/sources/{id}/sync", s.handleSyncSource)
				r.Post("/sources/{id}/test", s.handleTestSource)
				r.Post("/maintenance/orphan-contents/purge", s.handleOrphanPurge)
				r.Put("/settings", s.handleUpdateSettings)

				// Acervo. Proteger e apagar mexem no que os clientes assistem, e apagar
				// acervo próprio é perda definitiva — daí exigirem papel de escrita e
				// ficarem registrados em evento.
				r.Put("/acervo/arquivos/{id}/proteger", s.handleProtegerArquivo)
				r.Delete("/acervo/arquivos/{id}", s.handleApagarArquivo)
				// Envio de arquivo: streaming, sem o limite de 1 MiB que vale para o
				// resto da API — aqui o corpo tem gigabytes.
				r.Post("/acervo/enviar", s.handleEnviarArquivo)
				// As credenciais de uma conta de nuvem dão acesso total a ela: só
				// administrador cadastra ou remove.
				r.Group(func(r chi.Router) {
					r.Use(auth.RequireRole(s.deny, store.RoleAdmin))
					r.Post("/nuvens", s.handleCriarNuvem)
					// Autorização do Google: o painel manda começar, o Google devolve o
					// navegador no retorno.
					r.Post("/nuvens/oauth/iniciar", s.handleIniciarOAuthDrive)
					r.Get("/nuvens/oauth/retorno", s.handleRetornoOAuthDrive)
					r.Post("/acervo/arquivos/{id}/tentar", s.handleTentarDeNovo)
					r.Post("/acervo/limpar-invalidas", s.handleLimparInvalidas)
					r.Post("/acervo/esvaziar", s.handleEsvaziarAcervo)
					r.Post("/duplicatas/unir-tudo", s.handleUnirTodasDuplicatas)
					r.Post("/catalogo/classificacao", s.handleIniciarClassificacao)
					r.Delete("/catalogo/classificacao", s.handlePararClassificacao)
					r.Post("/nuvens/{id}/pasta", s.handleOrganizarNuvem)
					r.Post("/nuvens/{id}/esvaziar", s.handleEsvaziarNuvem)
					r.Patch("/nuvens/{id}", s.handleAjustarNuvem)
					r.Delete("/nuvens/{id}", s.handleRemoverNuvem)
				})
				// Trocar o próprio binário e reiniciar o serviço é a ação mais destrutiva do
				// painel: só administrador.
				// Gestão de usuários e atualização do sistema: só administrador. Dar a alguém o
				// poder de criar contas é dar o poder de se promover.
				r.Group(func(r chi.Router) {
					r.Use(auth.RequireRole(s.deny, store.RoleAdmin))
					r.Post("/system/update", s.handleUpdateStart)
					r.Post("/system/dominio", s.handleConfigurarDominio)
					// Migrar leva o catálogo inteiro, a chave de criptografia e os
					// usuários para outra máquina: só administrador.
					r.Post("/system/migracao", s.handleMigrarStart)
					r.Get("/users", s.handleListUsers)
					r.Post("/users", s.handleCreateUser)
					r.Patch("/users/{id}", s.handleUpdateUser)
					r.Delete("/users/{id}", s.handleDeleteUser)
				})
				// Devolve a senha de saída em claro: exige escrita e é registrado em evento.
				r.Get("/stream-credentials/{id}/links", s.handleCredentialLinks)
				r.Post("/stream-credentials", s.handleCreateStreamCredential)
				r.Patch("/stream-credentials/{id}", s.handleUpdateStreamCredential)
				r.Post("/stream-credentials/{id}/rotate", s.handleRotateStreamCredential)
				r.Post("/stream-credentials/{id}/revoke", s.handleRevokeStreamCredential)
				r.Delete("/stream-credentials/{id}", s.handleDeleteStreamCredential)
				r.Post("/source-categories/{id}/map", s.handleMapSourceCategory)
				r.Patch("/categories/{id}", s.handleRenameCategory)
				r.Put("/categories/{id}/principal", s.handleMarcarPrincipal)
				r.Post("/categories/{id}/absorver", s.handleAbsorverCategoria)
				r.Post("/categorias/pendencias/{id}/resolver", s.handleResolverPendencia)
				// Desfazer uma união: soltar o nome, ou devolvê-lo à condição de pasta.
				r.Delete("/categorias/apelidos/{id}", s.handleRemoverApelido)
				r.Post("/categorias/apelidos/{id}/reativar", s.handleReativarApelido)
				// Unir conteúdos apaga um identificador que clientes podem ter importado: exige
				// papel de escrita e fica registrado em evento.
				r.Post("/duplicatas/decidir", s.handleDecidirDuplicata)
				// Expõe a URL de origem: exige escrita e é registrado em evento.
				r.Get("/variants/{vid}/origin-url", s.handleVariantOriginURL)
			})
		})
	})

	// Painel web. Rota não encontrada fora de /api devolve o index, porque o roteamento
	// entre telas acontece no navegador.
	painel, err := panel.Handler()
	if err != nil {
		s.deps.Log.Error("painel indisponível", "erro", err)
	}

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if painel == nil || strings.HasPrefix(req.URL.Path, "/api/") {
			writeError(w, s.deps.Log, http.StatusNotFound, "not_found", "rota inexistente")
			return
		}
		painel.ServeHTTP(w, req)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, s.deps.Log, http.StatusMethodNotAllowed, "method_not_allowed", "método não permitido nesta rota")
	})
	return r
}

func (s *Server) deny(w http.ResponseWriter, _ *http.Request, status int, code, message string) {
	writeError(w, s.deps.Log, status, code, message)
}

// observe mede latência e conta respostas por status.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		if s.deps.Metrics != nil {
			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "desconhecida"
			}
			s.deps.Metrics.ObserveHTTP(r.Method, route, ww.Status(), time.Since(start))
		}
	})
}

// Module adapta o Server ao registro de módulos (papel: manager).
type Module struct {
	server   *Server
	http     *http.Server
	log      *slog.Logger
	addr     string
	shutdown time.Duration
	errCh    chan error
}

// NewModule cria o módulo HTTP da API.
func NewModule(server *Server, addr string, shutdown time.Duration, log *slog.Logger) *Module {
	return &Module{
		server:   server,
		addr:     addr,
		shutdown: shutdown,
		log:      log,
		errCh:    make(chan error, 1),
	}
}

// Name identifica o módulo.
func (m *Module) Name() string { return "api" }

// Roles: a API administrativa só roda no Manager.
func (m *Module) Roles() []roles.Role { return []roles.Role{roles.RoleManager} }

// Start sobe o listener HTTP.
func (m *Module) Start(context.Context) error {
	m.http = &http.Server{
		Addr:              m.addr,
		Handler:           m.server,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if err := m.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			m.log.Error("servidor HTTP encerrou com erro", "erro", err)
			m.errCh <- err
			return
		}
		m.errCh <- nil
	}()
	m.log.Info("API ouvindo", "addr", m.addr)
	return nil
}

// Stop encerra o listener de forma graciosa.
func (m *Module) Stop(ctx context.Context) error {
	if m.http == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.shutdown)
	defer cancel()
	return m.http.Shutdown(shutdownCtx)
}

// Err devolve o canal de erro fatal do listener.
func (m *Module) Err() <-chan error { return m.errCh }

// MontadorDeNuvens é o que a API precisa saber sobre contas de nuvem.
type MontadorDeNuvens interface {
	BackendDaNuvem(ctx context.Context, id int64) (armazenamento.Backend, error)
}
