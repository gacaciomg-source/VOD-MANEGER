package armazenamento

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Google Drive como destino do acervo.
//
// # As três operações, e o que cada uma tem de particular
//
// GRAVAR usa "upload retomável" em pedaços. É a única forma que funciona aqui, porque o
// tamanho do arquivo NÃO é conhecido quando a gravação começa: os bytes estão chegando de
// uma fonte, e ela raramente anuncia o total. O upload simples exigiria Content-Length, e
// para tê-lo seria preciso guardar o arquivo inteiro em disco antes de começar — que é
// exatamente o que não há espaço para fazer quando o destino é a nuvem justamente por falta
// de disco.
//
// O efeito prático: um filme de 100 GB atravessa uma máquina com 30 GB livres sem nunca
// tocar o disco. O que ocupa memória é um pedaço de cada vez.
//
// LER usa Range. Sem isso, pular para o meio do filme exigiria baixar tudo o que vem antes,
// e o espectador esperaria minutos por um clique na barra de progresso.
//
// MEDIR pergunta a cota da conta, e é o que permite ao sistema escolher outra conta quando
// esta encheu — em vez de descobrir isso no meio de uma gravação.

const (
	driveArquivos = "https://www.googleapis.com/drive/v3/files"
	driveUpload   = "https://www.googleapis.com/upload/drive/v3/files"
	driveSobre    = "https://www.googleapis.com/drive/v3/about"
	googleToken   = "https://oauth2.googleapis.com/token"
)

// pedacoDeUpload é quanto vai por vez.
//
// O Google exige múltiplo de 256 KiB em todo pedaço que não seja o último. 16 MiB é o
// equilíbrio: pedaços pequenos multiplicam idas e voltas num arquivo de dezenas de
// gigabytes, e pedaços grandes custam memória por gravação simultânea — dez uploads ao
// mesmo tempo com 16 MiB são 160 MB, o que uma VPS modesta absorve.
const pedacoDeUpload = 16 << 20

// margemDoToken é quanto antes do vencimento o token é renovado.
//
// Um token que vence no meio de uma leitura de duas horas produziria uma falha no meio do
// filme. Renovar com folga é mais barato que tratar isso.
const margemDoToken = 5 * time.Minute

// GDrive é uma conta do Google Drive.
type GDrive struct {
	nome         string
	clientID     string
	clientSecret string
	refreshToken string
	pastaRaiz    string

	http *http.Client

	mu      sync.Mutex
	token   string
	venceEm time.Time
}

// CredenciaisGDrive é o que o cadastro guardou.
type CredenciaisGDrive struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

