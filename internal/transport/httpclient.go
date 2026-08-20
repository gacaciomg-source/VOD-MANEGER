// Package transport é a camada que fala HTTP com as fontes.
//
// É a ÚNICA camada autorizada a usar credenciais e a materializar URLs de mídia. Os
// pacotes de parsing (internal/sources/...) permanecem puros e sequer podem importar
// net/http — há teste estrutural garantindo isso.
package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"vodmanager/internal/ingest"
	"vodmanager/internal/sources"
)

// maxBodyBytes limita a resposta de um endpoint de CATÁLOGO. Catálogos grandes chegam a
// centenas de MB, então o limite é generoso; ele existe para que uma fonte defeituosa
// não consuma disco/memória sem fim.
const maxBodyBytes = 1 << 30 // 1 GiB

// Client é um cliente HTTP com teto de requisições por execução.
//
// O teto é o mecanismo que protege a fonte: atingi-lo marca a run como parcial em vez de
// disparar requisições sem limite (docs/07 §6.1).
type Client struct {
	http      *http.Client
	userAgent string
	// idleTimeout é quanto tempo a leitura pode ficar SEM PROGRESSO antes de desistir.
	// Diferente de um prazo total, ele não pune uma transferência longa e saudável.
	idleTimeout time.Duration

	mu       sync.Mutex
	budget   int
	used     int
	exceeded bool
}

// NewClient cria o cliente para uma execução de sincronização.
func NewClient(timeout time.Duration, budget int, userAgent string) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if budget <= 0 {
		budget = 1000
	}
	if userAgent == "" {
		userAgent = "VODManager/1.0"
	}
	return &Client{
		http: &http.Client{
			// SEM Timeout global de propósito.
			//
			// http.Client.Timeout cobre a requisição INTEIRA, incluindo a leitura do
			// corpo. Uma playlist M3U de centenas de MB leva minutos para baixar, e um
			// prazo total cortava a sincronização no meio — o erro visto em produção foi
			// exatamente "context deadline exceeded while reading body", com 8.041 de
			// 13.000 itens já processados.
			//
			// O que protege contra uma fonte travada é o prazo para RESPONDER
			// (ResponseHeaderTimeout) e o prazo sem PROGRESSO na leitura (idleReadTimeout),
			// não um teto para a transferência inteira.
			Transport: &http.Transport{
				// Keep-alive por host: evita repetir DNS + TCP + TLS a cada requisição
				// de catálogo, que é o grosso do custo numa fonte com muitas séries.
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:          10,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       60 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: timeout,
				ExpectContinueTimeout: 5 * time.Second,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("cadeia de redirecionamentos longa demais")
				}
				return nil
			},
		},
		userAgent:   userAgent,
		budget:      budget,
		idleTimeout: timeout,
	}
}

// take consome uma unidade do orçamento.
func (c *Client) take() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used >= c.budget {
		c.exceeded = true
		return sources.ErrBudgetExceeded
	}
	c.used++
	return nil
}

// Used devolve quantas requisições foram feitas.
func (c *Client) Used() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// Exceeded informa se o teto foi atingido em algum momento.
func (c *Client) Exceeded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exceeded
}

// Remaining devolve quantas requisições ainda cabem no orçamento.
func (c *Client) Remaining() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used >= c.budget {
		return 0
	}
	return c.budget - c.used
}

// Open faz um GET e devolve o corpo em streaming, sem carregar tudo em memória.
//
// O chamador é responsável por fechar o corpo devolvido.
func (c *Client) Open(ctx context.Context, url string) (io.ReadCloser, error) {
	if err := c.take(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("montando requisição para %s: %w", ingest.RedactURL(url), err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := c.http.Do(req)
	if err != nil {
		// A URL entra redigida no erro: ela pode conter credencial na query.
		return nil, fmt.Errorf("consultando %s: %w", ingest.RedactURL(url), redactErr(err))
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("consultando %s: a fonte respondeu %s",
			ingest.RedactURL(url), resp.Status)
	}
	corpo := &limitedBody{ReadCloser: resp.Body, remaining: maxBodyBytes}
	return &idleTimeoutBody{ReadCloser: corpo, limite: c.idleTimeout}, nil
}

// idleTimeoutBody aborta a leitura quando a fonte para de enviar dados.
//
// É a proteção correta para transferências grandes: uma playlist de 300 MB pode levar
// minutos legitimamente, mas ficar 60 segundos sem receber um único byte significa que a
// fonte travou.
type idleTimeoutBody struct {
	io.ReadCloser
	limite time.Duration
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	if b.limite <= 0 {
		return b.ReadCloser.Read(p)
	}

	type resultado struct {
		n   int
		err error
	}
	ch := make(chan resultado, 1)
	go func() {
		n, err := b.ReadCloser.Read(p)
		ch <- resultado{n, err}
	}()

	temporizador := time.NewTimer(b.limite)
	defer temporizador.Stop()

	select {
	case r := <-ch:
		return r.n, r.err
	case <-temporizador.C:
		// Fechar o corpo faz a leitura pendente retornar, liberando a goroutine.
		b.ReadCloser.Close()
		return 0, fmt.Errorf("a fonte ficou %s sem enviar dados", b.limite)
	}
}

// Get faz um GET e devolve o corpo inteiro. Use apenas em respostas de tamanho
// previsível (JSON de API); para playlists, use Open.
func (c *Client) Get(ctx context.Context, url string) ([]byte, error) {
	body, err := c.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("lendo resposta de %s: %w", ingest.RedactURL(url), redactErr(err))
	}
	return data, nil
}

// limitedBody impede que uma resposta sem fim consuma toda a memória.
type limitedBody struct {
	io.ReadCloser
	remaining int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, fmt.Errorf("resposta da fonte excedeu o limite de %d bytes", int64(maxBodyBytes))
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.ReadCloser.Read(p)
	l.remaining -= int64(n)
	return n, err
}

// redactErr remove credenciais de mensagens de erro da stdlib, que costumam repetir a
// URL completa.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	limpo := ingest.RedactString(err.Error())
	if limpo == err.Error() {
		return err
	}
	return fmt.Errorf("%s", limpo)
}
