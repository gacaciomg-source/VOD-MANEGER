package export

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"vodmanager/internal/edge"
	"vodmanager/internal/store"
)

// Deps são as dependências da saída pública do catálogo.
type Deps struct {
	Store *store.Store
	Auth  *edge.Authenticator
	Log   *slog.Logger
	// BaseURL resolve o endereço público a cada requisição. É função, e não string,
	// porque o administrador pode mudá-lo no painel sem reiniciar o processo — e um link
	// com o endereço errado é justamente o defeito que essa configuração existe para
	// corrigir.
	BaseURL func(*http.Request) string
	// TrustProxy repassa a decisão sobre X-Forwarded-For, para a restrição por CIDR das
	// credenciais continuar valendo atrás de um proxy reverso.
	TrustProxy bool
}

// Handler serve a lista M3U e a API Xtream.
type Handler struct {
	deps Deps
}

// New cria o handler da saída pública.
func New(deps Deps) *Handler {
	if deps.BaseURL == nil {
		deps.BaseURL = func(r *http.Request) string { return "http://" + r.Host }
	}
	return &Handler{deps: deps}
}

// Rotas registra os caminhos que os clientes de IPTV procuram.
//
// Os nomes com ".php" não são um servidor PHP: são o que o protocolo Xtream padronizou, e
// o que todo cliente existente pede. Mudá-los seria o mesmo que inventar um verbo HTTP
// próprio — funcionaria só com clientes escritos para nós.
func (h *Handler) Rotas(r chi.Router) {
	// Lista M3U. A forma com caminho é a nossa; get.php é a que os clientes conhecem.
	r.Get("/playlist/{usuario}/{senha}", h.handleM3U)
	r.Get("/get.php", h.handleM3U)

	// API Xtream.
	r.Get("/player_api.php", h.handlePlayerAPI)
	r.Post("/player_api.php", h.handlePlayerAPI)

	// Guia de programação. Não temos canais ao vivo, então a resposta é um guia vazio —
	// válido, e melhor que 404, que alguns clientes tratam como falha de conexão.
	r.Get("/xmltv.php", h.handleXMLTV)
}

// --- Autenticação -------------------------------------------------------------

type sessao struct {
	cred  *store.StreamCredential
	user  string
	senha string
}

// autenticar valida a credencial vinda da URL, aceitando as duas formas de passá-la.
func (h *Handler) autenticar(w http.ResponseWriter, r *http.Request) (*sessao, bool) {
	usuario := chi.URLParam(r, "usuario")
	senha := chi.URLParam(r, "senha")
	if usuario == "" {
		usuario = r.URL.Query().Get("username")
		senha = r.URL.Query().Get("password")
	}

	cred, err := h.deps.Auth.Autenticar(r.Context(), usuario, senha,
		edge.ClientIP(r, h.deps.TrustProxy))
	if err != nil {
		// A resposta é a mesma para credencial inexistente, senha errada e credencial
		// revogada: distinguir permitiria descobrir usuários válidos por tentativa.
		h.deps.Log.Info("acesso negado ao catálogo",
			"usuario", usuario, "motivo", motivo(err))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"user_info": map[string]any{"auth": 0, "status": "Disabled"},
		})
		return nil, false
	}
	return &sessao{cred: cred, user: usuario, senha: senha}, true
}

func motivo(err error) string {
	switch {
	case errors.Is(err, edge.ErrCredencialRevogada):
		return "revogada ou expirada"
	case errors.Is(err, edge.ErrOrigemNaoPermitida):
		return "origem não autorizada"
	default:
		return "credencial inválida"
	}
}

// --- Lista M3U ----------------------------------------------------------------

