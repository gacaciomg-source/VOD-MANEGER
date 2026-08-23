package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"vodmanager/internal/cryptobox"
	"vodmanager/internal/store"
)

type createSourceRequest struct {
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	Kind                   string   `json:"kind"`
	BaseURL                string   `json:"base_url"`
	Priority               *int     `json:"priority"`
	Enabled                *bool    `json:"enabled"`
	SyncIntervalMinutes    *int     `json:"sync_interval_minutes"`
	MaxConnections         *int     `json:"max_connections"`
	MaxConcurrentDownloads *int     `json:"max_concurrent_downloads"`
	MaxBandwidthBPS        *int64   `json:"max_bandwidth_bps"`
	AllowedCategories      []string `json:"allowed_categories"`
	IgnoredCategories      []string `json:"ignored_categories"`
	CacheHabilitado        *bool    `json:"cache_habilitado"`
}

type updateSourceRequest struct {
	Name                   *string   `json:"name"`
	Description            *string   `json:"description"`
	BaseURL                *string   `json:"base_url"`
	Priority               *int      `json:"priority"`
	Enabled                *bool     `json:"enabled"`
	SyncIntervalMinutes    *int      `json:"sync_interval_minutes"`
	MaxConnections         *int      `json:"max_connections"`
	MaxConcurrentDownloads *int      `json:"max_concurrent_downloads"`
	AllowedCategories      *[]string `json:"allowed_categories"`
	IgnoredCategories      *[]string `json:"ignored_categories"`
	CacheHabilitado        *bool     `json:"cache_habilitado"`
	// Campo ausente = não alterar; `null` explícito = remover o limite; número = definir.
	// Por isso ele fica como RawMessage: *int64 não distingue "ausente" de "null".
	MaxBandwidthBPS json.RawMessage `json:"max_bandwidth_bps"`
}

type reorderRequest struct {
	IDs []int64 `json:"ids"`
}

type credentialRequest struct {
	Username string          `json:"username"`
	Password string          `json:"password"`
	Extra    json.RawMessage `json:"extra"`
}

// storedSecret é o que vai cifrado no banco. Nunca sai deste pacote em claro.
type storedSecret struct {
	Password string          `json:"password"`
	Extra    json.RawMessage `json:"extra,omitempty"`
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.deps.Store.ListSources(r.Context())
	if err != nil {
		s.fail(w, r, err, "listando fontes")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"sources": sources})
}

func (s *Server) handleGetSource(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id de fonte inválido")
		return
	}
	src, err := s.deps.Store.GetSource(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "buscando fonte")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, src)
}

func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	var req createSourceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}

	if problems := validateNewSource(req); len(problems) > 0 {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"campos inválidos: "+strings.Join(problems, "; "), problems...)
		return
	}

	src, err := s.deps.Store.CreateSource(r.Context(), store.NewSource{
		Name:                   strings.TrimSpace(req.Name),
		Description:            req.Description,
		Kind:                   req.Kind,
		BaseURL:                strings.TrimSpace(req.BaseURL),
		Priority:               req.Priority,
		Enabled:                req.Enabled,
		SyncIntervalMinutes:    req.SyncIntervalMinutes,
		MaxConnections:         req.MaxConnections,
		MaxConcurrentDownloads: req.MaxConcurrentDownloads,
		MaxBandwidthBPS:        req.MaxBandwidthBPS,
		AllowedCategories:      req.AllowedCategories,
		IgnoredCategories:      req.IgnoredCategories,
		CacheHabilitado:        req.CacheHabilitado,
	})
	if err != nil {
		s.fail(w, r, err, "criando fonte")
		return
	}
	s.logEvent(r, "source", "info", "fonte criada: "+src.Name, actorOf(r), &src.ID)
	writeJSON(w, s.deps.Log, http.StatusCreated, src)
}

func (s *Server) handleUpdateSource(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id de fonte inválido")
		return
	}
	var req updateSourceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	// max_bandwidth_bps: ausente = não alterar, null = sem limite, número = definir.
	var bandwidth *int64
	bandwidthSet := len(req.MaxBandwidthBPS) > 0
	if bandwidthSet {
		if err := json.Unmarshal(req.MaxBandwidthBPS, &bandwidth); err != nil {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
				"max_bandwidth_bps precisa ser um número ou null", "max_bandwidth_bps")
			return
		}
	}

	if problems := validateSourcePatch(req, bandwidth); len(problems) > 0 {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"campos inválidos: "+strings.Join(problems, "; "), problems...)
		return
	}

	patch := store.SourcePatch{
		Name:                   trimPtr(req.Name),
		Description:            req.Description,
		BaseURL:                trimPtr(req.BaseURL),
		Priority:               req.Priority,
		Enabled:                req.Enabled,
		SyncIntervalMinutes:    req.SyncIntervalMinutes,
		MaxConnections:         req.MaxConnections,
		MaxConcurrentDownloads: req.MaxConcurrentDownloads,
		AllowedCategories:      req.AllowedCategories,
		IgnoredCategories:      req.IgnoredCategories,
		CacheHabilitado:        req.CacheHabilitado,
	}
	if bandwidthSet {
		patch.MaxBandwidthBPS = &bandwidth
	}

	src, err := s.deps.Store.UpdateSource(r.Context(), id, patch)
	if err != nil {
		s.fail(w, r, err, "atualizando fonte")
		return
	}
	s.logEvent(r, "source", "info", "fonte atualizada: "+src.Name, actorOf(r), &src.ID)
	writeJSON(w, s.deps.Log, http.StatusOK, src)
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id de fonte inválido")
		return
	}
	// Cancela a sincronização em andamento ANTES de remover a fonte. Sem isso, a execução
	// continua tentando gravar variantes de uma fonte que não existe mais, falha em cada
	// item e ocupa a vaga de sincronização por minutos.
	if s.deps.Sync != nil {
		if s.deps.Sync.Orchestrator().Cancel(id) {
			s.deps.Log.Info("sincronização cancelada por exclusão da fonte", "source_id", id)
		}
	}

	if err := s.deps.Store.DeleteSource(r.Context(), id); err != nil {
		s.fail(w, r, err, "removendo fonte")
		return
	}
	s.logEvent(r, "source", "warn", "fonte removida", actorOf(r), &id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReorderSources(w http.ResponseWriter, r *http.Request) {
	var req reorderRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "informe a lista completa de ids", "ids")
		return
	}
	seen := make(map[int64]bool, len(req.IDs))
	for _, id := range req.IDs {
		if seen[id] {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "há ids repetidos na lista", "ids")
			return
		}
		seen[id] = true
	}

	if err := s.deps.Store.ReorderSources(r.Context(), req.IDs); err != nil {
		if errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrNotFound) {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
				"a lista precisa conter exatamente todos os ids de fontes existentes", "ids")
			return
		}
		s.fail(w, r, err, "reordenando fontes")
		return
	}
	sources, err := s.deps.Store.ListSources(r.Context())
	if err != nil {
		s.fail(w, r, err, "listando fontes")
		return
	}
	s.logEvent(r, "source", "info", "prioridade das fontes reordenada", actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"sources": sources})
}

