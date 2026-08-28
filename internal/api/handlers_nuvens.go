package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"vodmanager/internal/armazenamento"
	"vodmanager/internal/cryptobox"
	"vodmanager/internal/store"
)

// Contas de nuvem, pelo painel.
//
// # O que NUNCA sai daqui
//
// As credenciais. Elas entram uma vez, são cifradas com a chave mestra e nunca voltam em
// resposta nenhuma — nem para administrador, nem mascaradas.
//
// A regra é a mesma das credenciais das fontes, e vale pelo mesmo motivo: um token de nuvem
// lê, escreve e apaga tudo o que houver na conta. Devolvê-lo "só para conferir" o colocaria
// no histórico do navegador, no cache do proxy e no console de quem estivesse com a aba
// aberta. Quem esqueceu a credencial cadastra outra; não há por que existir caminho de
// leitura.

// credenciaisGDrive é o que o Google precisa para falar em nome da conta.
//
// O refresh token é o que sobrevive: o token de acesso vale uma hora, e é renovado a partir
// dele. Sem o refresh token, a conta pararia de funcionar sozinha uma hora depois do
// cadastro — e o sintoma seria "o acervo sumiu", não "o token venceu".
type credenciaisGDrive struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

func (s *Server) handleListarNuvens(w http.ResponseWriter, r *http.Request) {
	nuvens, err := s.deps.Store.ListarNuvens(r.Context())
	if err != nil {
		s.fail(w, r, err, "listando contas de nuvem")
		return
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"nuvens": nuvens,
		// Os provedores que o código sabe usar. A tela oferece o que existe, e não o que
		// existiria — um seletor com opção que não funciona é uma promessa quebrada.
		"provedores": []string{store.ProvedorGDrive},
	})
}

type criarNuvemRequest struct {
	Nome      string `json:"nome"`
	Provedor  string `json:"provedor"`
	PastaRaiz string `json:"pasta_raiz"`
	Ordem     int    `json:"ordem"`

	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

func (s *Server) handleCriarNuvem(w http.ResponseWriter, r *http.Request) {
	var req criarNuvemRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}

	nome := strings.TrimSpace(req.Nome)
	if nome == "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"dê um nome à conta — é como você vai distingui-la das outras", "nome")
		return
	}
	if req.Provedor == "" {
		req.Provedor = store.ProvedorGDrive
	}
	if req.Provedor != store.ProvedorGDrive {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"provedor não suportado", "provedor")
		return
	}
	for campo, valor := range map[string]string{
		"client_id":     req.ClientID,
		"client_secret": req.ClientSecret,
		"refresh_token": req.RefreshToken,
	} {
		if strings.TrimSpace(valor) == "" {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
				"informe "+campo, campo)
			return
		}
	}

	bruto, err := json.Marshal(credenciaisGDrive{
		ClientID:     strings.TrimSpace(req.ClientID),
		ClientSecret: strings.TrimSpace(req.ClientSecret),
		RefreshToken: strings.TrimSpace(req.RefreshToken),
	})
	if err != nil {
		s.fail(w, r, err, "preparando credenciais da conta")
		return
	}

	// AAD pelo nome da conta: mover o blob cifrado de uma linha para outra no banco não
	// produz credencial válida, produz falha de decifragem.
	cifrado, err := s.deps.Crypto.Seal(bruto, cryptobox.NuvemAAD(nome))
	if err != nil {
		s.fail(w, r, err, "cifrando credenciais da conta")
		return
	}

	nuvem, err := s.deps.Store.CriarNuvem(r.Context(), store.NovaNuvem{
		Nome:        nome,
		Provedor:    req.Provedor,
		PastaRaiz:   strings.TrimSpace(req.PastaRaiz),
		Ordem:       req.Ordem,
		Credenciais: cifrado,
	})
	if err != nil {
		s.fail(w, r, err, "cadastrando conta de nuvem")
		return
	}
	s.logEvent(r, "acervo", "info", "conta de nuvem cadastrada: "+nome, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusCreated, nuvem)
}

type ajustarNuvemRequest struct {
	Nome           *string `json:"nome"`
	PastaRaiz      *string `json:"pasta_raiz"`
	Ativa          *bool   `json:"ativa"`
	SomenteLeitura *bool   `json:"somente_leitura"`
	Ordem          *int    `json:"ordem"`
}

