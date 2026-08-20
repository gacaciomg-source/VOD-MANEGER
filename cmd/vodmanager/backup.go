package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"vodmanager/internal/backup"
	"vodmanager/internal/config"
	"vodmanager/internal/db"
)

// comandoBackup grava um backup completo em arquivo.
func comandoBackup(args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	destino := fs.String("arquivo", "", "caminho do arquivo a gerar (padrão: vodmanager-AAAA-MM-DD.tar.gz)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	caminho := *destino
	if caminho == "" {
		caminho = fmt.Sprintf("vodmanager-%s.tar.gz", time.Now().Format("2006-01-02-1504"))
	}

	cfg, pool, encerrar, code := abrirBanco()
	if code != 0 {
		return code
	}
	defer encerrar()

	// Escreve num arquivo temporário e só renomeia no fim: um backup interrompido pela
	// metade não pode ficar no lugar do bom, com nome de arquivo válido.
	parcial := caminho + ".parcial"
	f, err := os.Create(parcial)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro: não foi possível criar o arquivo:", err)
		return 1
	}

	man, err := backup.Gerar(context.Background(), backup.Opcoes{
		Pool:    pool,
		Chave:   cfg.EncryptionKey,
		Versao:  version,
		Destino: f,
		Log: func(msg string, kv ...any) {
			fmt.Println(" ", msg, formatarPares(kv))
		},
	})
	f.Close()
	if err != nil {
		os.Remove(parcial)
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	if err := os.Rename(parcial, caminho); err != nil {
		fmt.Fprintln(os.Stderr, "erro: não foi possível finalizar o arquivo:", err)
		return 1
	}

	info, _ := os.Stat(caminho)
	absoluto, _ := filepath.Abs(caminho)
	total := 0
	for _, n := range man.Linhas {
		total += n
	}

	fmt.Printf(`
Backup concluído.

  Arquivo ....... %s
  Tamanho ....... %s
  Linhas ........ %d
  Schema ........ versão %d
  Chave ......... impressão %s

GUARDE A CHAVE DE CRIPTOGRAFIA SEPARADAMENTE.
Ela NÃO está neste arquivo, de propósito. Sem ela, o backup restaura o catálogo mas
não as credenciais das suas fontes nem as senhas dos seus clientes.

  A chave é o valor de VODM_ENCRYPTION_KEY, em /etc/vodmanager.env.
`, absoluto, tamanhoHumano(info), total, man.SchemaMigracao, man.ImpressaoChave)
	return 0
}

// comandoRestaurar recarrega o banco a partir de um backup.
func comandoRestaurar(args []string) int {
	fs := flag.NewFlagSet("restaurar", flag.ContinueOnError)
	origem := fs.String("arquivo", "", "caminho do backup a restaurar (obrigatório)")
	forcar := fs.Bool("forcar", false, "restaurar mesmo com chave de criptografia diferente")
	confirmar := fs.Bool("sim", false, "não perguntar antes de substituir os dados atuais")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *origem == "" {
		fmt.Fprintln(os.Stderr, "erro: informe --arquivo")
		return 2
	}

	f, err := os.Open(*origem)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro: não foi possível abrir o arquivo:", err)
		return 1
	}
	defer f.Close()

	cfg, pool, encerrar, code := abrirBanco()
	if code != 0 {
		return code
	}
	defer encerrar()

	// Restaurar é destrutivo: substitui tudo. Sem confirmação explícita, um comando
	// digitado por engano apagaria o sistema em produção.
	if !*confirmar {
		fmt.Printf("Isto APAGA os dados atuais e coloca no lugar o conteúdo de %s.\n", *origem)
		fmt.Print("Digite 'restaurar' para confirmar: ")
		var resposta string
		fmt.Scanln(&resposta)
		if resposta != "restaurar" {
			fmt.Println("Cancelado. Nada foi alterado.")
			return 1
		}
	}

	man, err := backup.Restaurar(context.Background(), backup.OpcoesRestauracao{
		Pool:   pool,
		Chave:  cfg.EncryptionKey,
		Origem: f,
		Forcar: *forcar,
		Log: func(msg string, kv ...any) {
			fmt.Println(" ", msg, formatarPares(kv))
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nerro:", err)
		fmt.Fprintln(os.Stderr, "\nNada foi alterado: a restauração é uma transação só.")
		return 1
	}

	total := 0
	for _, n := range man.Linhas {
		total += n
	}
	fmt.Printf(`
Restauração concluída.

  Backup de .... %s
  Linhas ....... %d

Reinicie o serviço para que ele leia o estado novo:
  sudo systemctl restart vodmanager

Depois, confira em Configurações se o endereço público está correto para ESTA máquina.
`, man.CriadoEm.Format("2006-01-02 15:04"), total)
	return 0
}

// abrirBanco carrega a configuração e abre a conexão, com mensagens de erro úteis.
func abrirBanco() (*config.Config, *db.Pool, func(), int) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro de configuração:", err)
		fmt.Fprintln(os.Stderr, "\nEste comando precisa das mesmas variáveis que o servidor.")
		fmt.Fprintln(os.Stderr, "Num serviço instalado pelo guia:")
		fmt.Fprintln(os.Stderr, "  set -a && . /etc/vodmanager.env && set +a")
		return nil, nil, nil, 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, db.Options{
		DatabaseURL:     cfg.DatabaseURL,
		MaxConns:        2,
		ConnectTimeout:  cfg.DBConnectTimeout,
		ApplicationName: "vodmanager/backup",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro: não foi possível conectar ao banco:", err)
		return nil, nil, nil, 1
	}
	return cfg, pool, pool.Close, 0
}

func formatarPares(kv []any) string {
	out := ""
	for i := 0; i+1 < len(kv); i += 2 {
		out += fmt.Sprintf("%v=%v ", kv[i], kv[i+1])
	}
	return out
}

func tamanhoHumano(info os.FileInfo) string {
	if info == nil {
		return "?"
	}
	b := info.Size()
	const u = 1024
	if b < u {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(u), 0
	for n := b / u; n >= u && exp < 3; n /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMG"[exp])
}
