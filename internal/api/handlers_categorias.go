package api

import (
	"fmt"
	"net/http"
	"strings"

	"vodmanager/internal/ingest"
	"vodmanager/internal/store"
)

// Categorias PRINCIPAIS e pendências de vínculo.
//
// A ideia central: a decisão de "esta categoria da fonte pertence àquela pasta" é tomada
// UMA vez e vale para sempre. A sincronização não cria pasta nenhuma — ela só usa o que
// foi marcado como principal e o que foi vinculado.
//
// Por isso a tela de pendências esvazia: o que já foi decidido não volta a aparecer.

// handleListPendencias devolve as categorias de fonte esperando decisão.
func (s *Server) handleListPendencias(w http.ResponseWriter, r *http.Request) {
	pendentes, err := s.deps.Store.ListarPendencias(r.Context())
	if err != nil {
		s.fail(w, r, err, "listando pendências de categoria")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"pendencias": pendentes})
}

type marcarPrincipalRequest struct {
	Principal *bool `json:"principal"`
}

// handleMarcarPrincipal liga ou desliga a marcação de uma categoria.
func (s *Server) handleMarcarPrincipal(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	var req marcarPrincipalRequest
	if err := decodeJSON(w, r, &req); err != nil || req.Principal == nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe principal: true ou false", "principal")
		return
	}
	if err := s.deps.Store.MarcarPrincipal(r.Context(), id, *req.Principal); err != nil {
		s.fail(w, r, err, "marcando categoria principal")
		return
	}

	acao := "desmarcada como principal"
	if *req.Principal {
		acao = "marcada como principal"
	}
	s.logEvent(r, "catalog", "info", "categoria "+acao, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"ok": true, "principal": *req.Principal})
}

type resolverPendenciaRequest struct {
	// CategoriaID vincula a uma principal existente.
	CategoriaID *int64 `json:"categoria_id"`
	// Promover cria uma principal com o próprio nome desta categoria de fonte. É o caso
	// de "não tem onde encaixar": melhor uma pasta nova que um vínculo errado.
	Promover bool `json:"promover"`
	// Nome permite renomear ao promover — a fonte costuma trazer nomes como
	// "Filmes | Lancamentos", e a pasta final merece um nome escolhido.
	Nome string `json:"nome"`
}

// handleResolverPendencia decide o destino de uma categoria de fonte.
func (s *Server) handleResolverPendencia(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	var req resolverPendenciaRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	if req.CategoriaID == nil && !req.Promover {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"escolha uma categoria existente ou promova esta a principal")
		return
	}

	pendentes, err := s.deps.Store.ListarPendencias(r.Context())
	if err != nil {
		s.fail(w, r, err, "buscando pendência")
		return
	}
	var alvo *store.CategoriaPendente
	for i := range pendentes {
		if pendentes[i].ID == id {
			alvo = &pendentes[i]
			break
		}
	}
	if alvo == nil {
		writeError(w, s.deps.Log, http.StatusNotFound, "not_found",
			"esta pendência já foi resolvida")
		return
	}

	destino := int64(0)
	if req.Promover {
		nome := strings.TrimSpace(req.Nome)
		if nome == "" {
			nome = alvo.Declarado
		}
		destino, err = s.deps.Store.CriarPrincipal(r.Context(), nome,
			ingest.NormalizeName(nome), alvo.ContentType)
		if err != nil {
			s.fail(w, r, err, "criando categoria principal")
			return
		}
	} else {
		destino = *req.CategoriaID
	}

	if _, err := s.deps.Store.MapSourceCategory(r.Context(), id, destino); err != nil {
		s.fail(w, r, err, "vinculando categoria")
		return
	}

	// A decisão precisa valer para o que JÁ está no catálogo, não só para a próxima
	// sincronização: ver a escolha não surtir efeito é a maneira mais rápida de perder a
	// confiança na tela.
	movidos, err := s.deps.Store.AplicarVinculoRetroativo(r.Context(), id, destino)
	if err != nil {
		s.deps.Log.Warn("vínculo gravado mas o conteúdo não foi movido", "erro", err)
	}

	s.logEvent(r, "catalog", "info",
		"categoria da fonte vinculada: "+alvo.Declarado, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"ok": true, "categoria_id": destino, "conteudos_movidos": movidos,
	})
}

type absorverRequest struct {
	// CategoriaID é o destino: a principal que vai receber o conteúdo.
	CategoriaID int64 `json:"categoria_id"`
}

// handleAbsorverCategoria une uma categoria a uma principal.
//
// Apaga a categoria de origem, e um identificador apagado pode estar em uso do outro lado
// (um cliente que guardou a pasta). Por isso exige papel de escrita e fica registrado.
func (s *Server) handleAbsorverCategoria(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	var req absorverRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if req.CategoriaID <= 0 {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe categoria_id: a principal que recebe o conteúdo", "categoria_id")
		return
	}

	movidos, err := s.deps.Store.AbsorverCategoria(r.Context(), id, req.CategoriaID)
	if err != nil {
		s.fail(w, r, err, "unindo categorias")
		return
	}

	s.logEvent(r, "catalog", "info", fmt.Sprintf(
		"categoria %d unida à principal %d (%d conteúdo(s) movido(s))", id, req.CategoriaID, movidos),
		actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"ok": true, "conteudos_movidos": movidos,
	})
}
