package api

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"

	"vodmanager/internal/store"
)

// Migração para outra máquina, pelo painel.
//
// Mesmo mecanismo da atualização e do domínio: o painel escreve um pedido na única pasta
// onde pode escrever, e uma unidade `.path` do systemd — que é root — executa o script.
// O serviço nunca ganha privilégio e não escolhe o que roda.
//
// # A senha do SSH
//
// É o único lugar do sistema em que uma senha de OUTRA máquina passa por aqui, e ela
// precisa passar: o destino é uma VPS recém-criada, cuja única forma de entrada é a senha
// de root que o provedor mandou por e-mail.
//
// O que fazemos com ela:
//
//   - nunca é gravada no banco, nem em configuração, nem em evento;
//   - vai para um arquivo com permissão 0600 numa pasta 0750 do usuário do serviço;
//   - é apagada do disco pelo script ANTES de a migração começar — não depois;
//   - nunca aparece em linha de comando, que seria visível no `ps` para qualquer usuário
//     da máquina durante os muitos minutos que a migração leva.
//
// O que NÃO fazemos: guardar para reusar. Cada migração pede a senha de novo.
const (
	pedidoMigracao     = "/opt/vodmanager/runtime/solicitar-migracao"
	unidadeMigracao    = "vodmanager-migrar.service"
	observadorMigracao = "vodmanager-migrar.path"
	registroMigracao   = "/opt/vodmanager/runtime/ultima-migracao.log"
)

// usuarioSSHValido evita que um nome de usuário carregue caracteres que o pedido usa como
// estrutura (quebra de linha) ou que o shell trataria de forma especial.
var usuarioSSHValido = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func (s *Server) handleMigracaoStatus(w http.ResponseWriter, r *http.Request) {
	disponivel := unidadeAtiva(observadorMigracao)
	motivo := ""
	if !disponivel {
		motivo = "o observador de migração não está ativo nesta máquina; " +
			"atualize o sistema (aba Sistema) para habilitá-lo"
	}

	// O domínio configurado muda completamente o conselho pós-migração: com domínio, os
	// links dos clientes continuam idênticos e basta apontar o DNS; sem domínio, o
	// endereço dentro dos links muda de IP e alguém precisa trocá-lo no XC_VM.
	//
	// Dizer isso ANTES da migração é o que evita a descoberta pelo pior caminho.
	base, _ := s.deps.Store.GetSetting(r.Context(), store.SettingPublicBaseURL, "")
	dominio := dominioDaBase(base)

	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"disponivel":    disponivel,
		"motivo":        motivo,
		"em_andamento":  migracaoEmAndamento(),
		"registro":      registroDe(registroMigracao),
		"base_publica":  base,
		"dominio_atual": dominio,
		"usa_dominio":   dominio != "",
	})
}

type migrarRequest struct {
	// Destino é o endereço da máquina nova: IP ou nome.
	Destino string `json:"destino"`
	// Usuario é quem entra por SSH lá. Root, no caso normal de uma VPS recém-criada.
	Usuario string `json:"usuario"`
	// Senha do SSH do destino. Não é guardada em lugar nenhum.
	Senha    string `json:"senha"`
	PortaSSH int    `json:"porta_ssh"`
	PortaApp int    `json:"porta_app"`
}

func (s *Server) handleMigrarStart(w http.ResponseWriter, r *http.Request) {
	if !unidadeAtiva(observadorMigracao) {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "indisponivel",
			"o observador de migração não está ativo; atualize o sistema para habilitá-lo")
		return
	}
	if migracaoEmAndamento() {
		writeError(w, s.deps.Log, http.StatusConflict, "em_andamento",
			"já existe uma migração em andamento")
		return
	}

	var req migrarRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}

	destino := strings.TrimSpace(req.Destino)
	if destino == "" || !enderecoDeMaquinaValido(destino) {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe o IP ou o nome da máquina de destino", "destino")
		return
	}

	usuario := strings.TrimSpace(req.Usuario)
	if usuario == "" {
		usuario = "root"
	}
	if !usuarioSSHValido.MatchString(usuario) {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"usuário de SSH inválido", "usuario")
		return
	}

	// A senha é a única coisa aqui que pode conter qualquer caractere. A quebra de linha é
	// a exceção: o pedido é um arquivo de uma linha por campo, e uma quebra criaria um
	// campo novo — o jeito clássico de transformar um campo de texto em injeção.
	if req.Senha == "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe a senha de SSH do destino", "senha")
		return
	}
	if strings.ContainsAny(req.Senha, "\n\r") {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"a senha não pode conter quebra de linha", "senha")
		return
	}

	portaSSH := req.PortaSSH
	if portaSSH == 0 {
		portaSSH = 22
	}
	portaApp := req.PortaApp
	if portaApp == 0 {
		portaApp = 8080
	}
	if !portaValida(portaSSH) || !portaValida(portaApp) {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"as portas precisam estar entre 1 e 65535", "porta_ssh", "porta_app")
		return
	}

	// 0600 e escrita direta, sem arquivo temporário renomeado: o pedido some em segundos
	// e não vale a pena deixar uma segunda cópia pelo caminho.
	linhas := fmt.Sprintf("destino=%s@%s\nporta_ssh=%d\nporta_app=%d\nsenha=%s\n",
		usuario, destino, portaSSH, portaApp, req.Senha)
	if err := os.WriteFile(pedidoMigracao, []byte(linhas), 0o600); err != nil {
		s.fail(w, r, err, "registrando o pedido de migração")
		return
	}

	// O evento registra PARA ONDE, nunca com qual senha.
	s.logEvent(r, "sistema", "warn", "migração iniciada pelo painel para "+destino, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusAccepted, map[string]any{
		"iniciada": true,
		"aviso": "A migração começou em segundo plano e leva vários minutos — o destino " +
			"instala Postgres, Go e compila o sistema antes de receber os dados. " +
			"Este servidor continua no ar o tempo todo: nada aqui é desligado nem apagado.",
	})
}

func migracaoEmAndamento() bool {
	if _, err := os.Stat(pedidoMigracao); err == nil {
		return true
	}
	return unidadeEmExecucao(unidadeMigracao)
}

func portaValida(p int) bool { return p > 0 && p < 65536 }

// enderecoDeMaquinaValido aceita IP ou nome de host, e recusa o resto.
//
// Recusar cedo é o que impede um "destino" com espaço, aspas ou quebra de linha de virar
// argumento de um comando remoto lá adiante.
func enderecoDeMaquinaValido(v string) bool {
	if len(v) > 253 {
		return false
	}
	if net.ParseIP(v) != nil {
		return true
	}
	return dominioValido.MatchString(v)
}

// dominioDaBase extrai o nome de domínio do endereço público, quando ele não é um IP.
//
// Um IP não é domínio: para quem atende por IP, migrar muda o endereço e pronto. Para quem
// atende por domínio, o nginx e o certificado ficam para trás — e é isso que precisa ser
// dito antes, não depois.
func dominioDaBase(base string) string {
	if base == "" {
		return ""
	}
	hospedeiro := base
	if i := strings.Index(hospedeiro, "://"); i >= 0 {
		hospedeiro = hospedeiro[i+3:]
	}
	if i := strings.IndexAny(hospedeiro, "/?#"); i >= 0 {
		hospedeiro = hospedeiro[:i]
	}
	if h, _, err := net.SplitHostPort(hospedeiro); err == nil {
		hospedeiro = h
	}
	if hospedeiro == "" || net.ParseIP(hospedeiro) != nil {
		return ""
	}
	if strings.EqualFold(hospedeiro, "localhost") {
		return ""
	}
	return hospedeiro
}
