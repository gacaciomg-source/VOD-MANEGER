package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrChaveDiferente indica que a chave desta máquina não é a do backup.
var ErrChaveDiferente = errors.New("a chave de criptografia é diferente da usada no backup")

// ErrFormatoDesconhecido indica um arquivo gerado por uma versão futura.
var ErrFormatoDesconhecido = errors.New("formato de backup desconhecido")

// OpcoesRestauracao controla a restauração.
type OpcoesRestauracao struct {
	Pool   *pgxpool.Pool
	Chave  []byte
	Origem io.Reader
	// Forcar prossegue mesmo com a chave diferente. Só faz sentido quando o administrador
	// aceita conscientemente perder o acesso às credenciais cifradas.
	Forcar bool
	Log    func(string, ...any)
}

// Restaurar substitui o conteúdo do banco pelo do backup.
//
// É destrutivo por natureza: restaurar é trazer de volta um estado, não misturar dois.
// Tudo acontece numa transação — ou o banco fica exatamente como o backup, ou fica
// exatamente como estava. Não existe estado intermediário publicado.
func Restaurar(ctx context.Context, o OpcoesRestauracao) (*Manifesto, error) {
	registrar := o.Log
	if registrar == nil {
		registrar = func(string, ...any) {}
	}

	man, dados, err := lerArquivo(o.Origem)
	if err != nil {
		return nil, err
	}
	if man.Formato > FormatoAtual {
		return nil, fmt.Errorf("%w: arquivo no formato %d, este sistema entende até o %d — atualize o VOD Manager",
			ErrFormatoDesconhecido, man.Formato, FormatoAtual)
	}

	// A conferência da chave vem antes de qualquer escrita. Restaurar com a chave errada
	// produz um sistema que parece íntegro e falha só quando alguém tenta assistir.
	if man.ImpressaoChave != "" {
		atual := ImpressaoDaChave(o.Chave)
		if atual != man.ImpressaoChave {
			if !o.Forcar {
				return nil, fmt.Errorf("%w (backup: %s, esta máquina: %s) — "+
					"as credenciais das fontes e as senhas de saída ficariam ilegíveis. "+
					"Use a chave correta, ou repita com --forcar para restaurar mesmo assim",
					ErrChaveDiferente, man.ImpressaoChave, atual)
			}
			registrar("ATENÇÃO: restaurando com chave diferente; credenciais cifradas serão inutilizáveis",
				"backup", man.ImpressaoChave, "atual", atual)
		}
	}

	conn, err := o.Pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("restauração: obtendo conexão: %w", err)
	}
	defer conn.Release()

	var schemaAtual int64
	if err := conn.QueryRow(ctx,
		`SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&schemaAtual); err != nil {
		return nil, fmt.Errorf("restauração: lendo versão do schema: %w", err)
	}
	// Restaurar num schema mais ANTIGO falharia por coluna inexistente, de forma
	// difícil de entender. Num schema mais novo funciona: as colunas novas assumem o
	// padrão, que é o que uma migração faria de qualquer forma.
	if schemaAtual < man.SchemaMigracao {
		return nil, fmt.Errorf(
			"restauração: o backup vem do schema %d e esta máquina está no %d — "+
				"atualize o VOD Manager desta máquina antes de restaurar",
			man.SchemaMigracao, schemaAtual)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("restauração: iniciando transação: %w", err)
	}
	defer tx.Rollback(ctx)

	// As restrições de chave estrangeira são adiadas para o fim da transação: assim a
	// ordem de carga deixa de ser um problema mesmo que uma tabela ganhe uma referência
	// nova no futuro. No commit, tudo é verificado de uma vez.
	// Falha aqui não pode ser engolida: dentro de uma transação, um erro ignorado a
	// envenena, e o commit vira rollback silencioso.
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`); err != nil {
		return nil, fmt.Errorf("restauração: adiando restrições: %w", err)
	}

	alvo := append(append([]string{}, man.Tabelas...), efemeras...)
	if _, err := tx.Exec(ctx,
		`TRUNCATE `+strings.Join(alvo, ", ")+` RESTART IDENTITY CASCADE`); err != nil {
		return nil, fmt.Errorf("restauração: limpando tabelas: %w", err)
	}

	for _, t := range man.Tabelas {
		csv, ok := dados[t]
		if !ok {
			registrar("tabela ausente no arquivo; seguindo", "tabela", t)
			continue
		}
		linhas, err := carregarTabela(ctx, tx, t, csv)
		if err != nil {
			return nil, err
		}
		registrar("tabela restaurada", "tabela", t, "linhas", linhas)
	}

	// As sequências continuam onde o TRUNCATE as deixou (em 1), mas as linhas trazem ids
	// altos. Sem reposicioná-las, o primeiro cadastro novo colidiria com um id existente.
	if err := reposicionarSequencias(ctx, tx, man.Tabelas, registrar); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("restauração: confirmando: %w", err)
	}
	return man, nil
}

