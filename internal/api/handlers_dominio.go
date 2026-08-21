package api

import (
	"net/http"
	"os"
	"regexp"
	"strings"
)

// Configuração de domínio e HTTPS pelo painel.
//
// Mesmo mecanismo da atualização: o painel apenas CRIA UM ARQUIVO na única pasta onde pode
// escrever, e uma unidade `.path` do systemd — que é root — executa o script. O serviço
// nunca ganha privilégio, e não escolhe o que roda.
//
// # A decisão que governa este recurso
//
// A porta atual nunca é fechada, e o endereço de escuta do serviço nunca é alterado.
//
// Os links já entregues aos clientes contêm o IP e a porta. Fechá-la ao ligar o domínio
// derrubaria todo mundo de uma vez, e cada cliente só voltaria depois de receber um link
// novo. Com os dois caminhos vivos, a migração é gradual.
const (
	pedidoDominio   = "/opt/vodmanager/runtime/solicitar-dominio"
	unidadeDominio  = "vodmanager-domain.service"
	observadorDomin = "vodmanager-domain.path"
	registroDominio = "/opt/vodmanager/runtime/ultimo-dominio.log"
)

// dominioValido é o mesmo formato que o script aceita. Validar aqui devolve um erro
// imediato no painel, em vez de uma falha vista só no registro minutos depois.
var dominioValido = regexp.MustCompile(
	`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$`)

func (s *Server) handleDominioStatus(w http.ResponseWriter, r *http.Request) {
	disponivel := unidadeAtiva(observadorDomin)
	motivo := ""
	if !disponivel {
		motivo = "o observador de domínio não está ativo nesta máquina; " +
			"rode o instalador novamente para habilitá-lo"
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"disponivel":   disponivel,
		"motivo":       motivo,
		"em_andamento": dominioEmAndamento(),
		"registro":     registroDe(registroDominio),
	})
}

type configurarDominioRequest struct {
	Dominio string `json:"dominio"`
	// Email recebe os avisos de expiração do certificado. Opcional, mas sem ele ninguém
	// é avisado quando a renovação automática falhar.
	Email string `json:"email"`
}

func (s *Server) handleConfigurarDominio(w http.ResponseWriter, r *http.Request) {
	if !unidadeAtiva(observadorDomin) {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "indisponivel",
			"o observador de domínio não está ativo; rode o instalador novamente")
		return
	}
	if dominioEmAndamento() {
		writeError(w, s.deps.Log, http.StatusConflict, "em_andamento",
			"já existe uma configuração de domínio em andamento")
		return
	}

	var req configurarDominioRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}
	dominio := strings.ToLower(strings.TrimSpace(req.Dominio))
	dominio = strings.TrimPrefix(strings.TrimPrefix(dominio, "https://"), "http://")
	dominio = strings.TrimSuffix(dominio, "/")

	if !dominioValido.MatchString(dominio) {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe um domínio como vod.seudominio.com, sem http:// e sem barra", "dominio")
		return
	}
	email := strings.TrimSpace(req.Email)
	if strings.ContainsAny(email, " \n\t") {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"e-mail inválido", "email")
		return
	}

	// Domínio e e-mail numa linha só, separados por espaço: é o formato que a unidade lê.
	// Ambos já foram validados, então não há como injetar um argumento a mais.
	linha := dominio + " " + email + "\n"
	if err := os.WriteFile(pedidoDominio, []byte(linha), 0o644); err != nil {
		s.fail(w, r, err, "registrando o pedido de domínio")
		return
	}

	s.logEvent(r, "sistema", "warn", "configuração de domínio iniciada: "+dominio, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusAccepted, map[string]any{
		"iniciada": true,
		"aviso": "A configuração começou. Leva um ou dois minutos: o domínio é conferido, " +
			"o nginx é instalado se faltar, e o certificado é emitido. " +
			"O acesso pelo IP continua funcionando durante todo o processo.",
	})
}

func dominioEmAndamento() bool {
	if _, err := os.Stat(pedidoDominio); err == nil {
		return true
	}
	return unidadeEmExecucao(unidadeDominio)
}

// registroDe devolve o fim de um arquivo de registro.
func registroDe(caminho string) string {
	dados, err := os.ReadFile(caminho)
	if err != nil {
		return ""
	}
	if len(dados) > maxRegistro {
		dados = dados[len(dados)-maxRegistro:]
	}
	return string(dados)
}

// tempoDeEspera é quanto o painel deve aguardar antes de desistir de acompanhar.
