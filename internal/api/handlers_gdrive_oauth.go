package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"vodmanager/internal/cryptobox"
	"vodmanager/internal/store"
)

// Autorização do Google Drive, pelo painel.
//
// # Por que existe um fluxo, e não um campo de "cole o token aqui"
//
// O que o sistema precisa é um REFRESH TOKEN: o token de acesso do Google vale uma hora, e
// é a partir do refresh token que ele se renova. Sem isso, uma conta cadastrada pararia
// sozinha sessenta minutos depois — e o sintoma seria "o acervo sumiu", não "o token
// venceu".
//
// Obter um refresh token à mão exige montar uma URL de consentimento, copiar um código do
// navegador e fazer uma troca por HTTP. É um roteiro de sete passos em que errar um
// parâmetro produz um token que funciona hoje e morre amanhã. O painel faz isso: você
// autoriza no Google e volta com a conta cadastrada.
//
// # O que fica guardado
//
// O refresh token, cifrado com a chave mestra, junto do client id e do client secret. Eles
// nunca voltam em resposta nenhuma — nem para administrador, nem mascarados.

const (
	googleAutorizacao = "https://accounts.google.com/o/oauth2/v2/auth"
	googleToken       = "https://oauth2.googleapis.com/token"

	// escopoDrive é o acesso pedido ao Google.
	//
	// `drive.file` e não `drive`: ele dá acesso APENAS aos arquivos que este aplicativo
	// criou. Os documentos, fotos e planilhas que já estão na conta ficam invisíveis para
	// nós — não é uma promessa nossa, é o Google que não os entrega.
	//
	// Pedir `drive` (a conta inteira) seria mais fácil e daria a este sistema o poder de
	// ler e apagar tudo o que a pessoa tem no Drive. Um recurso de acervo não precisa
	// disso, e um dia em que algo dê errado a diferença entre os dois escopos é a
	// diferença entre perder o acervo e perder a vida digital de alguém.
	escopoDrive = "https://www.googleapis.com/auth/drive.file"

	// caminhoRetorno é o endereço para onde o Google devolve o navegador. Precisa ser
	// cadastrado como URI de redirecionamento autorizado no Google Cloud Console.
	caminhoRetorno = "/api/v1/nuvens/oauth/retorno"
)

// pendente é um cadastro esperando a autorização do Google.
//
// Fica em memória, e não no banco, de propósito: são segredos de vida curta que só
// interessam entre o clique e o retorno. Guardá-los no banco criaria uma segunda cópia
// permanente de um client secret para nada — e o pior lugar para um segredo é aquele onde
// ninguém lembra que ele está.
type pendente struct {
	nome         string
	clientID     string
	clientSecret string
	pastaRaiz    string
	ordem        int
	redirect     string
	criadoEm     time.Time
}

// pendentes guarda as autorizações em curso, por estado.
type pendentes struct {
	mu    sync.Mutex
	itens map[string]pendente
}

var autorizacoes = &pendentes{itens: map[string]pendente{}}

// validade é quanto tempo uma autorização iniciada continua aceitável.
//
// Curta: é o tempo de clicar em "permitir" numa tela do Google. Um valor generoso só
// aumentaria a janela em que um estado roubado ainda serve para alguma coisa.
const validadeDaAutorizacao = 15 * time.Minute

func (p *pendentes) guardar(estado string, item pendente) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Limpeza oportunista: sem ela, uma autorização abandonada guardaria um client secret
	// em memória até o processo reiniciar.
	for k, v := range p.itens {
		if time.Since(v.criadoEm) > validadeDaAutorizacao {
			delete(p.itens, k)
		}
	}
	p.itens[estado] = item
}

func (p *pendentes) tomar(estado string) (pendente, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	item, ok := p.itens[estado]
	if !ok {
		return pendente{}, false
	}
	// Consumido de uma vez: um código de autorização vale uma troca só, e o estado que o
	// acompanha não deve sobreviver a ela.
	delete(p.itens, estado)
	if time.Since(item.criadoEm) > validadeDaAutorizacao {
		return pendente{}, false
	}
	return item, true
}

