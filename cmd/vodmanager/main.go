// Comando vodmanager: binário de produção do VOD Manager.
//
// O papel do processo (VODM_ROLE = manager | node | all) decide quais módulos sobem.
// A montagem da aplicação vive em internal/app, compartilhada com o binário de
// desenvolvimento — assim os dois sobem exatamente o mesmo sistema.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"vodmanager/internal/app"
	"vodmanager/internal/config"
	"vodmanager/internal/cryptobox"
)

// version é preenchida no build via -ldflags.
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		if code, tratado := runSubcommand(os.Args[1]); tratado {
			os.Exit(code)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro fatal:", err)
		fmt.Fprintln(os.Stderr, "\nDica: para desenvolvimento local sem instalar Postgres, use:")
		fmt.Fprintln(os.Stderr, "  go run ./cmd/vodm-dev")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg, version); err != nil {
		fmt.Fprintln(os.Stderr, "erro fatal:", err)
		os.Exit(1)
	}
}

// runSubcommand trata os utilitários que não sobem o servidor.
func runSubcommand(cmd string) (int, bool) {
	switch cmd {
	case "genkey":
		key, err := cryptobox.GenerateKey()
		if err != nil {
			fmt.Fprintln(os.Stderr, "erro:", err)
			return 1, true
		}
		fmt.Printf("VODM_ENCRYPTION_KEY=%s\n", key)
		return 0, true
	case "backup":
		return comandoBackup(os.Args[2:]), true
	case "restaurar":
		return comandoRestaurar(os.Args[2:]), true
	case "version":
		fmt.Println(version)
		return 0, true
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0, true
	default:
		return 0, false
	}
}

const usage = `vodmanager — servidor VOD intermediário

Uso:
  vodmanager            sobe o servidor com a configuração do ambiente
  vodmanager genkey     gera uma chave mestra de cifra (VODM_ENCRYPTION_KEY)
  vodmanager version    imprime a versão do binário

  vodmanager backup [--arquivo NOME]
                        grava um backup completo (catálogo, fontes, credenciais
                        cifradas, usuários e configurações) num arquivo .tar.gz

  vodmanager restaurar --arquivo NOME [--forcar] [--sim]
                        SUBSTITUI os dados atuais pelo conteúdo do backup

O backup NÃO inclui a chave de criptografia (VODM_ENCRYPTION_KEY), de propósito.
Guarde-a separadamente: sem ela, as credenciais das fontes e as senhas dos seus
clientes são irrecuperáveis.

Configuração: variáveis de ambiente com prefixo VODM_. Ver .env.example.

Para desenvolvimento local sem instalar Postgres:
  go run ./cmd/vodm-dev
`
