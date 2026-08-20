package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"vodmanager/internal/store"
)

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := store.EventFilter{
		Category: q.Get("category"),
		Level:    q.Get("level"),
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_query", "limit precisa ser um inteiro positivo", "limit")
			return
		}
		filter.Limit = n
	}
	if v := q.Get("source_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_query", "source_id inválido", "source_id")
			return
		}
		filter.SourceID = &id
	}
	if v := q.Get("since"); v != "" {
		ts, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_query", "since precisa estar em RFC3339", "since")
			return
		}
		filter.Since = &ts
	}

	events, err := s.deps.Store.ListEvents(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err, "listando eventos")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"events": events})
}

// logEvent grava um evento de negócio. Falha de gravação nunca derruba a requisição:
// o log estruturado registra o problema e a operação principal segue.
func (s *Server) logEvent(r *http.Request, category, level, message, actor string, sourceID *int64) {
	err := s.deps.Store.InsertEvent(r.Context(), store.NewEvent{
		NodeID:   s.deps.NodeID,
		Level:    level,
		Category: category,
		Message:  message,
		Actor:    actor,
		SourceID: sourceID,
	})
	if err != nil {
		s.deps.Log.Error("não foi possível gravar o evento", "categoria", category, "erro", err)
	}
}

// fail traduz um erro da camada store em resposta HTTP, registrando os 5xx.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error, op string) {
	// Requisição abandonada pelo navegador não é erro do servidor.
	//
	// O painel se atualiza sozinho a cada poucos segundos e cancela a consulta anterior
	// ao trocar de tela. Cada um desses cancelamentos virava um ERROR de "erro interno" no
	// log — ruído que afoga os problemas de verdade, e assusta quem lê o log procurando
	// causa para outra coisa.
	if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
		s.deps.Log.Debug("requisição cancelada pelo cliente",
			"operacao", op, "rota", r.URL.Path)
		return
	}

	status, code := storeStatus(err)
	if status >= 500 {
		s.deps.Log.Error("erro interno", "operacao", op, "rota", r.URL.Path, "erro", err)
		writeError(w, s.deps.Log, status, code, "erro interno ao "+op)
		return
	}
	writeError(w, s.deps.Log, status, code, err.Error())
}
