package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"vodmanager/internal/ingest"
	"vodmanager/internal/store"
	vsync "vodmanager/internal/sync"
)

func (s *Server) handleListContents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filtro := store.ContentFilter{
		Type:   q.Get("type"),
		Status: q.Get("status"),
		Search: q.Get("q"),
	}
	if v := q.Get("category_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_query", "category_id inválido", "category_id")
			return
		}
		filtro.CategoryID = &id
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_query", "limit precisa ser um inteiro positivo", "limit")
			return
		}
		filtro.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_query", "offset inválido", "offset")
			return
		}
		filtro.Offset = n
	}
	if t := filtro.Type; t != "" && t != store.ContentMovie && t != store.ContentSeries {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_query", "type precisa ser movie ou series", "type")
		return
	}

	page, err := s.deps.Store.ListContents(r.Context(), filtro)
	if err != nil {
		s.fail(w, r, err, "listando conteúdos")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, page)
}

// handleGetContent devolve o conteúdo com suas variantes (sem URLs de origem).
func (s *Server) handleGetContent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id de conteúdo inválido")
		return
	}
	content, err := s.deps.Store.GetContent(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "buscando conteúdo")
		return
	}

	resposta := map[string]any{"content": content}

	if content.Type == store.ContentSeries {
		temporadas, err := s.deps.Store.ListSeasons(r.Context(), id)
		if err != nil {
			s.fail(w, r, err, "listando temporadas")
			return
		}
		resposta["seasons"] = temporadas
	} else {
		variantes, err := s.deps.Store.ListVariantsForTarget(r.Context(), store.TargetContent, id)
		if err != nil {
			s.fail(w, r, err, "listando variantes")
			return
		}
		resposta["variants"] = variantes
	}
	writeJSON(w, s.deps.Log, http.StatusOK, resposta)
}

func (s *Server) handleGetEpisode(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id de episódio inválido")
		return
	}
	ep, err := s.deps.Store.GetEpisode(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "buscando episódio")
		return
	}
	variantes, err := s.deps.Store.ListVariantsForTarget(r.Context(), store.TargetEpisode, id)
	if err != nil {
		s.fail(w, r, err, "listando variantes")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"episode": ep, "variants": variantes})
}

func (s *Server) handleListCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.deps.Store.ListCategories(r.Context())
	if err != nil {
		s.fail(w, r, err, "listando categorias")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"categories": cats})
}

// handleListSourceCategories devolve as categorias declaradas pelas fontes, cada uma com
// sugestões de categorias canônicas parecidas.
func (s *Server) handleListSourceCategories(w http.ResponseWriter, r *http.Request) {
	itens, err := s.deps.Store.ListSourceCategories(r.Context())
	if err != nil {
		s.fail(w, r, err, "listando categorias das fontes")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"source_categories": itens})
}

type mapearCategoriaRequest struct {
	// Informe CategoryID para usar uma categoria existente, ou NewName para criar uma.
	CategoryID *int64  `json:"category_id"`
	NewName    *string `json:"new_name"`
}

// handleMapSourceCategory unifica uma categoria de fonte em uma categoria canônica.
func (s *Server) handleMapSourceCategory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	var req mapearCategoriaRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}

	categoriaID := int64(0)
	switch {
	case req.CategoryID != nil && *req.CategoryID > 0:
		categoriaID = *req.CategoryID
	case req.NewName != nil && strings.TrimSpace(*req.NewName) != "":
		nome := strings.TrimSpace(*req.NewName)
		// O tipo vem da própria categoria de fonte que está sendo remapeada.
		tipo, err := s.deps.Store.SourceCategoryContentType(r.Context(), id)
		if err != nil {
			s.fail(w, r, err, "buscando categoria da fonte")
			return
		}
		criada, err := s.deps.Store.EnsureCategory(r.Context(), nome, ingest.NormalizeName(nome), tipo)
		if err != nil {
			s.fail(w, r, err, "criando categoria")
			return
		}
		categoriaID = criada
	default:
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe category_id (existente) ou new_name (nova)", "category_id", "new_name")
		return
	}

	movidos, err := s.deps.Store.MapSourceCategory(r.Context(), id, categoriaID)
	if err != nil {
		s.fail(w, r, err, "remapeando categoria")
		return
	}

	s.logEvent(r, "source", "info", "categoria de fonte remapeada", actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"category_id":    categoriaID,
		"contents_moved": movidos,
	})
}

type renomearCategoriaRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleRenameCategory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	var req renomearCategoriaRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "informe o nome", "name")
		return
	}
	if err := s.deps.Store.RenameCategory(r.Context(), id, strings.TrimSpace(req.Name)); err != nil {
		s.fail(w, r, err, "renomeando categoria")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListUnresolved(w http.ResponseWriter, r *http.Request) {
	var sourceID *int64
	if v := r.URL.Query().Get("source_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_query", "source_id inválido", "source_id")
			return
		}
		sourceID = &id
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v)
	}

	itens, err := s.deps.Store.ListUnresolved(r.Context(), sourceID, limit)
	if err != nil {
		s.fail(w, r, err, "listando itens não resolvidos")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"items": itens})
}

// handleOrphanPreview conta os conteúdos que ficaram sem nenhuma origem.
func (s *Server) handleOrphanPreview(w http.ResponseWriter, r *http.Request) {
	previa, err := s.deps.Store.PreviewOrphanCleanup(r.Context())
	if err != nil {
		s.fail(w, r, err, "prevendo limpeza")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, previa)
}

// handleOrphanPurge remove conteúdos sem nenhuma origem.
//
// Exclusão em massa por ação explícita do administrador. Conteúdos marcados como
// preservados nunca são removidos, nem aqui.
func (s *Server) handleOrphanPurge(w http.ResponseWriter, r *http.Request) {
	removidos, err := s.deps.Store.PurgeOrphanContents(r.Context())
	if err != nil {
		s.fail(w, r, err, "limpando conteúdos sem origem")
		return
	}
	s.logEvent(r, "content", "warn", fmt.Sprintf(
		"limpeza manual: %d filmes, %d séries e %d episódios sem origem foram removidos",
		removidos.Movies, removidos.Series, removidos.Episodes), actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, removidos)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	stats, err := s.deps.Store.GetCatalogStats(r.Context())
	if err != nil {
		s.fail(w, r, err, "consultando estatísticas")
		return
	}
	runs, err := s.deps.Store.ListSyncRuns(r.Context(), nil, 5)
	if err != nil {
		s.fail(w, r, err, "listando execuções")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"catalog":      stats,
		"recent_syncs": runs,
		"node_id":      s.deps.NodeID,
		"version":      s.deps.Version,
	})
}

// --- Sincronização -----------------------------------------------------------

// handleSyncSource dispara uma sincronização manual.
//
// Responde 202 com a execução recém-aberta e trabalha em segundo plano. O painel
// acompanha o progresso por GET /sync/runs/{id}, e fechar o navegador não interrompe
// a sincronização.
func (s *Server) handleSyncSource(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sync == nil {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "sync_disabled",
			"este processo não executa sincronização (papel node)")
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id de fonte inválido")
		return
	}

	run, err := s.deps.Sync.SyncNow(r.Context(), id)
	switch {
	case errors.Is(err, vsync.ErrJaEmExecucao):
		writeError(w, s.deps.Log, http.StatusConflict, "sync_in_progress",
			"já existe uma sincronização em andamento para esta fonte")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, s.deps.Log, http.StatusNotFound, "not_found", "fonte não encontrada")
		return
	case err != nil:
		writeError(w, s.deps.Log, http.StatusConflict, "sync_busy", err.Error())
		return
	}

	s.logEvent(r, "sync", "info", "sincronização manual iniciada", actorOf(r), &id)
	writeJSON(w, s.deps.Log, http.StatusAccepted, run)
}

