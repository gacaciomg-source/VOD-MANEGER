// Comando vodm-dev: sobe o VOD Manager completo em uma única linha, para desenvolvimento.
//
// Ele baixa e executa um PostgreSQL de verdade (sem Docker, sem instalação no sistema),
// gera e guarda a chave de cifra, e então sobe EXATAMENTE a mesma aplicação do binário
// de produção — a montagem vive em internal/app e é compartilhada pelos dois.
//
// NÃO use em produção: o Postgres embutido guarda os dados numa pasta local do projeto e
// as credenciais do banco são fixas e conhecidas.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"vodmanager/internal/app"
	"vodmanager/internal/config"
	"vodmanager/internal/cryptobox"
)

const (
	// dirDados é onde ficam o Postgres embutido e a chave de cifra do ambiente de
	// desenvolvimento. Está no .gitignore.
	dirDados = ".vodm-dev"
	// portaPadrao do Postgres de desenvolvimento. Diferente da 5432 para não colidir
	// com uma instalação existente na máquina.
	portaPadrao = 55432
	usuarioDB   = "vodm"
	senhaDB     = "vodm"
	nomeDB      = "vodm"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "\nerro:", err)
		os.Exit(1)
	}
}

func run() error {
	raiz, err := raizDoProjeto()
	if err != nil {
		return err
	}
	base := filepath.Join(raiz, dirDados)

	// Os três caminhos precisam ser IRMÃOS, não aninhados: o embedded-postgres apaga o
	// RuntimePath inteiro a cada partida. Se os binários ou os dados morassem dentro
	// dele, seriam destruídos junto — os binários antes mesmo da extração terminar, e o
	// banco a cada restart.
	dirBinarios := filepath.Join(base, "pg-bin")
	dirDadosPG := filepath.Join(base, "pg-data")
	dirRuntime := filepath.Join(base, "pg-run")

	for _, dir := range []string{base, dirBinarios, dirDadosPG} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("criando %s: %w", dir, err)
		}
	}

	porta := portaPadrao
	if v := os.Getenv("VODM_DEV_DB_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			porta = n
		}
	}
	// Um encerramento abrupto (kill -9, fechar o terminal, a IDE matando o processo)
	// deixa o Postgres órfão segurando a porta e o diretório de dados.
	//
	// Como este é um ambiente de DESENVOLVIMENTO e o servidor órfão é comprovadamente
	// nosso — o PID vem do postmaster.pid deste diretório de dados —, encerrá-lo
	// automaticamente é seguro e evita a fricção de pedir um comando manual a cada
	// reinício. Nunca tocamos num Postgres que não seja deste projeto.
	if ocupada(porta) {
		pid := pidDoPostgres(dirDadosPG)
		if pid == "" {
			return fmt.Errorf("a porta %d já está em uso e não parece ser um Postgres desta pasta.\n"+
				"Há outro vodm-dev rodando? Encerre-o, ou use VODM_DEV_DB_PORT com outra porta", porta)
		}

		fmt.Printf("Encontrei um PostgreSQL desta pasta ainda rodando (PID %s), de uma\n"+
			"execução anterior encerrada abruptamente. Encerrando…\n", pid)

		// O código de saída do encerrador não é confiável: o taskkill do Windows retorna
		// erro quando algum processo filho já havia terminado sozinho, mesmo tendo
		// encerrado o resto com sucesso. O que vale é o resultado — a porta ficou livre?
		erroKill := encerrarProcesso(pid)
		if !esperarPortaLivre(porta, 20*time.Second) {
			msg := fmt.Sprintf("a porta %d continuou ocupada após tentar encerrar o PID %s", porta, pid)
			if erroKill != nil {
				msg += fmt.Sprintf("\nO encerramento reportou: %v", erroKill)
			}
			return fmt.Errorf("%s\n\nEncerre-o manualmente com:\n  taskkill /PID %s /T /F", msg, pid)
		}
		fmt.Println("Pronto, a porta foi liberada.")
	}

	chave, err := carregarOuGerarChave(filepath.Join(base, "encryption.key"))
	if err != nil {
		return err
	}

	fmt.Println("┌─ VOD Manager — modo desenvolvimento")
	fmt.Println("│")
	fmt.Printf("│  Preparando PostgreSQL embutido em %s\n", dirDados)
	fmt.Println("│  (na primeira vez isso baixa ~100 MB e pode levar alguns minutos)")

	pg := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Username(usuarioDB).
			Password(senhaDB).
			Database(nomeDB).
			Port(uint32(porta)).
			RuntimePath(dirRuntime).
			DataPath(dirDadosPG).
			BinariesPath(dirBinarios).
			Logger(os.Stdout).
			StartTimeout(5 * time.Minute),
	)
	if err := pg.Start(); err != nil {
		return fmt.Errorf("subindo o PostgreSQL embutido: %w", err)
	}
	defer func() {
		fmt.Println("│  encerrando o PostgreSQL embutido...")
		if err := pg.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "aviso: falha ao parar o Postgres:", err)
		}
	}()

	dsn := fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable",
		usuarioDB, senhaDB, porta, nomeDB)

	// A configuração é montada aqui e passada pelo mesmo Load do binário de produção:
	// assim qualquer validação que vale lá vale aqui também.
	ambiente := map[string]string{
		"VODM_DATABASE_URL":             dsn,
		"VODM_ENCRYPTION_KEY":           chave,
		"VODM_HTTP_ADDR":                valorOuPadrao("VODM_HTTP_ADDR", ":8080"),
		"VODM_ROLE":                     valorOuPadrao("VODM_ROLE", "all"),
		"VODM_NODE_ID":                  valorOuPadrao("VODM_NODE_ID", "dev"),
		"VODM_LOG_FORMAT":               valorOuPadrao("VODM_LOG_FORMAT", "text"),
		"VODM_LOG_LEVEL":                valorOuPadrao("VODM_LOG_LEVEL", "info"),
		"VODM_BOOTSTRAP_ADMIN_USERNAME": valorOuPadrao("VODM_BOOTSTRAP_ADMIN_USERNAME", "admin"),
		"VODM_BOOTSTRAP_ADMIN_PASSWORD": valorOuPadrao("VODM_BOOTSTRAP_ADMIN_PASSWORD", "admin-desenvolvimento"),
	}
	cfg, err := config.LoadFrom(func(k string) string { return ambiente[k] })
	if err != nil {
		return err
	}

	endereco := cfg.HTTPAddr
	if strings.HasPrefix(endereco, ":") {
		endereco = "localhost" + endereco
	}

	fmt.Println("│")
	fmt.Println("│  Pronto.")
	fmt.Printf("│    API......: http://%s\n", endereco)
	fmt.Printf("│    Usuário..: %s\n", ambiente["VODM_BOOTSTRAP_ADMIN_USERNAME"])
	fmt.Printf("│    Senha....: %s\n", ambiente["VODM_BOOTSTRAP_ADMIN_PASSWORD"])
	fmt.Println("│")
	fmt.Println("│  Login:")
	fmt.Printf("│    curl -c cookies.txt -X POST http://%s/api/v1/auth/login \\\n", endereco)
	fmt.Printf("│      -H \"Content-Type: application/json\" \\\n")
	fmt.Printf("│      -d '{\"username\":\"%s\",\"password\":\"%s\"}'\n",
		ambiente["VODM_BOOTSTRAP_ADMIN_USERNAME"], ambiente["VODM_BOOTSTRAP_ADMIN_PASSWORD"])
	fmt.Println("│")
	fmt.Println("│  Depois, usando o cookie salvo:")
	fmt.Printf("│    curl -b cookies.txt http://%s/api/v1/stats/dashboard\n", endereco)
	fmt.Println("│")
	fmt.Println("│  Ctrl+C encerra tudo. Os dados ficam em " + dirDados + " e sobrevivem ao restart.")
	fmt.Println("└─")
	fmt.Println()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return app.Run(ctx, cfg, "dev")
}

