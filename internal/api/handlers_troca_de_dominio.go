package api

import (
	"errors"
	"net/http"
	"strconv"

	"vodmanager/internal/store"
)

type trocarDominioRequest struct {
	De   string `json:"de"`
	Para string `json:"para"`
	// Simular só conta o que mudaria. A tela sempre simula antes de confirmar.
	Simular bool `json:"simular"`
}

// handleTrocarDominio troca um domínio antigo pelo novo em todas as fontes e links.
func (s *Server) handleTrocarDominio(w http.ResponseWriter, r *http.Request) {
	var req trocarDominioRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}

	res, err := s.deps.Store.TrocarDominio(r.Context(), req.De, req.Para, req.Simular)
	if err != nil {
		if errors.Is(err, store.ErrDominioInvalido) {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", err.Error())
			return
		}
		s.fail(w, r, err, "trocando domínio")
		return
	}

	if !req.Simular {
		s.logEvent(r, "fontes", "info",
			"domínio trocado: "+res.De+" → "+res.Para+" ("+strconv.FormatInt(res.Links, 10)+
				" link(s), "+strconv.FormatInt(res.Fontes, 10)+" fonte(s))", actorOf(r), nil)
	}
	writeJSON(w, s.deps.Log, http.StatusOK, res)
}
