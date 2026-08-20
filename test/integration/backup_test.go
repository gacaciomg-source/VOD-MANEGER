package integration

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"vodmanager/internal/backup"
	"vodmanager/internal/cryptobox"
	"vodmanager/internal/edge"
	"vodmanager/internal/store"
)

// TestBackupRestauraOEstadoInteiro é o teste que dá sentido ao backup: o que sai do
// arquivo precisa ser indistinguível do que entrou. Sem isto, o administrador só descobre
// que o backup não presta na hora em que precisa dele.
func TestBackupRestauraOEstadoInteiro(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Um estado com as partes que importam: fonte, catálogo, categorias, credencial de
	// saída cifrada e configuração.
	const origem = "http://fonte-do-backup.exemplo.tld"
	lista := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-name="Interestelar (2014)" group-title="Filmes | Ficcao",Interestelar (2014)` + "\n" +
		origem + "/movie/u/s/1.mp4\n" +
		`#EXTINF:-1 tvg-name="Arquivo X S01E01" group-title="Series | Misterio",Arquivo X S01E01` + "\n" +
		origem + "/series/u/s/2.mkv\n"

	fonte := novaFonteM3U(t, lista)
	src := cadastrarFonte(t, env, "Fonte do Backup", store.SourceKindM3U,
		fonte.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)
	if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
		t.Fatalf("SyncSource: %v", err)
	}

	autenticador := edge.NewAuthenticator(env.Store, chaveDeTeste(t))
	cifrada, err := env.Crypto.Seal([]byte("senha-do-cliente"),
		cryptobox.StreamCredentialAAD("cliente.backup"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := env.Store.CreateStreamCredential(ctx, "Cliente do Backup", "", "cliente.backup",
		autenticador.HashSenha("senha-do-cliente"), cifrada, 0, nil); err != nil {
		t.Fatalf("CreateStreamCredential: %v", err)
	}
	if err := env.Store.SetSetting(ctx, store.SettingPublicBaseURL, "http://198.51.100.10:8080"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	antes := fotografar(t, env)

	// --- backup ---
	var arquivo bytes.Buffer
	man, err := backup.Gerar(ctx, backup.Opcoes{
		Pool: env.Pool, Chave: chaveDeTeste(t), Versao: "teste", Destino: &arquivo,
	})
	if err != nil {
		t.Fatalf("Gerar: %v", err)
	}
	if man.Linhas["contents"] == 0 || man.Linhas["source_variants"] == 0 {
		t.Fatalf("o manifesto não contou o catálogo: %+v", man.Linhas)
	}
	if man.ImpressaoChave == "" {
		t.Error("o manifesto não registrou a impressão da chave")
	}
	// A chave não pode viajar dentro do arquivo.
	if bytes.Contains(arquivo.Bytes(), chaveDeTeste(t)) {
		t.Fatal("a chave de criptografia foi parar dentro do backup")
	}

	// --- destruição ---
	truncateAll(t, env.Pool)
	if depois := fotografar(t, env); depois.filmes != 0 || depois.fontes != 0 {
		t.Fatalf("o banco não foi limpo antes da restauração: %+v", depois)
	}

	// --- restauração ---
	restaurado, err := backup.Restaurar(ctx, backup.OpcoesRestauracao{
		Pool: env.Pool, Chave: chaveDeTeste(t), Origem: bytes.NewReader(arquivo.Bytes()),
	})
	if err != nil {
		t.Fatalf("Restaurar: %v", err)
	}
	if restaurado.CriadoEm.IsZero() {
		t.Error("o manifesto restaurado veio sem data")
	}

	depois := fotografar(t, env)
	if depois != antes {
		t.Fatalf("o estado restaurado difere do original:\n antes:  %+v\n depois: %+v", antes, depois)
	}

	// A credencial cifrada precisa continuar decifrável com a mesma chave — é o que
	// separa um backup útil de um catálogo sem acesso nenhum.
	creds, err := env.Store.ListStreamCredentials(ctx)
	if err != nil || len(creds) != 1 {
		t.Fatalf("credenciais após restauração: %v %d", err, len(creds))
	}
	clara, err := env.Crypto.Open(creds[0].PasswordEnc,
		cryptobox.StreamCredentialAAD(creds[0].Username))
	if err != nil {
		t.Fatalf("a senha cifrada não sobreviveu ao backup: %v", err)
	}
	if string(clara) != "senha-do-cliente" {
		t.Errorf("senha restaurada = %q", clara)
	}

	// Configuração também volta.
	if v, _ := env.Store.GetSetting(ctx, store.SettingPublicBaseURL, ""); v != "http://198.51.100.10:8080" {
		t.Errorf("configuração restaurada = %q", v)
	}
}

// Depois de restaurar, cadastrar algo novo não pode colidir com um id existente: o
// TRUNCATE zera as sequências e as linhas voltam com ids altos.
func TestSequenciasContinuamDepoisDaRestauracao(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	for _, nome := range []string{"Fonte A", "Fonte B", "Fonte C"} {
		cadastrarFonte(t, env, nome, store.SourceKindM3U, "http://exemplo.tld/"+nome+".m3u", false)
	}

	var arquivo bytes.Buffer
	if _, err := backup.Gerar(ctx, backup.Opcoes{
		Pool: env.Pool, Chave: chaveDeTeste(t), Destino: &arquivo,
	}); err != nil {
		t.Fatalf("Gerar: %v", err)
	}
	truncateAll(t, env.Pool)
	if _, err := backup.Restaurar(ctx, backup.OpcoesRestauracao{
		Pool: env.Pool, Chave: chaveDeTeste(t), Origem: bytes.NewReader(arquivo.Bytes()),
	}); err != nil {
		t.Fatalf("Restaurar: %v", err)
	}

	nova := cadastrarFonte(t, env, "Fonte Nova", store.SourceKindM3U, "http://exemplo.tld/nova.m3u", false)
	fontes, err := env.Store.ListSources(ctx)
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(fontes) != 4 {
		t.Fatalf("fontes = %d, esperava 4 — a nova sobrescreveu uma restaurada", len(fontes))
	}
	for _, f := range fontes {
		if f.ID == nova.ID && f.Name != "Fonte Nova" {
			t.Fatalf("colisão de id: %d", nova.ID)
		}
	}
}

// Restaurar com a chave errada produziria um sistema que parece íntegro e falha só quando
// alguém tenta assistir. Precisa parar antes de escrever qualquer coisa.
func TestRestauracaoRecusaChaveDiferente(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	cadastrarFonte(t, env, "Fonte", store.SourceKindM3U, "http://exemplo.tld/a.m3u", false)

	var arquivo bytes.Buffer
	if _, err := backup.Gerar(ctx, backup.Opcoes{
		Pool: env.Pool, Chave: chaveDeTeste(t), Destino: &arquivo,
	}); err != nil {
		t.Fatalf("Gerar: %v", err)
	}

	outraChave := bytes.Repeat([]byte{0x42}, 32)
	_, err := backup.Restaurar(ctx, backup.OpcoesRestauracao{
		Pool: env.Pool, Chave: outraChave, Origem: bytes.NewReader(arquivo.Bytes()),
	})
	if !errors.Is(err, backup.ErrChaveDiferente) {
		t.Fatalf("erro = %v, esperava ErrChaveDiferente", err)
	}
	// E os dados atuais continuam intactos: a recusa vem antes de qualquer escrita.
	if fontes, _ := env.Store.ListSources(ctx); len(fontes) != 1 {
		t.Errorf("a recusa mexeu nos dados: %d fontes", len(fontes))
	}

	// Com --forcar, prossegue: é decisão consciente do administrador.
	if _, err := backup.Restaurar(ctx, backup.OpcoesRestauracao{
		Pool: env.Pool, Chave: outraChave, Origem: bytes.NewReader(arquivo.Bytes()), Forcar: true,
	}); err != nil {
		t.Fatalf("com --forcar deveria prosseguir: %v", err)
	}
}

func TestRestauracaoRecusaArquivoInvalido(t *testing.T) {
	env := newTestEnv(t)

	casos := map[string][]byte{
		"texto qualquer": []byte("isto não é um backup"),
		"vazio":          {},
	}
	for nome, conteudo := range casos {
		_, err := backup.Restaurar(context.Background(), backup.OpcoesRestauracao{
			Pool: env.Pool, Chave: chaveDeTeste(t), Origem: bytes.NewReader(conteudo),
		})
		if err == nil {
			t.Errorf("%s: deveria ter sido recusado", nome)
			continue
		}
		if !strings.Contains(err.Error(), "backup") {
			t.Errorf("%s: mensagem pouco clara: %v", nome, err)
		}
	}
}

// foto é o resumo do estado usado para comparar antes e depois.
type foto struct {
	fontes, filmes, series, episodios, variantes, categorias, credenciais int
}

func fotografar(t *testing.T, env *testEnv) foto {
	t.Helper()
	ctx := context.Background()
	var f foto

	contar := func(sql string) int {
		var n int
		if err := env.Pool.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatalf("contando (%s): %v", sql, err)
		}
		return n
	}
	f.fontes = contar(`SELECT count(*) FROM sources`)
	f.filmes = contar(`SELECT count(*) FROM contents WHERE type = 'movie'`)
	f.series = contar(`SELECT count(*) FROM contents WHERE type = 'series'`)
	f.episodios = contar(`SELECT count(*) FROM episodes`)
	f.variantes = contar(`SELECT count(*) FROM source_variants`)
	f.categorias = contar(`SELECT count(*) FROM categories`)
	f.credenciais = contar(`SELECT count(*) FROM stream_credentials`)
	return f
}