func (h *Handler) handleM3U(w http.ResponseWriter, r *http.Request) {
	s, ok := h.autenticar(w, r)
	if !ok {
		return
	}

	// O que incluir. Sem parâmetro, vai tudo — é o que o cliente espera de uma lista.
	tipo := strings.ToLower(r.URL.Query().Get("conteudo"))
	incluirFilmes := tipo != "series"
	incluirSeries := tipo != "filmes" && tipo != "movies"

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="lista.m3u"`)
	// A lista muda a cada sincronização: guardá-la em cache intermediário entregaria um
	// catálogo desatualizado sem que ninguém percebesse.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	esc := NovoEscritorM3U(w, h.deps.BaseURL(r), s.user, s.senha)
	inicio := time.Now()

	if incluirFilmes {
		err := h.deps.Store.EachExportMovie(r.Context(), nil, esc.Filme)
		if err != nil {
			h.abortarLista(r.Context(), err, esc.Itens())
			return
		}
	}
	if incluirSeries {
		err := h.deps.Store.EachExportEpisode(r.Context(), nil, esc.Episodio)
		if err != nil {
			h.abortarLista(r.Context(), err, esc.Itens())
			return
		}
	}

	itens, err := esc.Finalizar()
	if err != nil {
		h.abortarLista(r.Context(), err, itens)
		return
	}
	h.deps.Log.Info("lista M3U entregue",
		"credencial", s.cred.Name, "itens", itens, "duracao", time.Since(inicio).String())
}

// abortarLista registra a interrupção. O cabeçalho já foi enviado, então não há como
// devolver um erro HTTP: o cliente vê uma lista truncada, e o log diz por quê.
func (h *Handler) abortarLista(ctx context.Context, err error, itens int) {
	if ctx.Err() != nil {
		h.deps.Log.Info("cliente desconectou durante a lista", "itens_enviados", itens)
		return
	}
	h.deps.Log.Error("lista M3U interrompida", "erro", err, "itens_enviados", itens)
}

// --- API Xtream ---------------------------------------------------------------

func (h *Handler) handlePlayerAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := h.autenticar(w, r)
	if !ok {
		return
	}

	acao := r.URL.Query().Get("action")
	if acao == "" && r.Method == http.MethodPost {
		acao = r.FormValue("action")
	}

	switch acao {
	case "":
		// Handshake: o cliente pergunta quem ele é antes de pedir qualquer catálogo.
		h.json(w, r, h.handshake(r, s))

	case "get_vod_categories":
		h.categorias(w, r, "movie")
	case "get_series_categories":
		h.categorias(w, r, "series")

	case "get_vod_streams":
		h.filmes(w, r)
	case "get_series":
		h.series(w, r)

	case "get_series_info":
		h.detalheSerie(w, r)
	case "get_vod_info":
		h.detalheFilme(w, r)

	case "get_live_categories", "get_live_streams", "get_short_epg", "get_simple_data_table":
		// Não servimos canais ao vivo. Lista vazia é a resposta correta: o cliente
		// simplesmente não mostra a aba, em vez de exibir erro.
		h.json(w, r, []any{})

	default:
		h.json(w, r, []any{})
	}
}

func (h *Handler) handshake(r *http.Request, s *sessao) map[string]any {
	agora := time.Now()

	expira := ""
	if s.cred.ExpiresAt != nil {
		expira = strconv.FormatInt(s.cred.ExpiresAt.Unix(), 10)
	}
	maxCon := "0" // 0 significa "sem limite" no protocolo.
	if s.cred.MaxConnections != nil {
		maxCon = strconv.Itoa(*s.cred.MaxConnections)
	}

	base := h.deps.BaseURL(r)
	esquema, host, porta := partesDoEndereco(base)

	usuario := UsuarioXtream{
		Username:             s.user,
		Password:             s.senha,
		Message:              "",
		Auth:                 1,
		Status:               "Active",
		ExpDate:              expira,
		IsTrial:              "0",
		ActiveCons:           "0",
		CreatedAt:            strconv.FormatInt(s.cred.CreatedAt.Unix(), 10),
		MaxConnections:       maxCon,
		AllowedOutputFormats: []string{"mp4", "mkv", "ts"},
	}
	servidor := ServidorXtream{
		URL:            host,
		Port:           porta,
		HTTPSPort:      porta,
		ServerProtocol: esquema,
		RTMPPort:       "0",
		Timezone:       agora.Location().String(),
		TimestampNow:   agora.Unix(),
		TimeNow:        agora.Format("2006-01-02 15:04:05"),
		Process:        true,
	}
	return map[string]any{"user_info": usuario, "server_info": servidor}
}

func (h *Handler) categorias(w http.ResponseWriter, r *http.Request, tipo string) {
	cats, err := h.deps.Store.ExportCategories(r.Context(), tipo)
	if err != nil {
		h.erro(w, r, err, "listando categorias")
		return
	}
	h.json(w, r, categoriasXtream(cats))
}

func (h *Handler) filmes(w http.ResponseWriter, r *http.Request) {
	filtro := categoriaDaQuery(r)

	// A resposta é um array JSON escrito em fluxo: 16 mil filmes montados de uma vez em
	// memória seriam dezenas de megabytes por cliente conectado.
	h.arrayEmFluxo(w, r, func(enc *json.Encoder, sep func() error) error {
		num := 0
		return h.deps.Store.EachExportMovie(r.Context(), filtro, func(m store.ExportMovie) error {
			num++
			if err := sep(); err != nil {
				return err
			}
			return enc.Encode(filmeXtream(m, num))
		})
	})
}

func (h *Handler) series(w http.ResponseWriter, r *http.Request) {
	filtro := categoriaDaQuery(r)
	h.arrayEmFluxo(w, r, func(enc *json.Encoder, sep func() error) error {
		num := 0
		return h.deps.Store.EachExportSeries(r.Context(), filtro, func(s store.ExportSeries) error {
			num++
			if err := sep(); err != nil {
				return err
			}
			return enc.Encode(serieXtream(s, num))
		})
	})
}

func (h *Handler) detalheSerie(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("series_id"), 10, 64)
	if err != nil {
		h.json(w, r, map[string]any{})
		return
	}
	serie, err := h.deps.Store.GetExportSeries(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.json(w, r, map[string]any{})
			return
		}
		h.erro(w, r, err, "buscando série")
		return
	}

	// Os episódios vêm agrupados por temporada, com a chave em texto: é assim que o
	// cliente monta a árvore de pastas.
	porTemporada := map[string][]EpisodioXtream{}
	temporadas := []map[string]any{}
	vistas := map[int]bool{}

	err = h.deps.Store.EachExportEpisode(r.Context(), &id, func(e store.ExportEpisode) error {
		chave := strconv.Itoa(e.SeasonNumber)
		porTemporada[chave] = append(porTemporada[chave], episodioXtream(e))
		if !vistas[e.SeasonNumber] {
			vistas[e.SeasonNumber] = true
			temporadas = append(temporadas, map[string]any{
				"season_number": e.SeasonNumber,
				"name":          "Temporada " + strconv.Itoa(e.SeasonNumber),
				"cover":         serie.PosterURL,
			})
		}
		return nil
	})
	if err != nil {
		h.erro(w, r, err, "listando episódios")
		return
	}

	backdrops := []string{}
	if serie.BackdropURL != "" {
		backdrops = append(backdrops, serie.BackdropURL)
	}
	h.json(w, r, map[string]any{
		"seasons": temporadas,
		"info": InfoSerieXtream{
			Name:         serie.Title + marcaDeIdioma(serie.LanguageKey),
			Cover:        serie.PosterURL,
			Plot:         serie.Plot,
			ReleaseDate:  anoTexto(serie.Year),
			Rating:       textoNota(serie.Rating),
			Rating5Based: nota5(serie.Rating),
			BackdropPath: backdrops,
			CategoryID:   strconv.FormatInt(serie.CategoryID, 10),
		},
		"episodes": porTemporada,
	})
}

func (h *Handler) detalheFilme(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("vod_id"), 10, 64)
	if err != nil {
		h.json(w, r, map[string]any{})
		return
	}
	m, err := h.deps.Store.GetExportMovie(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.json(w, r, map[string]any{})
			return
		}
		h.erro(w, r, err, "buscando filme")
		return
	}

	segundos := 0
	if m.Duration != nil {
		segundos = *m.Duration
	}
	h.json(w, r, map[string]any{
		"info": InfoFilmeXtream{
			MovieImage:   m.PosterURL,
			Plot:         m.Plot,
			ReleaseDate:  anoTexto(m.Year),
			Rating:       textoNota(m.Rating),
			Rating5Based: nota5(m.Rating),
			Duration:     duracaoTexto(segundos),
			DurationSecs: segundos,
		},
		"movie_data": DadosFilmeXtream{
			StreamID:           m.ID,
			Name:               nomeComAno(m.Title, m.Year) + marcaDeIdioma(m.LanguageKey),
			Added:              strconv.FormatInt(m.AddedAt, 10),
			CategoryID:         strconv.FormatInt(m.CategoryID, 10),
			ContainerExtension: extensaoOu(m.Extension),
		},
	})
}

// --- EPG ----------------------------------------------------------------------

func (h *Handler) handleXMLTV(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.autenticar(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<tv generator-info-name="VOD Manager"></tv>` + "\n"))
}