func carregarTabela(ctx context.Context, tx pgx.Tx, tabela string, csv []byte) (int64, error) {
	// A lista de colunas vem do cabeçalho do CSV, e não de `SELECT *`. É o que permite
	// restaurar um backup antigo num schema mais novo: as colunas que não existiam no
	// backup assumem o padrão em vez de quebrar a carga.
	cabecalho, _, achou := strings.Cut(string(csv), "\n")
	if !achou || strings.TrimSpace(cabecalho) == "" {
		return 0, nil
	}
	colunas := strings.Split(strings.TrimSpace(strings.TrimSuffix(cabecalho, "\r")), ",")
	for i := range colunas {
		colunas[i] = `"` + strings.Trim(colunas[i], `"`) + `"`
	}

	tag, err := tx.Conn().PgConn().CopyFrom(ctx, strings.NewReader(string(csv)),
		fmt.Sprintf(`COPY %s (%s) FROM STDIN WITH (FORMAT csv, HEADER true)`,
			tabela, strings.Join(colunas, ", ")))
	if err != nil {
		return 0, fmt.Errorf("restauração: carregando %s: %w", tabela, err)
	}
	return tag.RowsAffected(), nil
}

// reposicionarSequencias avança cada sequência de identidade para além do maior id.
//
// A lista de sequências vem de uma consulta ao catálogo do Postgres, e não de
// pg_get_serial_sequence por tabela: essa função ERRA quando a coluna não existe, e um
// erro dentro da transação a envenena inteira — o commit vira rollback silencioso, com o
// banco vazio e nenhuma mensagem útil sobre o motivo. Tabelas sem coluna de identidade,
// como settings, simplesmente não aparecem no resultado.
func reposicionarSequencias(ctx context.Context, tx pgx.Tx, tabelas []string, registrar func(string, ...any)) error {
	rows, err := tx.Query(ctx, `
		SELECT c.relname, pg_get_serial_sequence('public.' || c.relname, a.attname), a.attname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = 'public'
		  AND c.relname = ANY($1)
		  AND a.attnum > 0 AND NOT a.attisdropped
		  AND a.attidentity <> ''`, tabelas)
	if err != nil {
		return fmt.Errorf("restauração: listando sequências: %w", err)
	}

	type alvoSeq struct{ tabela, sequencia, coluna string }
	var alvos []alvoSeq
	for rows.Next() {
		var a alvoSeq
		var seq *string
		if err := rows.Scan(&a.tabela, &seq, &a.coluna); err != nil {
			rows.Close()
			return fmt.Errorf("restauração: listando sequências: %w", err)
		}
		if seq != nil {
			a.sequencia = *seq
			alvos = append(alvos, a)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("restauração: listando sequências: %w", err)
	}

	for _, a := range alvos {
		if _, err := tx.Exec(ctx, fmt.Sprintf(
			`SELECT setval('%s', coalesce((SELECT max(%s) FROM %s), 0) + 1, false)`,
			a.sequencia, a.coluna, a.tabela)); err != nil {
			return fmt.Errorf("restauração: reposicionando a sequência de %s: %w", a.tabela, err)
		}
	}
	registrar("sequências reposicionadas", "quantidade", len(alvos))
	return nil
}

// lerArquivo descompacta o backup em memória.
//
// O catálogo inteiro em CSV comprimido é da ordem de dezenas de megabytes; carregar de
// uma vez troca um pouco de memória, durante uma operação pontual, por um código bem mais
// simples do que uma leitura em fluxo com ordem imposta pelo tar.
func lerArquivo(r io.Reader) (*Manifesto, map[string][]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, fmt.Errorf("restauração: o arquivo não parece um backup válido: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var man *Manifesto
	dados := map[string][]byte{}

	for {
		cab, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("restauração: lendo o arquivo: %w", err)
		}
		conteudo, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, fmt.Errorf("restauração: lendo %s: %w", cab.Name, err)
		}
		switch {
		case cab.Name == "manifesto.json":
			man = &Manifesto{}
			if err := json.Unmarshal(conteudo, man); err != nil {
				return nil, nil, fmt.Errorf("restauração: manifesto ilegível: %w", err)
			}
		case strings.HasPrefix(cab.Name, "dados/") && strings.HasSuffix(cab.Name, ".csv"):
			nome := strings.TrimSuffix(strings.TrimPrefix(cab.Name, "dados/"), ".csv")
			dados[nome] = conteudo
		}
	}
	if man == nil {
		return nil, nil, errors.New("restauração: o arquivo não tem manifesto — não é um backup do VOD Manager")
	}
	return man, dados, nil
}