// handleSetSourceCredential cifra e grava a credencial da fonte.
//
// A resposta NUNCA devolve a credencial — só a confirmação de que ela existe.
func (s *Server) handleSetSourceCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id de fonte inválido")
		return
	}
	var req credentialRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	if req.Password == "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "senha da fonte é obrigatória", "password")
		return
	}
	if len(req.Extra) > 0 && !json.Valid(req.Extra) {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "extra precisa ser JSON válido", "extra")
		return
	}

	if _, err := s.deps.Store.GetSource(r.Context(), id); err != nil {
		s.fail(w, r, err, "buscando fonte")
		return
	}

	plaintext, err := json.Marshal(storedSecret{Password: req.Password, Extra: req.Extra})
	if err != nil {
		s.fail(w, r, err, "serializando credencial")
		return
	}
	sealed, err := s.deps.Crypto.Seal(plaintext, cryptobox.SourceCredentialAAD(id))
	if err != nil {
		s.fail(w, r, err, "cifrando credencial")
		return
	}
	if _, err := s.deps.Store.SetSourceCredential(r.Context(), id, req.Username, sealed, 1); err != nil {
		s.fail(w, r, err, "gravando credencial")
		return
	}

	s.logEvent(r, "source", "info", "credencial da fonte atualizada", actorOf(r), &id)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"source_id":       id,
		"has_credentials": true,
		"username":        req.Username,
	})
}

func (s *Server) handleDeleteSourceCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id de fonte inválido")
		return
	}
	if err := s.deps.Store.DeleteSourceCredential(r.Context(), id); err != nil {
		s.fail(w, r, err, "removendo credencial")
		return
	}
	s.logEvent(r, "source", "warn", "credencial da fonte removida", actorOf(r), &id)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------

func validateNewSource(req createSourceRequest) []string {
	var problems []string
	if strings.TrimSpace(req.Name) == "" {
		problems = append(problems, "name")
	}
	if !store.ValidSourceKind(req.Kind) {
		problems = append(problems, "kind")
	}
	if !validBaseURL(req.BaseURL) {
		problems = append(problems, "base_url")
	}
	problems = append(problems, validateLimits(
		req.Priority, req.SyncIntervalMinutes, req.MaxConnections,
		req.MaxConcurrentDownloads, req.MaxBandwidthBPS)...)
	return problems
}

func validateSourcePatch(req updateSourceRequest, bandwidth *int64) []string {
	var problems []string
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		problems = append(problems, "name")
	}
	if req.BaseURL != nil && !validBaseURL(*req.BaseURL) {
		problems = append(problems, "base_url")
	}
	problems = append(problems, validateLimits(
		req.Priority, req.SyncIntervalMinutes, req.MaxConnections,
		req.MaxConcurrentDownloads, bandwidth)...)
	return problems
}

func validateLimits(priority, syncInterval, maxConns, maxDownloads *int, bandwidth *int64) []string {
	var problems []string
	if priority != nil && *priority <= 0 {
		problems = append(problems, "priority")
	}
	if syncInterval != nil && *syncInterval <= 0 {
		problems = append(problems, "sync_interval_minutes")
	}
	if maxConns != nil && *maxConns <= 0 {
		problems = append(problems, "max_connections")
	}
	if maxDownloads != nil && *maxDownloads <= 0 {
		problems = append(problems, "max_concurrent_downloads")
	}
	if maxConns != nil && maxDownloads != nil && *maxDownloads > *maxConns {
		problems = append(problems, "max_concurrent_downloads")
	}
	if bandwidth != nil && *bandwidth <= 0 {
		problems = append(problems, "max_bandwidth_bps")
	}
	return problems
}

// validBaseURL exige http/https absoluto com host. Uma URL relativa ou com esquema
// exótico não tem como ser sincronizada depois.
func validBaseURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func trimPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	return &v
}