// NovoGDrive monta o backend de uma conta.
//
// Não fala com o Google aqui: montar precisa ser barato, porque acontece na primeira
// reprodução que tocar nesta conta. A primeira operação de verdade é que busca o token.
func NovoGDrive(nome string, cred CredenciaisGDrive, pastaRaiz string) (*GDrive, error) {
	if cred.ClientID == "" || cred.ClientSecret == "" || cred.RefreshToken == "" {
		return nil, errors.New("credenciais do Google Drive incompletas")
	}
	return &GDrive{
		nome:         nome,
		clientID:     cred.ClientID,
		clientSecret: cred.ClientSecret,
		refreshToken: cred.RefreshToken,
		pastaRaiz:    strings.TrimSpace(pastaRaiz),
		http: &http.Client{
			// SEM prazo total: um upload de 40 GB leva horas. O que protege é o prazo
			// para o Google RESPONDER, não para a transferência terminar.
			Transport: &http.Transport{
				MaxIdleConns:          20,
				MaxIdleConnsPerHost:   8,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
	}, nil
}

// Nome identifica o backend. Todas as contas de nuvem respondem "nuvem": qual delas é
// dito pela coluna nuvem_id, e não pelo tipo do backend.
func (g *GDrive) Nome() string { return "nuvem" }

// acessar devolve um token de acesso válido, renovando quando preciso.
func (g *GDrive) acessar(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.token != "" && time.Now().Before(g.venceEm.Add(-margemDoToken)) {
		return g.token, nil
	}

	form := url.Values{
		"client_id":     {g.clientID},
		"client_secret": {g.clientSecret},
		"refresh_token": {g.refreshToken},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleToken,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("renovando o acesso à conta %q: %w", g.nome, err)
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode != http.StatusOK {
		// invalid_grant significa que a autorização foi revogada ou a conta trocou de
		// senha. Dizer isso separa "problema momentâneo" de "precisa cadastrar de novo".
		if strings.Contains(string(corpo), "invalid_grant") {
			return "", fmt.Errorf(
				"a autorização da conta %q não vale mais. Remova a conta e autorize de novo pelo Acervo", g.nome)
		}
		return "", fmt.Errorf("o Google recusou a renovação da conta %q: %s", g.nome, resp.Status)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(corpo, &payload); err != nil || payload.AccessToken == "" {
		return "", fmt.Errorf("resposta inesperada do Google ao renovar a conta %q", g.nome)
	}

	g.token = payload.AccessToken
	segundos := payload.ExpiresIn
	if segundos <= 0 {
		segundos = 3600
	}
	g.venceEm = time.Now().Add(time.Duration(segundos) * time.Second)
	return g.token, nil
}

// Guardar envia o conteúdo em pedaços, sem conhecer o tamanho de antemão.
func (g *GDrive) Guardar(ctx context.Context, sugestao string, conteudo io.Reader, bytesEsperados int64) (Localizacao, error) {
	sessao, err := g.abrirSessaoDeUpload(ctx, nomeSeguro(sugestao))
	if err != nil {
		return Localizacao{}, err
	}

	buf := make([]byte, pedacoDeUpload)
	var enviados int64

	for {
		// Lê um pedaço INTEIRO antes de enviar. io.ReadFull junta as leituras curtas que
		// uma conexão de rede produz o tempo todo — sem isso, um pedaço de 300 bytes seria
		// enviado como se fosse o último, e o Google encerraria o upload ali.
		n, errLeitura := io.ReadFull(conteudo, buf)
		ultimo := errors.Is(errLeitura, io.EOF) || errors.Is(errLeitura, io.ErrUnexpectedEOF)
		if errLeitura != nil && !ultimo {
			g.cancelarSessao(sessao)
			return Localizacao{}, fmt.Errorf("lendo o conteúdo para enviar: %w", errLeitura)
		}

		if n == 0 && ultimo {
			// O arquivo terminou EXATAMENTE na fronteira de um pedaço.
			//
			// É raro e acontece: um arquivo de tamanho múltiplo de 16 MiB. Ainda assim é
			// preciso fechar o upload anunciando o total — o Google não o infere — e é essa
			// última chamada que devolve o id.
			id, err := g.fecharUpload(ctx, sessao, enviados)
			if err != nil {
				return Localizacao{}, err
			}
			return Localizacao{Localizador: id, Bytes: enviados}, nil
		}

		id, err := g.enviarPedaco(ctx, sessao, buf[:n], enviados, ultimo)
		if err != nil {
			g.cancelarSessao(sessao)
			return Localizacao{}, err
		}
		enviados += int64(n)

		if ultimo {
			return Localizacao{Localizador: id, Bytes: enviados}, nil
		}
	}
}

// abrirSessaoDeUpload pede ao Google um endereço para receber os pedaços.
func (g *GDrive) abrirSessaoDeUpload(ctx context.Context, nome string) (string, error) {
	token, err := g.acessar(ctx)
	if err != nil {
		return "", err
	}

	meta := map[string]any{"name": nome}
	if g.pastaRaiz != "" {
		meta["parents"] = []string{g.pastaRaiz}
	}
	corpo, _ := json.Marshal(meta)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		driveUpload+"?uploadType=resumable&supportsAllDrives=true", bytes.NewReader(corpo))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("abrindo o envio na conta %q: %w", g.nome, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("a conta %q recusou o envio: %s", g.nome, resp.Status)
	}
	destino := resp.Header.Get("Location")
	if destino == "" {
		return "", fmt.Errorf("a conta %q não devolveu o endereço de envio", g.nome)
	}
	return destino, nil
}

// enviarPedaco manda um bloco. Devolve o id do arquivo quando é o último.
func (g *GDrive) enviarPedaco(ctx context.Context, sessao string, dados []byte, deslocamento int64, ultimo bool) (string, error) {
	total := "*"
	if ultimo {
		total = strconv.FormatInt(deslocamento+int64(len(dados)), 10)
	}
	faixa := fmt.Sprintf("bytes %d-%d/%s",
		deslocamento, deslocamento+int64(len(dados))-1, total)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessao, bytes.NewReader(dados))
	if err != nil {
		return "", err
	}
	// A sessão de upload já carrega a autorização na própria URL; repetir o cabeçalho é
	// desnecessário e o Google recusa em alguns casos.
	req.Header.Set("Content-Range", faixa)
	req.ContentLength = int64(len(dados))

	resp, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("enviando pedaço para a conta %q: %w", g.nome, err)
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	switch {
	case resp.StatusCode == 308:
		// "Resume Incomplete": é a resposta normal a todo pedaço que não é o último.
		if ultimo {
			return "", fmt.Errorf("a conta %q não fechou o envio", g.nome)
		}
		return "", nil
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		var payload struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(corpo, &payload); err != nil || payload.ID == "" {
			return "", fmt.Errorf("a conta %q não devolveu o id do arquivo", g.nome)
		}
		return payload.ID, nil
	case resp.StatusCode == http.StatusForbidden && bytes.Contains(corpo, []byte("quota")):
		return "", fmt.Errorf("%w: a conta %q está sem espaço", ErrSemEspaco, g.nome)
	default:
		return "", fmt.Errorf("a conta %q respondeu %s ao pedaço", g.nome, resp.Status)
	}
}

// fecharUpload encerra um upload cujo último pedaço caiu exatamente na fronteira.
//
// Devolve o id, como qualquer outro fechamento: sem isso, o arquivo ficaria gravado no
// Drive e o sistema não saberia onde ele está — espaço ocupado que ninguém encontra.
func (g *GDrive) fecharUpload(ctx context.Context, sessao string, total int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessao, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", total))

	resp, err := g.http.Do(req)
	if err != nil {
		g.cancelarSessao(sessao)
		return "", fmt.Errorf("fechando o envio na conta %q: %w", g.nome, err)
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		g.cancelarSessao(sessao)
		return "", fmt.Errorf("a conta %q respondeu %s ao fechar o envio", g.nome, resp.Status)
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(corpo, &payload); err != nil || payload.ID == "" {
		return "", fmt.Errorf("a conta %q não devolveu o id do arquivo", g.nome)
	}
	return payload.ID, nil
}

// cancelarSessao descarta um upload interrompido.
//
// Sem isto, um envio que falha na metade deixa os pedaços já recebidos ocupando espaço na
// conta — invisíveis no Drive e contando na cota, que é a pior combinação possível.
func (g *GDrive) cancelarSessao(sessao string) {
	ctx, cancelar := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelar()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, sessao, nil)
	if err != nil {
		return
	}
	if resp, err := g.http.Do(req); err == nil {
		resp.Body.Close()
	}
}

// Abrir lê o arquivo a partir de um deslocamento.
func (g *GDrive) Abrir(ctx context.Context, localizador string, deslocamento int64) (io.ReadCloser, error) {
	if localizador == "" {
		return nil, fmt.Errorf("%w: identificador vazio", ErrNaoEncontrado)
	}
	token, err := g.acessar(ctx)
	if err != nil {
		return nil, err
	}

	endereco := driveArquivos + "/" + url.PathEscape(localizador) +
		"?alt=media&supportsAllDrives=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endereco, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if deslocamento > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", deslocamento))
	}

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lendo da conta %q: %w", g.nome, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s na conta %q", ErrNaoEncontrado, localizador, g.nome)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("a conta %q respondeu %s à leitura", g.nome, resp.Status)
	}

	// Pedimos do byte N e viemos do zero.
	//
	// Um 200 em resposta a um pedido com Range significa que a faixa foi IGNORADA: o corpo
	// começa no início do arquivo. Devolvê-lo assim entrega ao player o começo do filme
	// quando ele pediu a continuação — e, como o player confia no que pediu, ele volta ao
	// início. É o mesmo defeito que já corrigimos nas fontes, e que voltou por este caminho:
	// o acervo na nuvem, que não tinha essa proteção.
	//
	// Descartar os primeiros N bytes custa uma leitura, e é o que resta a fazer: não há como
	// obrigar o outro lado a respeitar a faixa, e servir do lugar errado é pior que esperar.
	return posicionarSeIgnorouAFaixa(resp.Body, resp.StatusCode, deslocamento, g.nome)
}

