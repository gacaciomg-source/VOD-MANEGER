package guards

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"vodmanager/test/fixtures"
)

// padraoSuspeito é um sinal de credencial real dentro de um fixture.
type padraoSuspeito struct {
	nome string
	re   *regexp.Regexp
	// permitidos são valores fictícios convencionados que não devem disparar alarme.
	permitidos []string
}

var padroes = []padraoSuspeito{
	{
		nome:       "usuário em query string",
		re:         regexp.MustCompile(`(?i)\b(?:username|user|usuario|login)=([^&"'\s,}]+)`),
		permitidos: []string{"usuario", "user", "teste", "exemplo", "removido"},
	},
	{
		nome:       "senha em query string",
		re:         regexp.MustCompile(`(?i)\b(?:password|pass|senha|pwd)=([^&"'\s,}]+)`),
		permitidos: []string{"senha", "pass", "teste", "exemplo", "removido"},
	},
	{
		nome:       "credencial no userinfo da URL",
		re:         regexp.MustCompile(`(?i)://([^/\s:@"']+):([^/\s@"']+)@`),
		permitidos: []string{"usuario", "senha", "user", "pass", "removido"},
	},
	{
		nome:       "credencial no path (padrão /movie/user/pass/)",
		re:         regexp.MustCompile(`(?i)/(?:movie|series|live)/([^/\s"']+)/([^/\s"']+)/`),
		permitidos: []string{"usuario", "senha", "user", "pass", "removido"},
	},
	{
		nome:       "token longo",
		re:         regexp.MustCompile(`(?i)\b(?:token|api_key|apikey|auth|secret|sid)["'\s:=]+([A-Za-z0-9_\-]{24,})`),
		permitidos: []string{},
	},
	{
		nome:       "IP público",
		re:         regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`),
		permitidos: []string{},
	},
}

// dominiosPermitidos são os únicos hosts aceitos nos fixtures.
var (
	reHost = regexp.MustCompile(`(?i)://([a-z0-9.\-]+)`)
	reIP   = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$`)
)

var dominiosPermitidos = map[string]bool{
	"exemplo.tld":         true,
	"fonte-a.exemplo.tld": true,
	"fonte-b.exemplo.tld": true,
	"fonte.exemplo.tld":   true,
	"x.exemplo.tld":       true,
	"localhost":           true,
	"127.0.0.1":           true,
}

// TestFixturesNaoContemCredenciais varre testdata/ inteiro procurando credencial real.
//
// É a rede de segurança contra colar uma amostra sem anonimizar direito. Ela roda no CI
// e falha a suíte — o vazamento não chega ao repositório remoto.
func TestFixturesNaoContemCredenciais(t *testing.T) {
	raiz := fixtures.Dir(t)
	arquivos := 0

	err := filepath.WalkDir(raiz, func(caminho string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		conteudo, err := os.ReadFile(caminho)
		if err != nil {
			return err
		}
		arquivos++
		rel, _ := filepath.Rel(raiz, caminho)
		verificarConteudo(t, rel, string(conteudo))
		return nil
	})
	if err != nil {
		t.Fatalf("varrendo testdata: %v", err)
	}
	if arquivos == 0 {
		t.Fatal("nenhum fixture encontrado — a varredura não estaria protegendo nada")
	}
	t.Logf("%d arquivos de fixture verificados", arquivos)
}

func verificarConteudo(t *testing.T, arquivo, conteudo string) {
	t.Helper()

	// O README de testdata explica os padrões e por isso os cita: ele é a documentação
	// da própria regra, não um fixture.
	if strings.EqualFold(filepath.Base(arquivo), "README.md") {
		return
	}

	for _, p := range padroes {
		for _, m := range p.re.FindAllStringSubmatch(conteudo, -1) {
			for _, capturado := range m[1:] {
				if valorPermitido(capturado, p.permitidos) {
					continue
				}
				if p.nome == "IP público" && ipPrivadoOuDocumentacao(capturado) {
					continue
				}
				t.Errorf("%s: possível credencial real (%s): %q\n"+
					"Se for valor fictício, use as convenções de testdata/README.md.",
					arquivo, p.nome, capturado)
			}
		}
	}

	for _, m := range reHost.FindAllStringSubmatch(conteudo, -1) {
		host := strings.ToLower(m[1])
		if reIP.MatchString(host) {
			// Host numérico já é coberto pela regra de IP acima; aplicá-la de novo aqui
			// evitaria ter que listar toda faixa privada como "domínio permitido".
			continue
		}
		if !dominiosPermitidos[host] && !strings.HasSuffix(host, ".exemplo.tld") {
			t.Errorf("%s: domínio não permitido em fixture: %q\n"+
				"Use exemplo.tld ou um subdomínio dele.", arquivo, host)
		}
	}
}

func valorPermitido(valor string, permitidos []string) bool {
	v := strings.ToLower(strings.Trim(valor, `"' `))
	if v == "" {
		return true
	}
	for _, p := range permitidos {
		if v == p {
			return true
		}
	}
	return false
}

// ipPrivadoOuDocumentacao aceita faixas que, por definição, não são de ninguém.
func ipPrivadoOuDocumentacao(ip string) bool {
	prefixos := []string{"10.", "192.168.", "127.", "0.", "203.0.113.", "198.51.100.", "192.0.2."}
	for _, p := range prefixos {
		if strings.HasPrefix(ip, p) {
			return true
		}
	}
	if strings.HasPrefix(ip, "172.") {
		return true // 172.16–31 é privado; aceitar a faixa inteira é conservador o bastante
	}
	return false
}

// A varredura precisa realmente pegar credencial. Sem este teste, um erro de regex
// tornaria a guarda um placebo silencioso.
func TestVarreduraDetectaCredencialPlantada(t *testing.T) {
	amostras := map[string]string{
		"usuário e senha em query": `http://fonte.exemplo.tld/player_api.php?username=joao_real&password=S3nh4Real`,
		"credencial no userinfo":   `http://joaoreal:S3nh4Real@fonte.exemplo.tld/a.mp4`,
		"credencial no path":       `http://fonte.exemplo.tld/movie/joao_real/S3nh4Real/1.mp4`,
		"token longo":              `{"token":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9abc"}`,
		"IP público":               `http://200.150.100.50/lista.m3u`,
		"domínio real":             `http://servidor-de-verdade.com/lista.m3u`,
	}

	for nome, amostra := range amostras {
		t.Run(nome, func(t *testing.T) {
			falso := &testing.T{}
			verificarConteudo(falso, "amostra_plantada.txt", amostra)
			if !falso.Failed() {
				t.Errorf("a varredura NÃO detectou a credencial plantada: %s", amostra)
			}
		})
	}
}

// E não pode acusar os valores fictícios convencionados, senão vira ruído e as pessoas
// passam a ignorar o alarme.
func TestVarreduraNaoAcusaValoresFicticios(t *testing.T) {
	amostras := []string{
		`http://fonte-a.exemplo.tld/movie/usuario/senha/12345.mp4`,
		`http://fonte.exemplo.tld/player_api.php?username=usuario&password=senha`,
		`http://192.168.1.10/lista.m3u`,
		`{"stream_id":"12345","container_extension":"mp4"}`,
	}
	for _, amostra := range amostras {
		falso := &testing.T{}
		verificarConteudo(falso, "amostra_ok.txt", amostra)
		if falso.Failed() {
			t.Errorf("falso positivo em valor fictício convencionado: %s", amostra)
		}
	}
}
