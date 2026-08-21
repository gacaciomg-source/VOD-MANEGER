package api

import (
	"net/http"

	"vodmanager/internal/auth"
	"vodmanager/internal/ingest"
	"vodmanager/internal/store"
)

// Sugestões de conteúdo duplicado.
//
// O sistema aponta, o administrador decide. Nunca agrupa sozinho: uma regra que remove
// "Lançamento" do título acerta quase sempre e erra em silêncio no dia em que aparecer um
// filme que se chama assim de verdade — e um agrupamento errado só é descoberto quando
// alguém abre e vê outro filme.

// limiteDeSugestoes evita devolver milhares de pares numa tela só.
const limiteDeSugestoes = 200

func (s *Server) handleListDuplicatas(w http.ResponseWriter, r *http.Request) {
	chaves, detalhes, err := s.deps.Store.ConteudosParaComparacao(r.Context())
	if err != nil {
		s.fail(w, r, err, "carregando conteúdos para comparação")
		return
	}
	ignorados, err := s.deps.Store.ParesIgnorados(r.Context())
	if err != nil {
		s.fail(w, r, err, "carregando pares ignorados")
		return
	}

	// A chave sai das regras de limpeza de título que já existem e estão testadas.
	// Calculá-la aqui, e não no SQL, evita duas verdades sobre o que é o mesmo filme.
	for i := range chaves {
		chaves[i].Chave = ingest.ChaveDeDuplicata(detalhes[i].Titulo)
		detalhes[i].TemMarcacao = ingest.TemMarcacaoDeEstado(detalhes[i].Titulo)
	}

	sugestoes := store.MontarSugestoes(chaves, detalhes, ignorados, limiteDeSugestoes)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"sugestoes": sugestoes,
		"total":     len(sugestoes),
		"limitado":  len(sugestoes) >= limiteDeSugestoes,
	})
}

type decisaoDuplicataRequest struct {
	A int64 `json:"a"`
	B int64 `json:"b"`
	// Manter é o id do conteúdo que sobrevive à união. Zero significa "são diferentes,
	// não me pergunte de novo".
	Manter int64 `json:"manter"`
}

// handleDecidirDuplicata aplica a decisão do administrador sobre um par.
func (s *Server) handleDecidirDuplicata(w http.ResponseWriter, r *http.Request) {
	var req decisaoDuplicataRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	if req.A <= 0 || req.B <= 0 || req.A == req.B {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe dois conteúdos diferentes")
		return
	}

	var quem int64
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		quem = p.User.ID
	}

	// Decisão negativa: são conteúdos diferentes.
	if req.Manter == 0 {
		if err := s.deps.Store.IgnorarPar(r.Context(), req.A, req.B, quem); err != nil {
			s.fail(w, r, err, "registrando a decisão")
			return
		}
		s.logEvent(r, "catalog", "info", "par marcado como conteúdos diferentes", actorOf(r), nil)
		writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"ok": true, "unidos": false})
		return
	}

	if req.Manter != req.A && req.Manter != req.B {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"o conteúdo a manter precisa ser um dos dois do par", "manter")
		return
	}
	origem := req.A
	if req.Manter == req.A {
		origem = req.B
	}

	movidas, err := s.deps.Store.UnirConteudos(r.Context(), req.Manter, origem)
	if err != nil {
		s.fail(w, r, err, "unindo conteúdos")
		return
	}

	s.logEvent(r, "catalog", "warn", "conteúdos unidos pelo painel", actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"ok": true, "unidos": true, "variantes_movidas": movidas,
		"aviso": "O identificador do conteúdo removido deixou de existir. " +
			"Quem já tinha importado aquele item precisa reimportar para vê-lo de novo.",
	})
}
