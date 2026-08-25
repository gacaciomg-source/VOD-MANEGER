package api

import (
	"net/http"

	"vodmanager/internal/sysinfo"
)

// handleSystem devolve o consumo de recursos da máquina, já interpretado.
//
// Existe para responder "a VPS que eu tenho dá conta?" com medida, e não com palpite. Os
// números crus vão junto do veredito porque quem entende de servidor quer conferir a
// conclusão, e quem não entende só quer a conclusão.
func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sistema == nil {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "sem_medicao",
			"este processo não coleta métricas de sistema")
		return
	}
	amostra := s.deps.Sistema.Atual()

	ctx := sysinfo.Contexto{}
	// O tamanho do banco separa "meu disco está cheio" de "o VOD Manager encheu meu
	// disco" — são conclusões diferentes, com ações diferentes.
	if tam, err := s.deps.Store.TamanhoBanco(r.Context()); err == nil {
		ctx.TamanhoBanco = tam
	}
	if s.deps.StreamProxy != nil {
		for _, n := range s.deps.StreamProxy.Conexoes().Snapshot() {
			ctx.StreamsAtivos += n
		}
	}
	// CPU alta durante uma sincronização é esperado e não significa VPS pequena; o
	// veredito precisa saber disso para não recomendar troca de plano à toa.
	if rodando, err := s.deps.Store.ListRunningSyncRuns(r.Context()); err == nil {
		ctx.SincronizandoAgora = len(rodando) > 0
	}

	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"amostra":  amostra,
		"veredito": sysinfo.Avaliar(amostra, ctx),
		"contexto": map[string]any{
			"streams_ativos":      ctx.StreamsAtivos,
			"tamanho_banco":       ctx.TamanhoBanco,
			"sincronizando_agora": ctx.SincronizandoAgora,
		},
		"versao": s.deps.Version,
		"node":   s.deps.NodeID,
	})
}

// handleFalhas lista as reproduções que não entregaram vídeo.
//
// A pergunta que isto responde não é "quantas falhas houve", mas "o que não abriu e de
// qual fonte tentou puxar" — sem a fonte, a falha não sugere ação nenhuma.
func (s *Server) handleFalhas(w http.ResponseWriter, r *http.Request) {
	falhas, err := s.deps.Store.ListFalhasDeReproducao(r.Context(), 100)
	if err != nil {
		s.fail(w, r, err, "listando falhas de reprodução")
		return
	}
	// O resumo por causa vai junto: sem ele a tela responde "o que falhou" e nao "por que",
	// que e a pergunta que decide o que fazer.
	resumo, err := s.deps.Store.ResumoDeFalhas(r.Context())
	if err != nil {
		s.fail(w, r, err, "resumindo falhas de reproducao")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"falhas": falhas, "resumo": resumo})
}

// handleTrafego resume o volume entregue por período.
func (s *Server) handleTrafego(w http.ResponseWriter, r *http.Request) {
	periodos, err := s.deps.Store.Trafego(r.Context())
	if err != nil {
		s.fail(w, r, err, "resumindo tráfego")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"periodos": periodos,
		// Enquanto a entrega é direta da fonte, cada byte entregue foi um byte recebido
		// da fonte. O painel usa isto para explicar que o tráfego real é o dobro.
		"entrega_direta": true,
	})
}
