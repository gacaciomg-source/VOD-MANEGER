package api

import (
	"errors"
	"net/http"
	"strings"

	"vodmanager/internal/auth"
	"vodmanager/internal/store"
)

type trocaSenhaContaRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword troca a senha de quem está logado.
//
// Três decisões que valem explicação:
//
//   - Exige a senha atual. Sem isso, uma sessão esquecida aberta num computador alheio
//     bastaria para tomar a conta.
//   - Não permite trocar a senha de OUTRA pessoa. Gestão de usuários é outra tela, que
//     ainda não existe; até lá, cada um troca a própria.
//   - Encerra as demais sessões. Trocar a senha é o que se faz quando se desconfia de um
//     acesso — deixar a sessão do invasor viva anularia o gesto.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeError(w, s.deps.Log, http.StatusUnauthorized, "unauthenticated", "autenticação necessária")
		return
	}

	var req trocaSenhaContaRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	nova := strings.TrimSpace(req.NewPassword)
	if nova == "" || req.CurrentPassword == "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe a senha atual e a nova", "new_password")
		return
	}

	// A verificação vem antes de qualquer outra checagem: assim o tempo de resposta não
	// muda conforme a nova senha ser válida ou não.
	usuario, err := s.deps.Store.GetUserByID(r.Context(), p.User.ID)
	if err != nil {
		s.fail(w, r, err, "buscando usuário")
		return
	}
	confere, err := auth.VerifyPassword(req.CurrentPassword, usuario.PasswordHash)
	if err != nil || !confere {
		s.logEvent(r, "auth", "warn",
			"troca de senha recusada: senha atual incorreta", actorOf(r), nil)
		writeError(w, s.deps.Log, http.StatusForbidden, "senha_atual_incorreta",
			"a senha atual está incorreta", "current_password")
		return
	}

	if nova == req.CurrentPassword {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"a nova senha precisa ser diferente da atual", "new_password")
		return
	}

	// HashPassword aplica o mínimo de tamanho; a mensagem dele já é específica.
	hash, err := auth.HashPassword(nova)
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", err.Error(), "new_password")
		return
	}
	if err := s.deps.Store.SetUserPassword(r.Context(), p.User.ID, hash); err != nil {
		s.fail(w, r, err, "trocando senha")
		return
	}

	// Todas as sessões caem, inclusive esta: quem trocou refaz o login com a senha nova,
	// e qualquer outra sessão aberta em outro lugar deixa de valer.
	encerradas, err := s.deps.Store.RevokeUserSessions(r.Context(), p.User.ID)
	if err != nil {
		s.deps.Log.Warn("senha trocada mas as sessões continuaram abertas", "erro", err)
	}
	limparCookieSessao(w, s.deps.CookieName, s.cookieSeguro(r))

	s.logEvent(r, "auth", "info", "senha do painel trocada", actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"ok":                 true,
		"sessoes_encerradas": encerradas,
		"aviso":              "Sua senha foi trocada. Entre novamente com a nova senha.",
	})
}

// --- Usuários do painel -------------------------------------------------------
//
// Gestão de quem entra no painel. Só administrador: dar a alguém o poder de criar contas
// é o mesmo que dar o poder de se promover.

type novoUsuarioRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	usuarios, err := s.deps.Store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, r, err, "listando usuários")
		return
	}
	atual := int64(0)
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		atual = p.User.ID
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"users":     usuarios,
		"eu":        atual,
		"papeis":    []string{store.RoleAdmin, store.RoleOperator, store.RoleViewer},
		"descricao": descricaoDosPapeis(),
	})
}

