package api

import (
	"context"
	"net/http"
	"time"

	"vodmanager/internal/auth"
)

// handleHealthz é liveness: responde enquanto o processo estiver de pé.
// Não toca no banco de propósito — um Postgres lento não deve fazer o orquestrador
// reiniciar um processo saudável.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"status":  "ok",
		"node_id": s.deps.NodeID,
		"version": s.deps.Version,
	})
}

// handleReadyz é readiness: só responde ok se o banco estiver acessível.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if err := s.deps.Store.Pool().Ping(ctx); err != nil {
		s.deps.Log.Warn("readiness falhou", "erro", err)
		writeJSON(w, s.deps.Log, http.StatusServiceUnavailable, map[string]any{
			"status":   "degraded",
			"database": "indisponível",
			"node_id":  s.deps.NodeID,
		})
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"status":   "ok",
		"database": "ok",
		"node_id":  s.deps.NodeID,
		"version":  s.deps.Version,
	})
}

// actorOf identifica quem fez a requisição, para o log de eventos.
func actorOf(r *http.Request) string {
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		return p.User.Username
	}
	return ""
}