type iniciarOAuthRequest struct {
	Nome         string `json:"nome"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	PastaRaiz    string `json:"pasta_raiz"`
	Ordem        int    `json:"ordem"`
}

// handleIniciarOAuthDrive devolve o endereço da tela de consentimento do Google.
func (s *Server) handleIniciarOAuthDrive(w http.ResponseWriter, r *http.Request) {
	var req iniciarOAuthRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body", "corpo inválido: "+err.Error())
		return
	}

	nome := strings.TrimSpace(req.Nome)
	clientID := strings.TrimSpace(req.ClientID)
	clientSecret := strings.TrimSpace(req.ClientSecret)
	if nome == "" || clientID == "" || clientSecret == "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe o nome da conta, o client id e o client secret")
		return
	}

	// O endereço de retorno precisa ser EXATAMENTE o que está cadastrado no Google, letra
	// por letra. Montá-lo aqui, a partir do endereço configurado, evita a divergência mais
	// comum — o painel acessado por um endereço e o Google esperando outro.
	redirect := strings.TrimRight(s.baseURL(r), "/") + caminhoRetorno

	// O Google recusa endereço de retorno em HTTP puro, e recusa de um jeito que não ajuda:
	// "Erro 400: invalid_request" numa tela dele, depois de a pessoa já ter criado o
	// projeto, ativado a API e preenchido tudo aqui.
	//
	// Só `localhost` escapa da regra, porque lá não há o que interceptar.
	//
	// Recusar antes de mandar para o Google troca aquele erro sem contexto por uma frase
	// que diz o que fazer — e economiza a ida e volta inteira.
	if !enderecoAceitoPeloGoogle(redirect) {
		writeError(w, s.deps.Log, http.StatusBadRequest, "endereco_inseguro",
			"o Google só aceita retorno em HTTPS, e o endereço público deste painel é "+
				strings.TrimRight(s.baseURL(r), "/")+". Emita o certificado do domínio e, "+
				"em Configurações, troque o endereço público para https:// antes de "+
				"cadastrar a conta.", "endereco_publico")
		return
	}

	var bruto [16]byte
	if _, err := rand.Read(bruto[:]); err != nil {
		s.fail(w, r, err, "gerando o estado da autorização")
		return
	}
	estado := hex.EncodeToString(bruto[:])

	autorizacoes.guardar(estado, pendente{
		nome: nome, clientID: clientID, clientSecret: clientSecret,
		pastaRaiz: strings.TrimSpace(req.PastaRaiz), ordem: req.Ordem,
		redirect: redirect, criadoEm: time.Now(),
	})

	q := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirect},
		"response_type": {"code"},
		"scope":         {escopoDrive},
		// access_type=offline é o que faz o Google devolver um REFRESH token. Sem ele, vem
		// só o token de acesso de uma hora — e a conta pararia sozinha.
		"access_type": {"offline"},
		// prompt=consent força a tela de permissão mesmo quando a pessoa já autorizou
		// antes. Sem isso, uma segunda conta do mesmo projeto voltaria SEM refresh token:
		// o Google só o envia na primeira autorização, e reautorizar é o jeito de pedi-lo
		// de novo.
		"prompt": {"consent"},
		"state":  {estado},
	}

	writeJSON(w, s.deps.Log, http.StatusOK, map[string]any{
		"url":          googleAutorizacao + "?" + q.Encode(),
		"redirect_uri": redirect,
	})
}

// handleRetornoOAuthDrive recebe o navegador de volta do Google e cadastra a conta.
//
// Responde HTML, e não JSON: quem chega aqui é uma janela do navegador vinda de um
// redirecionamento, não o código do painel. Devolver JSON mostraria chaves e colchetes à
// pessoa no exato momento em que ela espera uma confirmação.
func (s *Server) handleRetornoOAuthDrive(w http.ResponseWriter, r *http.Request) {
	if erroGoogle := r.URL.Query().Get("error"); erroGoogle != "" {
		s.paginaDeRetorno(w, false, "A autorização foi recusada no Google ("+erroGoogle+").")
		return
	}

	codigo := r.URL.Query().Get("code")
	estado := r.URL.Query().Get("state")
	if codigo == "" || estado == "" {
		s.paginaDeRetorno(w, false, "O Google não devolveu o código de autorização.")
		return
	}

	item, ok := autorizacoes.tomar(estado)
	if !ok {
		s.paginaDeRetorno(w, false,
			"Esta autorização expirou ou já foi usada. Comece de novo pelo Acervo.")
		return
	}

	refresh, err := trocarCodigoPorToken(r.Context(), item, codigo)
	if err != nil {
		s.deps.Log.Warn("troca de código do Google falhou", "erro", err)
		s.paginaDeRetorno(w, false, "O Google recusou a troca do código: "+err.Error())
		return
	}

	bruto, err := json.Marshal(credenciaisGDrive{
		ClientID: item.clientID, ClientSecret: item.clientSecret, RefreshToken: refresh,
	})
	if err != nil {
		s.paginaDeRetorno(w, false, "falha ao preparar as credenciais")
		return
	}
	cifrado, err := s.deps.Crypto.Seal(bruto, cryptobox.NuvemAAD(item.nome))
	if err != nil {
		s.paginaDeRetorno(w, false, "falha ao cifrar as credenciais")
		return
	}

	if _, err := s.deps.Store.CriarNuvem(r.Context(), store.NovaNuvem{
		Nome:        item.nome,
		Provedor:    store.ProvedorGDrive,
		PastaRaiz:   item.pastaRaiz,
		Ordem:       item.ordem,
		Credenciais: cifrado,
	}); err != nil {
		s.paginaDeRetorno(w, false, "a conta foi autorizada, mas não pôde ser salva: "+err.Error())
		return
	}

	s.logEvent(r, "acervo", "info", "conta de nuvem autorizada: "+item.nome, actorOf(r), nil)
	s.paginaDeRetorno(w, true,
		"A conta \""+item.nome+"\" foi autorizada e já está no Acervo.")
}

// trocarCodigoPorToken faz a troca com o Google.
func trocarCodigoPorToken(ctx context.Context, item pendente, codigo string) (string, error) {
	form := url.Values{
		"code":          {codigo},
		"client_id":     {item.clientID},
		"client_secret": {item.clientSecret},
		"redirect_uri":  {item.redirect},
		"grant_type":    {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleToken,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	cliente := &http.Client{Timeout: 30 * time.Second}
	resp, err := cliente.Do(req)
	if err != nil {
		return "", fmt.Errorf("não foi possível falar com o Google: %w", err)
	}
	defer resp.Body.Close()

	corpo, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		// A resposta de erro do Google diz o motivo em texto claro, e ele é quase sempre
		// acionável: redirect_uri divergente, client secret errado.
		return "", fmt.Errorf("o Google respondeu %s: %s", resp.Status, resumir(corpo))
	}

	var payload struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(corpo, &payload); err != nil {
		return "", fmt.Errorf("resposta do Google em formato inesperado")
	}
	if payload.RefreshToken == "" {
		// Acontece quando a conta já autorizou este projeto antes: o Google só manda o
		// refresh token na primeira vez. Dizer isso, com a saída, evita a conclusão errada
		// de que as credenciais estão erradas.
		return "", fmt.Errorf(
			"o Google não devolveu o token de renovação. Isso acontece quando esta conta " +
				"já autorizou este projeto antes. Remova o acesso em " +
				"myaccount.google.com/permissions e autorize de novo")
	}
	return payload.RefreshToken, nil
}

func resumir(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// paginaDeRetorno mostra o resultado e fecha a janela.
func (s *Server) paginaDeRetorno(w http.ResponseWriter, ok bool, mensagem string) {
	titulo, cor := "Conta autorizada", "#3fb950"
	if !ok {
		titulo, cor = "Não deu certo", "#f85149"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	status := http.StatusOK
	if !ok {
		status = http.StatusBadRequest
	}
	w.WriteHeader(status)

	// A janela se fecha sozinha no caso de sucesso, e espera no caso de erro: uma
	// mensagem de falha que some antes de ser lida é o mesmo que nenhuma mensagem.
	fmt.Fprintf(w, `<!doctype html><html lang="pt-BR"><head><meta charset="utf-8">
<title>%s</title></head>
<body style="background:#0e1116;color:#e6edf3;font:15px system-ui;padding:48px;text-align:center">
<h1 style="color:%s;font-size:20px">%s</h1>
<p style="color:#8b949e;max-width:520px;margin:12px auto">%s</p>
<p style="color:#8b949e">Pode fechar esta janela e voltar ao painel.</p>
%s</body></html>`,
		titulo, cor, titulo, htmlSeguro(mensagem),
		mapaDeFechamento(ok))
}

func mapaDeFechamento(ok bool) string {
	if !ok {
		return ""
	}
	return `<script>setTimeout(function(){ window.close(); }, 2500);</script>`
}

// htmlSeguro escapa o que vai para a página.
//
// A mensagem carrega texto vindo do Google e do banco. Interpolar isso em HTML sem escapar
// é o caminho clássico para uma injeção — aqui, numa página que o administrador abre logo
// depois de autorizar.
func htmlSeguro(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// enderecoAceitoPeloGoogle diz se o retorno serve como URI de redirecionamento.
//
// HTTPS sempre; HTTP só em localhost, que é a exceção que o próprio Google abre — ali não há
// rede entre o navegador e o servidor para alguém interceptar.
func enderecoAceitoPeloGoogle(endereco string) bool {
	if strings.HasPrefix(endereco, "https://") {
		return true
	}
	if !strings.HasPrefix(endereco, "http://") {
		return false
	}
	resto := strings.TrimPrefix(endereco, "http://")
	hospedeiro, _, _ := strings.Cut(resto, "/")
	if h, _, err := net.SplitHostPort(hospedeiro); err == nil {
		hospedeiro = h
	}
	return hospedeiro == "localhost" || hospedeiro == "127.0.0.1" || hospedeiro == "[::1]"
}