// descricaoDosPapeis explica o que cada papel permite, em português.
//
// Sem isto o administrador escolhe no escuro — e a diferença entre "operator" e "viewer"
// é justamente o que decide se o sócio dele pode apagar uma fonte.
func descricaoDosPapeis() map[string]string {
	return map[string]string{
		store.RoleAdmin:    "Faz tudo, inclusive criar e remover usuários e atualizar o sistema.",
		store.RoleOperator: "Cadastra fontes, sincroniza, cria credenciais e vê as senhas de acesso. Não gerencia usuários.",
		store.RoleViewer:   "Só olha: catálogo, reproduções e relatórios. Não altera nada nem vê senhas.",
	}
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req novoUsuarioRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}

	username := strings.TrimSpace(req.Username)
	if username == "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "informe o usuário", "username")
		return
	}
	if !store.ValidUserRole(req.Role) {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"papel inválido", "role")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", err.Error(), "password")
		return
	}

	usuario, err := s.deps.Store.CreateUser(r.Context(), username, hash, req.Role)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, s.deps.Log, http.StatusConflict, "usuario_em_uso",
				"já existe um usuário com este nome", "username")
			return
		}
		s.fail(w, r, err, "criando usuário")
		return
	}

	s.logEvent(r, "auth", "warn", "usuário do painel criado: "+usuario.Username+
		" ("+usuario.Role+")", actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusCreated, usuario)
}

type patchUsuarioRequest struct {
	Role     *string `json:"role"`
	Enabled  *bool   `json:"enabled"`
	Password *string `json:"password"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	var req patchUsuarioRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	if req.Role != nil && !store.ValidUserRole(*req.Role) {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "papel inválido", "role")
		return
	}

	// Rebaixar ou desabilitar o último administrador tranca o sistema por dentro: não há
	// tela de recuperação, e a correção exigiria mexer no banco à mão.
	rebaixando := req.Role != nil && *req.Role != store.RoleAdmin
	desabilitando := req.Enabled != nil && !*req.Enabled
	if rebaixando || desabilitando {
		if msg := s.ultimoAdmin(r, id); msg != "" {
			writeError(w, s.deps.Log, http.StatusConflict, "ultimo_admin", msg)
			return
		}
	}

	if req.Password != nil && *req.Password != "" {
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", err.Error(), "password")
			return
		}
		if err := s.deps.Store.SetUserPassword(r.Context(), id, hash); err != nil {
			s.fail(w, r, err, "trocando senha")
			return
		}
		// Trocar a senha de alguém tem que derrubar as sessões dele: senão, quem já
		// estava dentro continua dentro, e a troca não protege nada.
		if _, err := s.deps.Store.RevokeUserSessions(r.Context(), id); err != nil {
			s.deps.Log.Warn("sessões não encerradas após troca de senha", "erro", err)
		}
	}

	usuario, err := s.deps.Store.UpdateUser(r.Context(), id, store.UserPatch{
		Role: req.Role, Enabled: req.Enabled,
	})
	if err != nil {
		s.fail(w, r, err, "atualizando usuário")
		return
	}
	if desabilitando {
		if _, err := s.deps.Store.RevokeUserSessions(r.Context(), id); err != nil {
			s.deps.Log.Warn("sessões não encerradas ao desabilitar", "erro", err)
		}
	}

	s.logEvent(r, "auth", "warn", "usuário do painel alterado: "+usuario.Username, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusOK, usuario)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_id", "id inválido")
		return
	}
	if p, ok := auth.PrincipalFrom(r.Context()); ok && p.User.ID == id {
		writeError(w, s.deps.Log, http.StatusConflict, "auto_remocao",
			"você não pode remover a própria conta")
		return
	}
	if msg := s.ultimoAdmin(r, id); msg != "" {
		writeError(w, s.deps.Log, http.StatusConflict, "ultimo_admin", msg)
		return
	}

	if err := s.deps.Store.DeleteUser(r.Context(), id); err != nil {
		s.fail(w, r, err, "removendo usuário")
		return
	}
	s.logEvent(r, "auth", "warn", "usuário do painel removido", actorOf(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ultimoAdmin devolve a mensagem de recusa quando a operação deixaria o sistema sem
// nenhum administrador. Vazio significa que pode prosseguir.
func (s *Server) ultimoAdmin(r *http.Request, id int64) string {
	alvo, err := s.deps.Store.GetUserByID(r.Context(), id)
	if err != nil || alvo.Role != store.RoleAdmin {
		return ""
	}
	restantes, err := s.deps.Store.ContarAdministradoresAtivos(r.Context(), id)
	if err != nil || restantes > 0 {
		return ""
	}
	return "este é o único administrador ativo; promova outra pessoa antes, " +
		"senão ninguém conseguiria mais gerenciar o sistema"
}
