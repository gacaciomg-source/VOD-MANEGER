package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"vodmanager/internal/armazenamento"
	"vodmanager/internal/store"
)

// O acervo: o que esta operação guarda, onde, e o que pode ser apagado.
//
// A tela responde três perguntas, e elas são diferentes o bastante para não caberem numa
// só listagem:
//
//  1. quanto espaço está sendo usado, e onde — disco, e cada conta de nuvem;
//  2. o que é CACHE, que a limpeza apaga sozinha quando faltar espaço;
//  3. o que é ACERVO PRÓPRIO, que a limpeza nunca toca e por isso precisa de decisão
//     humana quando o espaço acabar.
//
// A terceira é a que justifica a tela existir. Um sistema que só apagasse cache resolveria
// o espaço sozinho e nunca precisaria perguntar nada. É o acervo insubstituível que
// transforma "liberar espaço" numa decisão que não pode ser automática.

func (s *Server) handleAcervoResumo(w http.ResponseWriter, r *http.Request) {
	resumo, err := s.deps.Store.ResumoDoAcervo(r.Context())
	if err != nil {
		s.fail(w, r, err, "resumindo o acervo")
		return
	}
	nuvens, err := s.deps.Store.ListarNuvens(r.Context())
	if err != nil {
		s.fail(w, r, err, "listando contas de nuvem")
		return
	}

	// Ignorar o erro e proposital: uma categoria que nao carrega nao pode impedir a tela
	// do acervo de abrir. O seletor fica vazio, e o envio continua possivel sem categoria.
	categorias, _ := s.deps.Store.ListCategories(r.Context())

	ligado, _ := s.deps.Store.GetSetting(r.Context(), store.SettingCacheLigado, "")
	destino, _ := s.deps.Store.GetSetting(r.Context(), store.SettingCacheBackend, store.BackendLocal)

	corpo := map[string]any{
		"resumo": resumo,
		"nuvens": nuvens,
		// As categorias vao junto porque o formulario de envio precisa delas: uma
		// segunda requisicao so para preencher um seletor faria a tela abrir em duas
		// etapas visiveis.
		"categorias":    categorias,
		"cache_ligado":  ligado == "true",
		"cache_destino": destino,
	}

	// O espaço do disco só é medido em Linux; em desenvolvimento a resposta é "não sei".
	// Dizer isso é melhor que devolver zero, que a tela leria como disco cheio.
	if s.deps.Armazenamento != nil {
		if local, ok := s.deps.Armazenamento.Obter(armazenamento.ChaveLocal); ok {
			if esp, err := local.Espaco(r.Context()); err == nil {
				dados := map[string]any{
					"total": esp.Total, "livre": esp.Livre, "usado": esp.Usado,
					"ilimitado": esp.Ilimitado,
				}
				// A PASTA junto com os numeros, e nao so os numeros.
				//
				// "12 GB livres" numa maquina com um disco de 200 GB parece defeito, e a
				// explicacao quase sempre e a mesma: o acervo esta numa particao pequena,
				// e o disco grande esta montado em outro lugar. Sem dizer QUAL pasta foi
				// medida, nao ha como perceber isso pela tela.
				if l, ok := local.(interface{ Raiz() string }); ok {
					dados["pasta"] = l.Raiz()
				}
				corpo["espaco_local"] = dados

				// Aperto = a limpeza esta (ou deveria estar) rodando. So nesse estado a
				// explicacao interessa; com disco sobrando ela seria ruido.
				if !esp.Ilimitado && esp.Total > 0 {
					dados["apertado"] = esp.Livre*100/esp.Total < 15
				}
			}
		}
	}
	// Por que a limpeza nao esta liberando espaco.
	//
	// Quatro causas possiveis, com acoes opostas: diminuir a carencia, desproteger algo,
	// apagar acervo proprio a mao, ou nada — porque de fato ainda ha espaco. De fora elas
	// sao indistinguiveis, e a unica saida seria ler o codigo.
	if s.deps.Store != nil {
		carencia := 24 * time.Hour
		if v, err := s.deps.Store.GetSetting(r.Context(), store.SettingCacheIdadeMinimaHoras, "24"); err == nil {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				carencia = time.Duration(n) * time.Hour
			}
		}
		if d, err := s.deps.Store.ExplicarLimpeza(r.Context(), carencia); err == nil {
			corpo["limpeza"] = d
			corpo["carencia_horas"] = int(carencia / time.Hour)
		}
	}

	writeJSON(w, s.deps.Log, http.StatusOK, corpo)
}

