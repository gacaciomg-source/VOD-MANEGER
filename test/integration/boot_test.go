package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"vodmanager/internal/cryptobox"
)

// TestBootDoBinario compila e executa o binário de verdade contra um Postgres real,
// exatamente como o `docker compose up` faz: aplica migrações, cria o administrador
// inicial, sobe o HTTP e aceita login.
//
// É o teste que sustenta o critério de aceite da Fase 1. Os demais testes exercitam a
// API montada em processo; este exercita a fiação do main().
func TestBootDoBinario(t *testing.T) {
	if testing.Short() {
		t.Skip("pulando teste de integração em modo -short")
	}
	// newTestEnv deixa o banco vazio. Sem isso, um usuário deixado por outro teste faria
	// o bootstrap ser pulado (ele é idempotente por desenho) e o login abaixo falharia.
	newTestEnv(t)
	dbURL := databaseURL(t)

	bin := filepath.Join(t.TempDir(), "vodmanager")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "./cmd/vodmanager")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compilando o binário: %v\n%s", err, out)
	}

	key, err := cryptobox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	port, err := freePort()
	if err != nil {
		t.Fatalf("porta livre: %v", err)
	}
	const senhaAdmin = "senha-do-boot-de-teste"

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"VODM_DATABASE_URL="+dbURL,
		"VODM_ENCRYPTION_KEY="+key,
		"VODM_HTTP_ADDR=127.0.0.1:"+strconv.Itoa(port),
		"VODM_ROLE=all",
		"VODM_NODE_ID=node-boot",
		"VODM_LOG_FORMAT=text",
		"VODM_BOOTSTRAP_ADMIN_USERNAME=bootadmin",
		"VODM_BOOTSTRAP_ADMIN_PASSWORD="+senhaAdmin,
	)
	var saida strings.Builder
	cmd.Stdout = &saida
	cmd.Stderr = &saida

	if err := cmd.Start(); err != nil {
		t.Fatalf("iniciando o binário: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Timeout: 3 * time.Second}
	if !esperarPronto(client, base+"/readyz", 45*time.Second) {
		t.Fatalf("o servidor não ficou pronto a tempo. Saída:\n%s", saida.String())
	}

	// O administrador inicial criado no boot consegue autenticar.
	resp, err := client.Post(base+"/api/v1/auth/login", "application/json",
		strings.NewReader(`{"username":"bootadmin","password":"`+senhaAdmin+`"}`))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d: %s\nsaída do processo:\n%s", resp.StatusCode, corpo, saida.String())
	}
	var payload struct {
		User struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(corpo, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.User.Username != "bootadmin" || payload.User.Role != "admin" {
		t.Fatalf("usuário do boot inesperado: %+v", payload.User)
	}

	// A senha do administrador não pode aparecer no log de partida.
	if strings.Contains(saida.String(), senhaAdmin) {
		t.Error("a senha do administrador vazou na saída do processo")
	}
	// O log de partida também não pode conter a chave mestra.
	if strings.Contains(saida.String(), key) {
		t.Error("a chave de cifra vazou na saída do processo")
	}
	if !strings.Contains(saida.String(), "modulos") {
		t.Errorf("o log de partida não registrou os módulos habilitados:\n%s", saida.String())
	}
}

func esperarPronto(client *http.Client, url string, limite time.Duration) bool {
	deadline := time.Now().Add(limite)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("não encontrei a raiz do repositório (go.mod)")
	return ""
}
