package sync

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"vodmanager/internal/ingest"
	"vodmanager/internal/store"
	"vodmanager/internal/tmdb"
)

// Classificar o acervo por gênero, usando o TMDB.
//
// # O problema
//
// Uma fonte de IPTV entrega o filme e o nome do filme. Ela não entrega gênero — e a
// "categoria" que ela declara é a pasta que AQUELA fonte escolheu, que muda de fonte para
// fonte e frequentemente é só "Filmes". O resultado é um acervo com milhares de títulos numa
// pasta só, que ninguém consegue navegar.
//
// # Por que o TMDB, e não adivinhar pelo nome
//
// Gênero é informação catalogada por gente. Deduzir do título produziria "Guerra dos Mundos"
// em Guerra e "O Sexto Sentido" em Documentário — e o erro seria invisível, porque uma pasta
// com o filme errado dentro parece uma pasta certa.
//
// # O ritmo
//
// Devagar de propósito. São milhares de títulos, e cada um é uma requisição a um serviço
// alheio e gratuito. Correr atrás de terminar em cinco minutos é o caminho para levar
// bloqueio e não terminar nunca.

// pausaEntreConsultas espaça as requisições ao TMDB.
//
// O limite público é generoso, mas ele é de quem hospeda — e nós somos convidados. Quatro por
// segundo classificam seis mil títulos em vinte e cinco minutos, que é rápido o bastante para
// uma tarefa que se faz uma vez.
const pausaEntreConsultas = 250 * time.Millisecond

// loteDaClassificacao é quantos títulos são lidos do banco por vez.
const loteDaClassificacao = 500

// Andamento é o estado da classificação, para a tela acompanhar.
type Andamento struct {
	Rodando       bool       `json:"rodando"`
	Processados   int        `json:"processados"`
	Classificados int        `json:"classificados"`
	SemResultado  int        `json:"sem_resultado"`
	Falhas        int        `json:"falhas"`
	Erro          string     `json:"erro"`
	InicioEm      time.Time  `json:"inicio_em"`
	FimEm         *time.Time `json:"fim_em"`
}

// Categorizador classifica conteúdos sem pasta usando o TMDB.
type Categorizador struct {
	store *store.Store
	log   *slog.Logger
	// chaveDoAmbiente e a configurada por variavel de ambiente, usada quando o painel nao
	// tem nenhuma. Ela existia primeiro, e tirar o suporte quebraria quem ja a configurou.
	chaveDoAmbiente  string
	idiomaDoAmbiente string

	mu        sync.Mutex
	andamento Andamento
	parar     context.CancelFunc
}

// NovoCategorizador cria o serviço. Cliente nulo (sem chave) é aceito: o Iniciar recusa com
// uma mensagem que diz o que fazer, em vez de o construtor falhar na partida do sistema.
func NovoCategorizador(st *store.Store, chave, idioma string, log *slog.Logger) *Categorizador {
	return &Categorizador{store: st, log: log, chaveDoAmbiente: chave, idiomaDoAmbiente: idioma}
}

// cliente monta o cliente do TMDB na hora de usar, e nao na partida do sistema.
//
// Assim trocar a chave pelo painel vale na proxima classificacao, sem reiniciar o servico. O
// painel vence o ambiente: quem esta olhando a tela e quem acabou de decidir.
func (c *Categorizador) cliente(ctx context.Context) *tmdb.Cliente {
	chave := c.chaveDoAmbiente
	if v, err := c.store.GetSetting(ctx, store.SettingTMDBAPIKey, ""); err == nil && v != "" {
		chave = v
	}
	idioma := c.idiomaDoAmbiente
	if v, err := c.store.GetSetting(ctx, store.SettingTMDBIdioma, ""); err == nil && v != "" {
		idioma = v
	}
	return tmdb.Novo(chave, idioma)
}

// TemChave diz se a classificacao pode ser usada. A chave em si nunca sai daqui.
func (c *Categorizador) TemChave(ctx context.Context) bool {
	return c.cliente(ctx) != nil
}

// Andamento devolve o estado atual.
func (c *Categorizador) Andamento() Andamento {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.andamento
}

// Parar interrompe uma classificação em curso.
//
// O que já foi classificado FICA. Cada título é uma decisão independente e completa — não há
// meio-estado a desfazer, e desfazer seria jogar fora trabalho bom.
func (c *Categorizador) Parar() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.parar != nil {
		c.parar()
	}
}

// Iniciar começa a classificação em segundo plano.
//
// Devolve erro quando não dá para começar — sem chave, ou já rodando. Erros durante a
// execução ficam no andamento, e não aqui: quem chamou já foi embora quando eles acontecem.
func (c *Categorizador) Iniciar(tipo string) error {
	cli := c.cliente(context.Background())
	if cli == nil {
		return tmdb.ErrSemChave
	}

	c.mu.Lock()
	if c.andamento.Rodando {
		c.mu.Unlock()
		return errors.New("já existe uma classificação em andamento")
	}
	ctx, cancelar := context.WithCancel(context.Background())
	c.parar = cancelar
	c.andamento = Andamento{Rodando: true, InicioEm: time.Now()}
	c.mu.Unlock()

	go c.rodar(ctx, cli, tipo)
	return nil
}

