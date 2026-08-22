package integration

import (
	"context"
	"sort"
	"testing"

	"vodmanager/internal/backup"
)

// TestBackupCobreTodasAsTabelas confere a lista do backup contra o banco de verdade.
//
// # Por que este teste existe
//
// Duas migrações criaram tabelas — `duplicatas_ignoradas` e `category_aliases` — e ninguém
// as acrescentou à lista do backup. Nada acusou: o backup continuou sendo gerado com
// sucesso, a restauração continuou devolvendo tudo o que a lista mandava salvar, e todos os
// testes continuaram passando. O buraco só apareceria numa migração de servidor, com as
// decisões do administrador (duplicatas descartadas, categorias unidas) sumindo em
// silêncio.
//
// É a única forma de falha que um backup não pode ter: a que só se descobre na hora de
// usar. Daí a guarda ser estrutural — ela compara com o que o Postgres realmente tem, e não
// com uma segunda lista escrita à mão, que esqueceria junto.
//
// Uma tabela nova passa a exigir uma decisão explícita: entra em `tabelas` (é salva) ou em
// `efemeras` (não faz sentido atravessar uma migração). As duas são escolhas legítimas;
// não escolher é que não é.
func TestBackupCobreTodasAsTabelas(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	rows, err := env.Pool.Query(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname <> 'schema_migrations'
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("listando tabelas: %v", err)
	}
	defer rows.Close()

	var noBanco []string
	for rows.Next() {
		var nome string
		if err := rows.Scan(&nome); err != nil {
			t.Fatalf("scan: %v", err)
		}
		noBanco = append(noBanco, nome)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("listando tabelas: %v", err)
	}
	if len(noBanco) == 0 {
		t.Fatal("nenhuma tabela encontrada — a guarda não estaria protegendo nada")
	}

	decidida := map[string]string{}
	for _, tabela := range backup.TabelasSalvas() {
		decidida[tabela] = "salva"
	}
	for _, tabela := range backup.TabelasEfemeras() {
		decidida[tabela] = "efêmera"
	}

	var esquecidas []string
	for _, tabela := range noBanco {
		if decidida[tabela] == "" {
			esquecidas = append(esquecidas, tabela)
		}
	}
	if len(esquecidas) > 0 {
		sort.Strings(esquecidas)
		t.Errorf("estas tabelas existem no banco e ninguém decidiu o destino delas no backup: %v\n"+
			"    Acrescente cada uma a `tabelas` (é salva, respeitando a ordem das chaves\n"+
			"    estrangeiras) ou a `efemeras` (não atravessa uma migração), em internal/backup/backup.go.",
			esquecidas)
	}

	// O caminho contrário: uma tabela listada que não existe mais faria o backup falhar
	// inteiro, no meio, com um erro do Postgres — e um backup que não é gerado é pior que
	// um backup incompleto, porque não existe.
	existe := map[string]bool{}
	for _, tabela := range noBanco {
		existe[tabela] = true
	}
	for _, tabela := range backup.TabelasSalvas() {
		if !existe[tabela] {
			t.Errorf("o backup manda salvar %q, que não existe no banco", tabela)
		}
	}
}
