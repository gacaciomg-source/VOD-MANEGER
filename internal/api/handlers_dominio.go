package api

import (
	"net/http"
	"os"
	"os/exec"
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
			"atualize o sistema para habilitá-lo"
	}
	// Só um programa pode responder pela porta 80 desta máquina, e é por ela que o domínio
	// sem `:porta` chega. Se o nginx do aaPanel já a segura, este botão não tem como agir —
	// e dizer isso na tela, com o caminho que funciona logo abaixo, vale muito mais que
	// deixar a pessoa clicar e receber um erro.
	//
	// O que impede o botão não é o aaPanel ESTAR instalado — é o nginx dele estar
	// SEGURANDO a porta 80. Desligar o botão pela simples presença da pasta recusaria o
	// caminho direto em máquinas onde o aaPanel veio na imagem e nunca serviu nada; uma
	// trava que barra o que funcionaria manda a pessoa procurar um jeito pior.
	if disponivel && aaPanelNginxAtivo() {
		disponivel = false
		motivo = "o nginx do aaPanel está no ar e já responde pela porta 80. Configurar o " +
			"domínio por aqui instalaria um segundo nginx, e os dois brigariam pela mesma " +
			"porta — o domínio não responderia e os sites que já existem lá poderiam cair. " +
			"O caminho para esta máquina é criar o site pelo aaPanel, com o bloco abaixo."
	}
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"disponivel":   disponivel,
		"motivo":       motivo,
		"em_andamento": dominioEmAndamento(),
		"registro":     registroDe(registroDominio),
		"aapanel":      aaPanelInstalado(),
		"porta_local":  s.portaLocal(),
	})
}

// aaPanelInstalado reconhece a instalação do aaPanel pelos caminhos que ela sempre cria.
func aaPanelInstalado() bool {
	for _, caminho := range []string{"/www/server/panel", "/www/server/nginx/sbin/nginx"} {
		if _, err := os.Stat(caminho); err == nil {
			return true
		}
	}
	return false
}

// aaPanelNginxAtivo pergunta ao sistema, e não ao disco: instalado e no ar são coisas
// diferentes, e só a segunda atrapalha.
func aaPanelNginxAtivo() bool {
	if !aaPanelInstalado() {
		return false
	}
	return exec.Command("pgrep", "-f", "/www/server/nginx").Run() == nil
}

// portaLocal é a porta em que este processo escuta. A tela do aaPanel precisa dela para
// montar o proxy_pass — errar essa porta é o erro nº 1 de quem segue o guia à mão.
func (s *Server) portaLocal() string {
	addr := os.Getenv("VODM_HTTP_ADDR")
	if i := strings.LastIndex(addr, ":"); i >= 0 && i+1 < len(addr) {
		return addr[i+1:]
	}
	return "8080"
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
	// Mais de um nome, separados por vírgula.
	//
	// O nginx só responde pelo nome EXATO que está no server_name: configurado apenas
	// `vod.seudominio.com`, digitar `seudominio.com` não abre nada — e o nome curto é
	// justamente o que se lembra às pressas. O script ainda acrescenta sozinho o domínio
	// raiz e o www quando eles apontam para esta máquina.
	nomes := []string{}
	for _, bruto := range strings.Split(req.Dominio, ",") {
		nome := strings.ToLower(strings.TrimSpace(bruto))
		nome = strings.TrimPrefix(strings.TrimPrefix(nome, "https://"), "http://")
		nome = strings.TrimSuffix(nome, "/")
		if nome == "" {
			continue
		}
		if !dominioValido.MatchString(nome) {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
				"informe um domínio como vod.seudominio.com, sem http:// e sem barra", "dominio")
			return
		}
		nomes = append(nomes, nome)
	}
	if len(nomes) == 0 {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe um domínio como vod.seudominio.com, sem http:// e sem barra", "dominio")
		return
	}
	// O primeiro é o principal: é ele que vira o endereço público e o que aparece nos
	// links. Os demais são atalhos que levam ao mesmo lugar.
	dominio := strings.Join(nomes, ",")
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

	s.logEvent(r, "sistema", "warn", "configuração de domínio iniciada: "+nomes[0], actorOf(r), nil)
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
