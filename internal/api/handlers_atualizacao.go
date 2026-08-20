package api

import (
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Atualização pelo painel.
//
// O sistema troca o PRÓPRIO binário e reinicia o próprio serviço. Duas armadilhas moldaram
// o desenho:
//
//  1. Se o atualizador fosse filho deste processo, morreria no instante em que o serviço
//     parasse — no meio da troca, deixando a instalação pela metade. Por isso quem executa
//     é o systemd, não nós.
//  2. O serviço roda como um usuário sem privilégio e com NoNewPrivileges, que impede até
//     o sudo de elevar. Abrir exceção nisso enfraqueceria o processo que fica exposto na
//     internet.
//
// A saída para as duas foi inverter o sentido: o painel apenas CRIA UM ARQUIVO na única
// pasta onde já pode escrever, e uma unidade `.path` do systemd — que é root — observa esse
// arquivo e dispara a atualização.
//
// O processo nunca ganha privilégio nenhum, e não escolhe o que será executado: só pede que
// a atualização aconteça. O que roda foi definido pelo root na instalação.
const (
	pedidoAtualizacao     = "/opt/vodmanager/solicitar-atualizacao"
	unidadeAtualizacao    = "vodmanager-update.service"
	observadorAtualizacao = "vodmanager-update.path"
	registroAtualizacao   = "/opt/vodmanager/ultima-atualizacao.log"
	// maxRegistro limita o que devolvemos: o registro cresce a cada atualização, e não
	// faz sentido mandar tudo para preencher uma caixa de texto.
	maxRegistro = 16 << 10
)

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	disponivel, motivo := s.podeAtualizar()
	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"versao_atual": s.deps.Version,
		"disponivel":   disponivel,
		"motivo":       motivo,
		"em_andamento": emAndamento(),
		"registro":     ultimoRegistro(),
	})
}

// handleUpdateStart pede a atualização.
//
// Responde antes de o trabalho terminar, de propósito: a atualização derruba este mesmo
// processo, e uma resposta que esperasse o fim nunca chegaria ao navegador.
func (s *Server) handleUpdateStart(w http.ResponseWriter, r *http.Request) {
	if disponivel, motivo := s.podeAtualizar(); !disponivel {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "indisponivel", motivo)
		return
	}
	if emAndamento() {
		writeError(w, s.deps.Log, http.StatusConflict, "em_andamento",
			"já existe uma atualização em andamento")
		return
	}

	conteudo := "pedido em " + time.Now().Format(time.RFC3339) + " por " + actorOf(r) + "\n"
	if err := os.WriteFile(pedidoAtualizacao, []byte(conteudo), 0o644); err != nil {
		s.fail(w, r, err, "registrando o pedido de atualização")
		return
	}

	s.logEvent(r, "sistema", "warn", "atualização iniciada pelo painel", actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusAccepted, map[string]any{
		"iniciada": true,
		"aviso": "A atualização começou em segundo plano. O serviço vai reiniciar e o painel " +
			"ficará alguns segundos fora do ar. Um backup é feito antes de qualquer troca, e " +
			"se a versão nova não subir o sistema volta sozinho para a anterior.",
	})
}

// podeAtualizar verifica se este ambiente suporta a atualização pelo painel.
//
// Devolve o motivo junto: um botão que some sem explicação é pior que um botão desligado
// que diz por quê.
func (s *Server) podeAtualizar() (bool, string) {
	if runtime.GOOS != "linux" {
		return false, "a atualização pelo painel só existe em Linux; em desenvolvimento use os comandos do terminal"
	}
	if unidadeAtiva(observadorAtualizacao) {
		return true, ""
	}
	return false, "o observador de atualização não está ativo nesta máquina; " +
		"rode o instalador novamente para habilitá-lo"
}

// unidadeAtiva consulta o estado de uma unidade. Ler o estado não exige privilégio.
func unidadeAtiva(unidade string) bool {
	saida, _ := exec.Command("systemctl", "is-active", unidade).Output()
	return strings.TrimSpace(string(saida)) == "active"
}

// emAndamento cobre os dois sinais: o pedido ainda não consumido e a unidade rodando.
func emAndamento() bool {
	if _, err := os.Stat(pedidoAtualizacao); err == nil {
		return true
	}
	saida, _ := exec.Command("systemctl", "is-active", unidadeAtualizacao).Output()
	estado := strings.TrimSpace(string(saida))
	return estado == "active" || estado == "activating"
}

// ultimoRegistro devolve o fim do relato da última atualização.
func ultimoRegistro() string {
	dados, err := os.ReadFile(registroAtualizacao)
	if err != nil {
		return ""
	}
	if len(dados) > maxRegistro {
		dados = dados[len(dados)-maxRegistro:]
	}
	return string(dados)
}