// Apagar remove o arquivo. Apagar o que não existe não é erro.
func (g *GDrive) Apagar(ctx context.Context, localizador string) error {
	if localizador == "" {
		return nil
	}
	token, err := g.acessar(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		driveArquivos+"/"+url.PathEscape(localizador)+"?supportsAllDrives=true", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("apagando na conta %q: %w", g.nome, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK ||
		resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("a conta %q respondeu %s ao apagar", g.nome, resp.Status)
}

// Espaco lê a cota da conta.
func (g *GDrive) Espaco(ctx context.Context) (Espaco, error) {
	token, err := g.acessar(ctx)
	if err != nil {
		return Espaco{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		driveSobre+"?fields=storageQuota", nil)
	if err != nil {
		return Espaco{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := g.http.Do(req)
	if err != nil {
		return Espaco{}, fmt.Errorf("medindo a conta %q: %w", g.nome, err)
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return Espaco{}, fmt.Errorf("a conta %q respondeu %s à medição", g.nome, resp.Status)
	}

	var payload struct {
		StorageQuota struct {
			// Vêm como texto porque passam de 2^53 e não cabem num número de JSON.
			Limit string `json:"limit"`
			Usage string `json:"usage"`
		} `json:"storageQuota"`
	}
	if err := json.Unmarshal(corpo, &payload); err != nil {
		return Espaco{}, fmt.Errorf("resposta inesperada ao medir a conta %q", g.nome)
	}

	usado, _ := strconv.ParseInt(payload.StorageQuota.Usage, 10, 64)
	limite, _ := strconv.ParseInt(payload.StorageQuota.Limit, 10, 64)
	if limite <= 0 {
		// Contas empresariais sem limite não devolvem `limit`. Ilimitado é a resposta
		// honesta; zero seria lido como "cheia" e a conta pararia de receber.
		return Espaco{Usado: usado, Ilimitado: true}, nil
	}
	livre := limite - usado
	if livre < 0 {
		livre = 0
	}
	return Espaco{Total: limite, Usado: usado, Livre: livre}, nil
}

// PastaPadrao é o nome da pasta que o sistema cria na conta.
//
// Nome fixo e reconhecível de propósito: quem abrir o Drive precisa entender de onde aqueles
// arquivos vieram sem consultar ninguém.
const PastaPadrao = "VOD Manager"

// GarantirPasta cria a pasta do acervo e devolve o id dela.
//
// # Por que criar, e não pedir que a pessoa informe
//
// O escopo é `drive.file`: o sistema só enxerga o que ele mesmo criou. Uma pasta feita à mão
// na tela do Drive é invisível para ele — colar o id dela produziria erro em toda gravação,
// e o erro não diria "esta pasta não é minha".
//
// Criando aqui, a pasta nasce nossa e passa a funcionar. É a única forma que o escopo
// permite, e a mais segura: continuamos sem enxergar nada que já estava na conta.
//
// # Por que não deixar na raiz
//
// A raiz é onde estão os documentos e fotos de quem cedeu a conta. Despejar centenas de
// filmes ali torna a conta inutilizável para o dono — e a primeira reação, previsivelmente,
// é apagar tudo em massa, levando o acervo junto.
func (g *GDrive) GarantirPasta(ctx context.Context, nome string) (string, error) {
	token, err := g.acessar(ctx)
	if err != nil {
		return "", err
	}

	// Procura antes de criar: reautorizar a mesma conta não pode encher o Drive de pastas
	// repetidas com o mesmo nome.
	//
	// `trashed = false` importa — uma pasta na lixeira ainda aparece na busca, e devolver o
	// id dela faria as gravações irem parar no lixo.
	busca := driveArquivos + "?q=" + url.QueryEscape(
		"mimeType = 'application/vnd.google-apps.folder' and name = '"+
			strings.ReplaceAll(nome, "'", `\'`)+"' and trashed = false") +
		"&fields=files(id)&pageSize=1"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, busca, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("procurando a pasta na conta %q: %w", g.nome, err)
	}
	corpo, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var achado struct {
			Files []struct {
				ID string `json:"id"`
			} `json:"files"`
		}
		if json.Unmarshal(corpo, &achado) == nil && len(achado.Files) > 0 {
			return achado.Files[0].ID, nil
		}
	}

	meta, err := json.Marshal(map[string]any{
		"name":     nome,
		"mimeType": "application/vnd.google-apps.folder",
	})
	if err != nil {
		return "", err
	}
	criar, err := http.NewRequestWithContext(ctx, http.MethodPost,
		driveArquivos+"?fields=id", bytes.NewReader(meta))
	if err != nil {
		return "", err
	}
	criar.Header.Set("Authorization", "Bearer "+token)
	criar.Header.Set("Content-Type", "application/json")

	res, err := g.http.Do(criar)
	if err != nil {
		return "", fmt.Errorf("criando a pasta na conta %q: %w", g.nome, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("a conta %q respondeu %s ao criar a pasta", g.nome, res.Status)
	}
	var nova struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &nova); err != nil || nova.ID == "" {
		return "", fmt.Errorf("resposta inesperada ao criar a pasta na conta %q", g.nome)
	}
	return nova.ID, nil
}

// posicionarSeIgnorouAFaixa conserta um corpo que veio do começo quando pedimos do meio.
//
// Um 200 em resposta a um pedido com Range significa que a faixa foi IGNORADA: o corpo começa
// no início do arquivo. Devolvê-lo assim entrega ao player o começo do filme quando ele pediu
// a continuação — e, como o player confia no que pediu, ele volta ao início e trava sempre no
// mesmo ponto.
//
// É o mesmo defeito que já foi corrigido nas fontes, e que voltou por este caminho: o acervo
// na nuvem, que não tinha essa proteção.
//
// Descartar os primeiros bytes custa uma leitura, e é o que resta a fazer: não há como
// obrigar o outro lado a respeitar a faixa, e servir do lugar errado é pior que esperar.
//
// Função separada para poder ser testada: a decisão é curta e o custo de errar é um filme que
// não passa do meio.
func posicionarSeIgnorouAFaixa(corpo io.ReadCloser, status int, deslocamento int64,
	conta string) (io.ReadCloser, error) {

	if deslocamento <= 0 || status != http.StatusOK {
		return corpo, nil
	}
	if _, err := io.CopyN(io.Discard, corpo, deslocamento); err != nil {
		corpo.Close()
		return nil, fmt.Errorf("a conta %q ignorou a faixa pedida e o arquivo acabou antes "+
			"do byte %d", conta, deslocamento)
	}
	return corpo, nil
}