// handleListarArquivos lista o acervo, filtrado por origem e estado.
//
// O filtro por origem não é conveniência de tela: cache e acervo próprio respondem a
// perguntas diferentes, e misturá-los numa listagem só faria a mais importante das duas —
// "o que eu perco se apagar isto?" — se dissolver no meio de milhares de linhas de cache.
func (s *Server) handleListarArquivos(w http.ResponseWriter, r *http.Request) {
	origem := strings.TrimSpace(r.URL.Query().Get("origem"))
	if origem != "" && origem != store.OrigemFonte && origem != store.OrigemProprio {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_query",
			"origem precisa ser 'fonte' ou 'proprio'", "origem")
		return
	}
	estado := strings.TrimSpace(r.URL.Query().Get("estado"))

	limite := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limite = n
		}
	}

	arquivos, err := s.deps.Store.ListarArquivos(r.Context(), store.FiltroDeArquivos{
		Origem: origem, Estado: estado, Limite: limite,
	})
	if err != nil {
		s.fail(w, r, err, "listando o acervo")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"arquivos": arquivos})
}

type protegerRequest struct {
	Protegido *bool `json:"protegido"`
}

// handleProtegerArquivo liga ou desliga a imunidade à limpeza automática.
//
// Serve ao caso que aparece sozinho com o tempo: a fonte tira o filme do ar, e a cópia que
// era conveniência passa a ser a única que existe. Sem isto, a limpeza apagaria exatamente
// o arquivo que se tornou insubstituível.
func (s *Server) handleProtegerArquivo(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	var req protegerRequest
	if err := decodeJSON(w, r, &req); err != nil || req.Protegido == nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe protegido: true ou false", "protegido")
		return
	}
	if err := s.deps.Store.ProtegerArquivo(r.Context(), id, *req.Protegido); err != nil {
		s.fail(w, r, err, "protegendo arquivo do acervo")
		return
	}
	acao := "desprotegido"
	if *req.Protegido {
		acao = "protegido contra a limpeza automática"
	}
	s.logEvent(r, "acervo", "info", "arquivo "+acao, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"ok": true, "protegido": *req.Protegido})
}

// handleApagarArquivo remove uma cópia do acervo, a pedido.
//
// Marca para remoção em vez de apagar a linha. O arquivo existe em DOIS lugares — no banco
// e no armazenamento — e apagar a linha primeiro deixaria o arquivo órfão lá, ocupando um
// espaço que ninguém mais sabe que está ocupado. Quem apaga de verdade é o faxineiro, que
// só remove a linha depois de o backend confirmar.
func (s *Server) handleApagarArquivo(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	arquivo, err := s.deps.Store.ArquivoPorID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "buscando arquivo do acervo")
		return
	}
	if err := s.deps.Store.MarcarParaRemocao(r.Context(), id); err != nil {
		s.fail(w, r, err, "removendo arquivo do acervo")
		return
	}

	nivel := "info"
	if arquivo.Origem == store.OrigemProprio {
		// Acervo próprio apagado é perda definitiva: o registro do evento precisa refletir
		// isso, para quem for procurar o que aconteceu encontrar sem cavar.
		nivel = "warn"
	}
	s.logEvent(r, "acervo", nivel, "arquivo removido do acervo ("+arquivo.Origem+")", actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"ok": true})
}
