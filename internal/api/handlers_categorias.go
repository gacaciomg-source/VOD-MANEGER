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

	// E vale para o NOME, não só para esta fonte. Quem decide "Lançamentos 2024 vai para
	// Lançamentos" não está falando de uma fonte específica — e repetir a mesma decisão
	// fonte a fonte, com uma centena de categorias, é o que torna a tela impraticável.
	outras, err := s.deps.Store.RegistrarApelidoDePendencia(r.Context(), id, destino)
	if err != nil {
		s.deps.Log.Warn("vínculo gravado mas o apelido não foi guardado", "erro", err)
	}

	s.logEvent(r, "catalog", "info",
		"categoria da fonte vinculada: "+alvo.Declarado, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"ok": true, "categoria_id": destino, "conteudos_movidos": movidos,
		"outras_fontes": outras,
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

// Apelidos de categoria: a lista do que foi unido, e o caminho de volta.
//
// Unir apaga uma pasta. Sem esta lista, o que foi unido some sem deixar rastro — não dá
// para conferir o que se decidiu nem para mudar de ideia, e uma ação irreversível que se
// toma às dezenas é uma armadilha.

func (s *Server) handleListApelidos(w http.ResponseWriter, r *http.Request) {
	apelidos, err := s.deps.Store.ListarApelidos(r.Context())
	if err != nil {
		s.fail(w, r, err, "listando apelidos de categoria")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"apelidos": apelidos})
}

// handleRemoverApelido solta o nome: ele volta a pedir decisão na próxima sincronização.
func (s *Server) handleRemoverApelido(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	soltos, err := s.deps.Store.RemoverApelido(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "removendo apelido de categoria")
		return
	}
	s.logEvent(r, "catalog", "info", "apelido de categoria removido", actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"ok": true, "pendencias": soltos})
}

// handleReativarApelido devolve o nome à condição de pasta própria e principal.
func (s *Server) handleReativarApelido(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	categoria, movidos, err := s.deps.Store.ReativarApelido(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "reativando apelido de categoria")
		return
	}
	s.logEvent(r, "catalog", "info", fmt.Sprintf(
		"categoria reativada como principal (%d conteúdo(s) de volta)", movidos), actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"ok": true, "categoria_id": categoria, "conteudos_movidos": movidos,
	})
}