func (s *Server) handleTestSource(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sync == nil {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "sync_disabled",
			"este processo não testa fontes (papel node)")
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id de fonte inválido")
		return
	}

	if err := s.deps.Sync.Orchestrator().TestSource(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, s.deps.Log, http.StatusNotFound, "not_found", "fonte não encontrada")
			return
		}
		writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
			"ok":     false,
			"detail": err.Error(),
		})
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"ok": true, "detail": "a fonte respondeu corretamente"})
}

func (s *Server) handleListSyncRuns(w http.ResponseWriter, r *http.Request) {
	var sourceID *int64
	if v := r.URL.Query().Get("source_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_query", "source_id inválido", "source_id")
			return
		}
		sourceID = &id
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v)
	}

	runs, err := s.deps.Store.ListSyncRuns(r.Context(), sourceID, limit)
	if err != nil {
		s.fail(w, r, err, "listando execuções")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleGetSyncRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id de execução inválido")
		return
	}
	run, err := s.deps.Store.GetSyncRun(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "buscando execução")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, run)
}

// handleVariantOriginURL devolve a URL original de uma variante ao administrador.
//
// É o único endpoint que expõe esse dado, ele exige papel de escrita e cada acesso é
// registrado em evento — a URL de origem é justamente o que o sistema existe para
// esconder do cliente final.
func (s *Server) handleVariantOriginURL(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sync == nil {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "sync_disabled",
			"este processo não resolve URLs de origem")
		return
	}
	id, err := pathID(r, "vid")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id de variante inválido")
		return
	}

	variant, err := s.deps.Store.GetVariant(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "buscando variante")
		return
	}
	url, err := s.deps.Sync.Orchestrator().ResolveStreamURL(r.Context(), variant)
	if err != nil {
		s.fail(w, r, err, "resolvendo URL de origem")
		return
	}

	s.logEvent(r, "source", "warn", "URL de origem consultada pelo administrador", actorOf(r), &variant.SourceID)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"variant_id": variant.ID,
		"source_id":  variant.SourceID,
		"origin_url": url,
	})
}

// handleClassificarPorGenero começa (ou consulta) a classificação automática.
//
// GET devolve o andamento; POST começa; DELETE interrompe. Três verbos no mesmo lugar porque
// são três perguntas sobre a MESMA coisa, e separá-las em três endereços faria a tela ter de
// saber qual usar quando.
func (s *Server) handleAndamentoDaClassificacao(w http.ResponseWriter, r *http.Request) {
	if s.deps.Categorizador == nil {
		writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"disponivel": false})
		return
	}
	filmes, series, err := s.deps.Store.ContarSemCategoria(r.Context())
	if err != nil {
		s.fail(w, r, err, "contando conteúdos sem categoria")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"disponivel":    true,
		"andamento":     s.deps.Categorizador.Andamento(),
		"sem_categoria": map[string]any{"filmes": filmes, "series": series},
	})
}

func (s *Server) handleIniciarClassificacao(w http.ResponseWriter, r *http.Request) {
	if s.deps.Categorizador == nil {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "sem_tmdb",
			"a classificação por gênero precisa de uma chave do TMDB. Crie uma gratuitamente "+
				"em themoviedb.org, e coloque em TMDB_API_KEY no arquivo de ambiente do serviço.")
		return
	}
	if err := s.deps.Categorizador.Iniciar(r.URL.Query().Get("tipo")); err != nil {
		writeError(w, s.deps.Log, http.StatusConflict, "nao_pode_iniciar", err.Error())
		return
	}
	s.logEvent(r, "catalogo", "info", "classificação por gênero iniciada", actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusAccepted, map[string]any{"ok": true})
}

func (s *Server) handlePararClassificacao(w http.ResponseWriter, r *http.Request) {
	if s.deps.Categorizador != nil {
		s.deps.Categorizador.Parar()
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"ok": true})
}
