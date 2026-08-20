package db

import (
	"strings"
	"testing"
)

func TestLoadMigrationsOrdenadasEValidas(t *testing.T) {
	ms, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("nenhuma migração embutida no binário")
	}

	seen := make(map[int64]bool)
	for i, m := range ms {
		if m.Version <= 0 {
			t.Errorf("migração %d tem versão inválida: %d", i, m.Version)
		}
		if seen[m.Version] {
			t.Errorf("versão %d duplicada", m.Version)
		}
		seen[m.Version] = true
		if i > 0 && ms[i-1].Version >= m.Version {
			t.Errorf("migrações fora de ordem: %d antes de %d", ms[i-1].Version, m.Version)
		}
		if strings.TrimSpace(m.SQL) == "" {
			t.Errorf("migração %d (%s) está vazia", m.Version, m.Name)
		}
		if m.Name == "" {
			t.Errorf("migração %d sem nome", m.Version)
		}
	}
}

// sqlDaMigracao devolve o SQL de uma versão específica, em minúsculas.
func sqlDaMigracao(t *testing.T, versao int64) string {
	t.Helper()
	ms, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	for _, m := range ms {
		if m.Version == versao {
			return strings.ToLower(m.SQL)
		}
	}
	t.Fatalf("migração %d não encontrada", versao)
	return ""
}

// sqlDeTodasAsMigracoes concatena tudo, em minúsculas.
func sqlDeTodasAsMigracoes(t *testing.T) string {
	t.Helper()
	ms, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	var all strings.Builder
	for _, m := range ms {
		all.WriteString(strings.ToLower(m.SQL))
	}
	return all.String()
}

// Cada migração cria apenas o que a sua fase usa (doc 05: "nada de tabela morta").
func TestMigracao0001TemApenasOEscopoDaFase1(t *testing.T) {
	sql := sqlDaMigracao(t, 1)

	esperadas := []string{
		"create table users",
		"create table sessions",
		"create table api_tokens",
		"create table settings",
		"create table events",
		"create table sources",
		"create table source_credentials",
	}
	for _, e := range esperadas {
		if !strings.Contains(sql, e) {
			t.Errorf("a migração 0001 não contém %q", e)
		}
	}

	// Catálogo é Fase 2: não pode estar na 0001.
	proibidas := []string{"create table contents", "create table source_variants", "create table sync_runs"}
	for _, p := range proibidas {
		if strings.Contains(sql, p) {
			t.Errorf("a migração 0001 contém %q — isso pertence à Fase 2", p)
		}
	}
}

func TestMigracao0005TemOEscopoDoStreaming(t *testing.T) {
	sql := sqlDaMigracao(t, 5)
	for _, e := range []string{"create table stream_credentials", "create table streams"} {
		if !strings.Contains(sql, e) {
			t.Errorf("a migração 0005 não contém %q", e)
		}
	}
	// A credencial de saída nunca guarda senha em claro — só o HMAC.
	if !strings.Contains(sql, "password_hmac") {
		t.Error("stream_credentials precisa guardar HMAC, não senha")
	}
	if strings.Contains(sql, "password text") {
		t.Error("stream_credentials não pode ter coluna de senha em texto")
	}
}

func TestMigracao0002TemOEscopoDoCatalogo(t *testing.T) {
	sql := sqlDaMigracao(t, 2)

	esperadas := []string{
		"create table categories",
		"create table source_categories",
		"create table contents",
		"create table seasons",
		"create table episodes",
		"create table source_variants",
		"create table unresolved_items",
		"create table match_decisions",
		"create table sync_runs",
	}
	for _, e := range esperadas {
		if !strings.Contains(sql, e) {
			t.Errorf("a migração 0002 não contém %q", e)
		}
	}
}

// Tabelas de fases que ainda não chegaram não podem existir em nenhuma migração.
func TestTabelasDeFasesFuturasAindaNaoExistem(t *testing.T) {
	sql := sqlDeTodasAsMigracoes(t)

	// Cada uma pertence a uma fase específica do plano em docs/05.
	//
	// stream_credentials e streams saíram desta lista na Fase 6: a decisão D7 foi
	// implementada junto com o proxy de streaming, porque não há como servir vídeo sem
	// autenticar quem pede.
	futuras := map[string]string{
		"create table cache_entries":      "Fase 5 (Cache Engine)",
		"create table download_jobs":      "Fase 5 (Cache Engine)",
		"create table storage_providers":  "Fase 8 (Storage Manager)",
		"create table orphan_records":     "Fase 9 (Lifecycle)",
		"create table quarantine_entries": "Fase 9 (Lifecycle)",
		"create table archive_entries":    "Fase 9 (Lifecycle)",
	}
	for tabela, fase := range futuras {
		if strings.Contains(sql, tabela) {
			t.Errorf("%q apareceu antes da hora — pertence à %s", tabela, fase)
		}
	}
}