func (s *Server) handleAjustarNuvem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	var req ajustarNuvemRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}

	// Renomear muda o AAD das credenciais, que foram cifradas com o nome antigo — e a
	// decifragem passaria a falhar, deixando a conta inútil sem nada explicar.
	//
	// Recusar é melhor que reencriptar em silêncio: o nome é só um rótulo, e trocá-lo não
	// vale o risco de um caminho que mexe em credencial. Quem quer outro nome cadastra
	// outra conta.
	if req.Nome != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"o nome da conta não pode ser alterado: ele faz parte da proteção das "+
				"credenciais. Cadastre outra conta se precisar de outro nome.", "nome")
		return
	}

	nuvem, err := s.deps.Store.AtualizarNuvem(r.Context(), id, store.AjusteDeNuvem{
		PastaRaiz:      req.PastaRaiz,
		Ativa:          req.Ativa,
		SomenteLeitura: req.SomenteLeitura,
		Ordem:          req.Ordem,
	})
	if err != nil {
		s.fail(w, r, err, "atualizando conta de nuvem")
		return
	}
	s.logEvent(r, "acervo", "info", "conta de nuvem ajustada: "+nuvem.Nome, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, nuvem)
}

// handleRemoverNuvem apaga a conta, se ela estiver vazia.
//
// Recusa enquanto houver arquivo guardado nela. Sem isso, sobrariam registros apontando
// para lugar nenhum, e o painel entregaria links de vídeos que não existem mais — o pior
// jeito de descobrir, porque parece funcionar até alguém apertar o play.
func (s *Server) handleRemoverNuvem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	nuvem, err := s.deps.Store.NuvemPorID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "buscando conta de nuvem")
		return
	}

	if err := s.deps.Store.RemoverNuvem(r.Context(), id); err != nil {
		if status, _ := storeStatus(err); status == http.StatusConflict {
			writeError(w, s.deps.Log, http.StatusConflict, "conflict",
				"esta conta ainda guarda arquivos do acervo. Apague-os pela tela do "+
					"Acervo, ou desative a conta em vez de removê-la — desativada, ela "+
					"para de receber sem derrubar o que já está dentro.")
			return
		}
		s.fail(w, r, err, "removendo conta de nuvem")
		return
	}

	// O registro de backends precisa esquecer a conta na hora: continuar servindo de uma
	// conta removida seria pior que falhar.
	if s.deps.Armazenamento != nil {
		s.deps.Armazenamento.Remover(armazenamento.ChaveDaNuvem(id))
	}
	s.logEvent(r, "acervo", "warn", "conta de nuvem removida: "+nuvem.Nome, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"ok": true})
}

// handleOrganizarNuvem cria (ou reencontra) a pasta do acervo e passa a usá-la.
//
// # Por que um botão, e não um campo para colar o id
//
// O escopo é `drive.file`: o sistema só enxerga o que ele mesmo criou. Uma pasta feita à mão
// na tela do Drive é invisível para ele, e colar o id dela produziria erro em toda gravação
// — um erro que não diria "esta pasta não é minha".
//
// Então a única forma que funciona é o sistema criar a pasta. O campo de id continua
// existindo para quem repete um cadastro e já tem o id de uma pasta NOSSA.
//
// # O que acontece com o que já está na raiz
//
// Nada: os arquivos ficam onde estão e continuam sendo servidos. A pasta vale das próximas
// gravações em diante. Mover os antigos seria uma operação longa sobre centenas de arquivos,
// com o acervo no ar — e o ganho é só arrumação.
func (s *Server) handleOrganizarNuvem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	if s.deps.Nuvens == nil {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "acervo_indisponivel",
			"este processo não gerencia o acervo")
		return
	}

	backend, err := s.deps.Nuvens.BackendDaNuvem(r.Context(), id)
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadGateway, "conta_indisponivel",
			"não foi possível falar com a conta: "+err.Error())
		return
	}
	organizavel, ok := backend.(interface {
		GarantirPasta(context.Context, string) (string, error)
	})
	if !ok {
		writeError(w, s.deps.Log, http.StatusBadRequest, "provedor_sem_pasta",
			"este provedor não organiza o acervo em pastas")
		return
	}

	pasta, err := organizavel.GarantirPasta(r.Context(), armazenamento.PastaPadrao)
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadGateway, "falha_ao_criar_pasta",
			"não foi possível criar a pasta: "+err.Error())
		return
	}

	nuvem, err := s.deps.Store.AtualizarNuvem(r.Context(), id, store.AjusteDeNuvem{PastaRaiz: &pasta})
	if err != nil {
		s.fail(w, r, err, "gravando a pasta da conta")
		return
	}

	s.logEvent(r, "acervo", "info",
		"pasta do acervo definida na conta "+nuvem.Nome, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{"nuvem": nuvem, "pasta": pasta})
}