// --- Utilidades ---------------------------------------------------------------

func (h *Handler) json(w http.ResponseWriter, _ *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.deps.Log.Warn("resposta interrompida", "erro", err)
	}
}

// arrayEmFluxo escreve um array JSON item a item, sem montá-lo inteiro em memória.
//
// O encoder do Go termina cada Encode com uma quebra de linha, o que dentro de um array
// é espaço em branco irrelevante — e poupa a concatenação manual de cada item.
func (h *Handler) arrayEmFluxo(w http.ResponseWriter, r *http.Request,
	percorrer func(*json.Encoder, func() error) error) {

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	buf := novoBufferHTTP(w)
	enc := json.NewEncoder(buf)

	if _, err := buf.WriteString("["); err != nil {
		return
	}
	primeiro := true
	separador := func() error {
		if primeiro {
			primeiro = false
			return nil
		}
		_, err := buf.WriteString(",")
		return err
	}

	if err := percorrer(enc, separador); err != nil {
		// Cabeçalho já enviado: não há status de erro para devolver. Fechamos o array
		// para que o cliente ao menos consiga interpretar o que recebeu.
		if r.Context().Err() == nil {
			h.deps.Log.Error("resposta Xtream interrompida", "erro", err)
		}
	}
	buf.WriteString("]")
	buf.Flush()
}

func (h *Handler) erro(w http.ResponseWriter, _ *http.Request, err error, contexto string) {
	h.deps.Log.Error("falha na saída do catálogo", "contexto", contexto, "erro", err)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]any{"error": "falha ao montar a resposta"})
}

func categoriaDaQuery(r *http.Request) *int64 {
	bruto := r.URL.Query().Get("category_id")
	if bruto == "" || bruto == "0" {
		return nil
	}
	id, err := strconv.ParseInt(bruto, 10, 64)
	if err != nil {
		return nil
	}
	return &id
}

// partesDoEndereco separa o endereço público nos pedaços que o handshake exige.
//
// O cliente monta URLs a partir deles: entregar host e porta errados faz o catálogo
// carregar e nenhum vídeo abrir.
func partesDoEndereco(base string) (esquema, host, porta string) {
	esquema = "http"
	resto := base
	if s, r, ok := strings.Cut(base, "://"); ok {
		esquema, resto = s, r
	}
	resto = strings.TrimSuffix(resto, "/")
	if h, p, ok := strings.Cut(resto, ":"); ok {
		return esquema, h, p
	}
	if esquema == "https" {
		return esquema, resto, "443"
	}
	return esquema, resto, "80"
}