// carregarOuGerarChave mantém a MESMA chave entre execuções.
//
// Gerar uma chave nova a cada boot tornaria ilegíveis as credenciais de fonte já
// gravadas — o administrador teria que recadastrar tudo a cada restart.
func carregarOuGerarChave(caminho string) (string, error) {
	if dados, err := os.ReadFile(caminho); err == nil {
		chave := strings.TrimSpace(string(dados))
		if _, err := cryptobox.ParseKey(chave); err == nil {
			return chave, nil
		}
		fmt.Fprintln(os.Stderr, "aviso: a chave em", caminho, "é inválida; gerando uma nova")
	}

	chave, err := cryptobox.GenerateKey()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(caminho, []byte(chave+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("gravando a chave em %s: %w", caminho, err)
	}
	return chave, nil
}

// raizDoProjeto sobe diretórios até achar o go.mod, para que o comando funcione mesmo
// quando executado de dentro de uma subpasta.
func raizDoProjeto() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		pai := filepath.Dir(dir)
		if pai == dir {
			break
		}
		dir = pai
	}
	return "", fmt.Errorf("não encontrei o go.mod — rode este comando de dentro da pasta do projeto")
}

// pidDoPostgres lê o PID do postmaster.pid, se houver um servidor desta pasta rodando.
func pidDoPostgres(dirDados string) string {
	dados, err := os.ReadFile(filepath.Join(dirDados, "postmaster.pid"))
	if err != nil {
		return ""
	}
	linhas := strings.SplitN(strings.TrimSpace(string(dados)), "\n", 2)
	if len(linhas) == 0 {
		return ""
	}
	return strings.TrimSpace(linhas[0])
}

// encerrarProcesso mata um processo pelo PID, no jeito de cada sistema.
func encerrarProcesso(pid string) error {
	n, err := strconv.Atoi(pid)
	if err != nil {
		return fmt.Errorf("PID inválido: %q", pid)
	}
	if runtime.GOOS == "windows" {
		// O postmaster ignora sinais do Go no Windows; taskkill /T encerra a árvore
		// inteira, incluindo os processos filhos que o Postgres cria.
		saida, err := exec.Command("taskkill", "/PID", pid, "/T", "/F").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(saida)))
		}
		return nil
	}
	proc, err := os.FindProcess(n)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// esperarPortaLivre aguarda o sistema operacional liberar a porta.
func esperarPortaLivre(porta int, limite time.Duration) bool {
	prazo := time.Now().Add(limite)
	for time.Now().Before(prazo) {
		if !ocupada(porta) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func ocupada(porta int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", porta))
	if err != nil {
		return true
	}
	_ = l.Close()
	return false
}

func valorOuPadrao(chave, padrao string) string {
	if v := strings.TrimSpace(os.Getenv(chave)); v != "" {
		return v
	}
	return padrao
}