func (c *Categorizador) rodar(ctx context.Context, cli *tmdb.Cliente, tipo string) {
	defer func() {
		c.mu.Lock()
		agora := time.Now()
		c.andamento.Rodando = false
		c.andamento.FimEm = &agora
		c.parar = nil
		c.mu.Unlock()
	}()

	// As pastas de gênero, criadas sob demanda e reaproveitadas.
	//
	// Em memória porque são poucas — duas dezenas — e consultadas uma vez por título. Buscar
	// no banco a cada um seriam milhares de consultas para receber sempre a mesma resposta.
	pastas := map[string]int64{}

	for {
		if ctx.Err() != nil {
			return
		}
		lote, err := c.store.ConteudosSemCategoria(ctx, tipo, loteDaClassificacao)
		if err != nil {
			c.anotarErro("falha ao ler a fila: " + err.Error())
			return
		}
		if len(lote) == 0 {
			return // acabou
		}

		// Um lote sem NENHUMA classificação significa que todos falharam ou não foram
		// encontrados — e como a consulta busca sempre os mesmos primeiros sem categoria,
		// insistir produziria um laço infinito consumindo a cota do TMDB.
		antes := c.contarClassificados()

		for i := range lote {
			if ctx.Err() != nil {
				return
			}
			c.classificarUm(ctx, cli, &lote[i], pastas)
			time.Sleep(pausaEntreConsultas)
		}

		if c.contarClassificados() == antes {
			c.log.Info("classificação encerrada: o lote inteiro ficou sem resultado",
				"tamanho", len(lote))
			return
		}
	}
}

func (c *Categorizador) classificarUm(ctx context.Context, cli *tmdb.Cliente,
	item *store.SemCategoria, pastas map[string]int64) {

	serie := item.Tipo == store.ContentSeries

	var filme tmdb.Filme
	var err error
	if item.TMDBID != nil && *item.TMDBID != "" {
		// O caminho exato: a fonte já disse qual é o filme. Uma requisição, sem chute.
		filme, err = cli.PorID(ctx, *item.TMDBID, serie)
	} else {
		ano := 0
		if item.Ano != nil {
			ano = *item.Ano
		}
		// O título limpo, e não o declarado: "- 007 (1969) [DUB]" não encontra nada. A
		// limpeza é a mesma que o resto do sistema já usa para comparar títulos.
		filme, err = cli.Buscar(ctx, ingest.ChaveDeDuplicata(item.Titulo), ano, serie)
	}

	if err != nil {
		c.mu.Lock()
		c.andamento.Processados++
		if errors.Is(err, tmdb.ErrNaoEncontrado) {
			c.andamento.SemResultado++
		} else {
			c.andamento.Falhas++
			c.andamento.Erro = err.Error()
		}
		c.mu.Unlock()
		return
	}

	if len(filme.Generos) == 0 {
		c.mu.Lock()
		c.andamento.Processados++
		c.andamento.SemResultado++
		c.mu.Unlock()
		return
	}

	// O PRIMEIRO gênero, e um só.
	//
	// O TMDB devolve vários, em ordem de relevância, e um filme numa pasta só é o que faz o
	// catálogo navegável. Espalhar o mesmo título por quatro pastas devolve o problema que
	// isto veio resolver, com outro nome.
	nome := filme.Generos[0]
	id, err := c.pastaDoGenero(ctx, nome, item.Tipo, pastas)
	if err != nil {
		c.mu.Lock()
		c.andamento.Processados++
		c.andamento.Falhas++
		c.andamento.Erro = err.Error()
		c.mu.Unlock()
		return
	}

	if err := c.store.DefinirCategoriaDoConteudo(ctx, item.ID, id); err != nil {
		c.mu.Lock()
		c.andamento.Processados++
		c.andamento.Falhas++
		c.andamento.Erro = err.Error()
		c.mu.Unlock()
		return
	}

	c.mu.Lock()
	c.andamento.Processados++
	c.andamento.Classificados++
	c.mu.Unlock()
}

// pastaDoGenero devolve a categoria daquele gênero, criando na primeira vez.
//
// A pasta nasce marcada como PRINCIPAL: sem isso ela não seria usada pela sincronização, e os
// títulos classificados hoje voltariam a ficar sem pasta na próxima passagem.
func (c *Categorizador) pastaDoGenero(ctx context.Context, genero, tipo string,
	pastas map[string]int64) (int64, error) {

	chave := tipo + "|" + genero
	if id, ok := pastas[chave]; ok {
		return id, nil
	}
	normalizado := strings.ToLower(strings.TrimSpace(genero))
	id, err := c.store.CriarPrincipal(ctx, genero, normalizado, tipo)
	if err != nil {
		return 0, err
	}
	pastas[chave] = id
	return id, nil
}

func (c *Categorizador) contarClassificados() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.andamento.Classificados
}

func (c *Categorizador) anotarErro(msg string) {
	c.mu.Lock()
	c.andamento.Erro = msg
	c.mu.Unlock()
}
