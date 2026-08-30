package api

import (
	"net/http"
	"strconv"

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

// handleUnirTodasDuplicatas aplica de uma vez todas as uniões sugeridas.
//
// # Por que isto precisa existir
//
// A tela decide um par por vez, e isso funciona com dez sugestões. Com um catálogo de quatro
// fontes que se sobrepõem, são centenas — o mesmo filme aparece seis vezes porque cada fonte
// o declara numa pasta diferente. Decidir uma por uma é uma tarde inteira, e ninguém faz: a
// tela vira um número que só cresce, e o catálogo continua com o mesmo filme repetido.
//
// # Qual lado sobrevive
//
// O que tem MAIS variantes. Variante é fonte capaz de servir aquele conteúdo, então o lado
// com mais é o que continua tocando se alguma fonte cair. Empatando, vence o que NÃO carrega
// marcação de estado no título — "Fulano (Lançamento)" é o mesmo filme com um rótulo que
// envelhece mal.
//
// Nenhuma variante é perdida: unir move as do lado que sai para o que fica. O que desaparece
// é a linha duplicada do catálogo, não o acesso ao filme.
func (s *Server) handleUnirTodasDuplicatas(w http.ResponseWriter, r *http.Request) {
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
	for i := range chaves {
		chaves[i].Chave = ingest.ChaveDeDuplicata(detalhes[i].Titulo)
		detalhes[i].TemMarcacao = ingest.TemMarcacaoDeEstado(detalhes[i].Titulo)
	}

	// Sem limite aqui, ao contrário da tela: ela mostra um punhado porque ninguém lê mil
	// linhas, mas quem manda unir tudo quer tudo — e parar na centésima deixaria o trabalho
	// pela metade sem dizer.
	sugestoes := store.MontarSugestoes(chaves, detalhes, ignorados, 0)

	// Um conteúdo já absorvido não pode ser origem nem destino de outra união: ele não
	// existe mais. Sem isto, o mesmo filme repetido seis vezes produziria uniões em cadeia
	// apontando para linhas apagadas.
	sumiram := map[int64]bool{}
	var unidos, movidas int64
	for _, s2 := range sugestoes {
		if sumiram[s2.A.ID] || sumiram[s2.B.ID] {
			continue
		}
		fica, sai := s2.A, s2.B
		if melhorParaManter(s2.B, s2.A) {
			fica, sai = s2.B, s2.A
		}
		n, err := s.deps.Store.UnirConteudos(r.Context(), fica.ID, sai.ID)
		if err != nil {
			// Uma união que falha não pode abortar as outras: são independentes, e parar
			// aqui deixaria metade do trabalho feito sem nada indicando onde parou.
			s.deps.Log.Warn("falha ao unir duplicata",
				"fica", fica.ID, "sai", sai.ID, "erro", err)
			continue
		}
		sumiram[sai.ID] = true
		unidos++
		movidas += n
	}

	s.logEvent(r, "catalogo", "info",
		"duplicatas unidas em lote: "+strconv.FormatInt(unidos, 10), actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"unidos": unidos, "variantes_movidas": movidas,
	})
}

// melhorParaManter diz se `a` deve sobreviver no lugar de `b`.
//
// Mais variantes ganha: variante é fonte capaz de servir, e o lado com mais é o que continua
// tocando se uma fonte cair. Empatando, ganha o que não carrega marcação de estado — um
// título com "(Lançamento)" descreve o momento em que a fonte o cadastrou, não o filme.
func melhorParaManter(a, b store.ConteudoDuplicado) bool {
	if a.Variantes != b.Variantes {
		return a.Variantes > b.Variantes
	}
	if a.TemMarcacao != b.TemMarcacao {
		return !a.TemMarcacao
	}
	// Persistindo o empate, o mais antigo: ele é o que os links já entregues apontam.
	return a.ID < b.ID
}
