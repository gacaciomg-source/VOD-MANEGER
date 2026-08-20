package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"vodmanager/internal/auth"
	"vodmanager/internal/cryptobox"
	"vodmanager/internal/store"
)

func TestUsuarioCicloDeVida(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	hash, err := auth.HashPassword("senha-do-administrador")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	u, err := env.Store.CreateUser(ctx, "admin", hash, store.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == 0 || u.Role != store.RoleAdmin || !u.Enabled {
		t.Fatalf("usuário criado inconsistente: %+v", u)
	}

	if _, err := env.Store.CreateUser(ctx, "admin", hash, store.RoleAdmin); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("nome duplicado: esperava ErrConflict, obtive %v", err)
	}
	if _, err := env.Store.CreateUser(ctx, "outro", hash, "superuser"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("papel inválido: esperava ErrInvalid, obtive %v", err)
	}

	found, err := env.Store.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if found.PasswordHash != hash {
		t.Error("hash de senha não sobreviveu ao round-trip")
	}
	if _, err := env.Store.GetUserByUsername(ctx, "inexistente"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("esperava ErrNotFound, obtive %v", err)
	}

	if err := env.Store.TouchUserLogin(ctx, u.ID); err != nil {
		t.Fatalf("TouchUserLogin: %v", err)
	}
	after, _ := env.Store.GetUserByID(ctx, u.ID)
	if after.LastLoginAt == nil {
		t.Error("last_login_at não foi registrado")
	}
}

func TestSessaoSoRetornaQuandoValida(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	user := criarUsuario(t, env, "admin", store.RoleAdmin)

	tok, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	sess, err := env.Store.CreateSession(ctx, user.ID, tok.Hash, "teste-agent", "10.0.0.1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, gotUser, err := env.Store.LookupSession(ctx, tok.Hash)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if got.ID != sess.ID || gotUser.ID != user.ID {
		t.Fatal("LookupSession devolveu sessão/usuário errados")
	}

	// Revogada não retorna.
	if err := env.Store.RevokeSession(ctx, tok.Hash); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, _, err := env.Store.LookupSession(ctx, tok.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sessão revogada: esperava ErrNotFound, obtive %v", err)
	}

	// Expirada não retorna.
	expTok, _ := auth.NewToken()
	if _, err := env.Store.CreateSession(ctx, user.ID, expTok.Hash, "", "", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("CreateSession expirada: %v", err)
	}
	if _, _, err := env.Store.LookupSession(ctx, expTok.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sessão expirada: esperava ErrNotFound, obtive %v", err)
	}

	// Limpeza remove as duas.
	n, err := env.Store.PurgeExpiredSessions(ctx, 0)
	if err != nil {
		t.Fatalf("PurgeExpiredSessions: %v", err)
	}
	if n != 2 {
		t.Errorf("PurgeExpiredSessions removeu %d, esperava 2", n)
	}
}

func TestSessaoDeUsuarioDesabilitadoNaoAutentica(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	user := criarUsuario(t, env, "operador", store.RoleOperator)

	tok, _ := auth.NewToken()
	if _, err := env.Store.CreateSession(ctx, user.ID, tok.Hash, "", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := env.Pool.Exec(ctx, `UPDATE users SET enabled = false WHERE id = $1`, user.ID); err != nil {
		t.Fatalf("desabilitando usuário: %v", err)
	}
	if _, _, err := env.Store.LookupSession(ctx, tok.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("usuário desabilitado deveria invalidar a sessão, obtive %v", err)
	}
}

func TestTokenDeAPIRespeitaExpiracaoERevogacao(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	user := criarUsuario(t, env, "automacao", store.RoleOperator)

	tok, _ := auth.NewToken()
	created, err := env.Store.CreateAPIToken(ctx, user.ID, "ci", tok.Prefix, tok.Hash, nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if _, _, err := env.Store.LookupAPIToken(ctx, tok.Hash); err != nil {
		t.Fatalf("LookupAPIToken: %v", err)
	}

	expired, _ := auth.NewToken()
	past := time.Now().Add(-time.Hour)
	if _, err := env.Store.CreateAPIToken(ctx, user.ID, "expirado", expired.Prefix, expired.Hash, &past); err != nil {
		t.Fatalf("CreateAPIToken expirado: %v", err)
	}
	if _, _, err := env.Store.LookupAPIToken(ctx, expired.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token expirado deveria ser rejeitado, obtive %v", err)
	}

	if err := env.Store.RevokeAPIToken(ctx, user.ID, created.ID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, _, err := env.Store.LookupAPIToken(ctx, tok.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token revogado deveria ser rejeitado, obtive %v", err)
	}

	list, err := env.Store.ListAPITokens(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("ListAPITokens devolveu %d, esperava 2", len(list))
	}
}

func TestFonteCRUDEValidacoesDoSchema(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	src, err := env.Store.CreateSource(ctx, store.NewSource{
		Name: "Fonte Principal", Kind: store.SourceKindXtream, BaseURL: "http://exemplo.tld:8080",
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if src.Priority != 1 {
		t.Errorf("primeira fonte deveria receber prioridade 1, recebeu %d", src.Priority)
	}
	if src.SyncIntervalMinutes != 1440 || src.MaxConnections != 4 || src.MaxConcurrentDownloads != 2 {
		t.Errorf("padrões do schema não aplicados: %+v", src)
	}
	if src.HasCredentials {
		t.Error("fonte nova não deveria ter credenciais")
	}

	segunda, err := env.Store.CreateSource(ctx, store.NewSource{
		Name: "Fonte Secundária", Kind: store.SourceKindM3U, BaseURL: "http://outra.tld/lista.m3u",
	})
	if err != nil {
		t.Fatalf("CreateSource segunda: %v", err)
	}
	if segunda.Priority != 2 {
		t.Errorf("segunda fonte deveria receber prioridade 2, recebeu %d", segunda.Priority)
	}

	if _, err := env.Store.CreateSource(ctx, store.NewSource{
		Name: "Fonte Principal", Kind: store.SourceKindM3U, BaseURL: "http://x.tld",
	}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("nome duplicado: esperava ErrConflict, obtive %v", err)
	}
	if _, err := env.Store.CreateSource(ctx, store.NewSource{
		Name: "Tipo Inválido", Kind: "torrent", BaseURL: "http://x.tld",
	}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("tipo inválido: esperava ErrInvalid, obtive %v", err)
	}

	// O schema impede baixar mais do que o limite de conexões permite.
	conns, downloads := 2, 5
	if _, err := env.Store.CreateSource(ctx, store.NewSource{
		Name: "Limites Incoerentes", Kind: store.SourceKindM3U, BaseURL: "http://x.tld",
		MaxConnections: &conns, MaxConcurrentDownloads: &downloads,
	}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("downloads > conexões: esperava ErrInvalid, obtive %v", err)
	}

	novoNome := "Fonte Principal (renomeada)"
	bw := int64(5_000_000)
	bwPtr := &bw
	atualizada, err := env.Store.UpdateSource(ctx, src.ID, store.SourcePatch{
		Name: &novoNome, MaxBandwidthBPS: &bwPtr,
	})
	if err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if atualizada.Name != novoNome {
		t.Errorf("nome = %q", atualizada.Name)
	}
	if atualizada.MaxBandwidthBPS == nil || *atualizada.MaxBandwidthBPS != bw {
		t.Errorf("max_bandwidth_bps = %v", atualizada.MaxBandwidthBPS)
	}
	if atualizada.Kind != store.SourceKindXtream {
		t.Errorf("kind não deveria mudar num patch, virou %q", atualizada.Kind)
	}

	// Ponteiro duplo apontando para nil grava NULL: "sem limite de banda".
	var semLimite *int64
	limpa, err := env.Store.UpdateSource(ctx, src.ID, store.SourcePatch{MaxBandwidthBPS: &semLimite})
	if err != nil {
		t.Fatalf("UpdateSource limpando banda: %v", err)
	}
	if limpa.MaxBandwidthBPS != nil {
		t.Errorf("max_bandwidth_bps deveria ter virado NULL, ficou %v", *limpa.MaxBandwidthBPS)
	}

	if _, err := env.Store.UpdateSource(ctx, 999999, store.SourcePatch{Name: &novoNome}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("fonte inexistente: esperava ErrNotFound, obtive %v", err)
	}
	if err := env.Store.DeleteSource(ctx, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete inexistente: esperava ErrNotFound, obtive %v", err)
	}
	if err := env.Store.DeleteSource(ctx, segunda.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
}

func TestReordenarFontesReescreveAPrioridade(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	a := criarFonte(t, env, "A")
	b := criarFonte(t, env, "B")
	c := criarFonte(t, env, "C")

	if err := env.Store.ReorderSources(ctx, []int64{c.ID, a.ID, b.ID}); err != nil {
		t.Fatalf("ReorderSources: %v", err)
	}
	lista, err := env.Store.ListSources(ctx)
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	querido := []string{"C", "A", "B"}
	for i, s := range lista {
		if s.Name != querido[i] {
			t.Fatalf("ordem = %v, esperava %v", nomes(lista), querido)
		}
		if s.Priority != i+1 {
			t.Errorf("%s tem prioridade %d, esperava %d", s.Name, s.Priority, i+1)
		}
	}

	// Lista parcial deixaria prioridades ambíguas: precisa ser recusada.
	if err := env.Store.ReorderSources(ctx, []int64{a.ID}); err == nil {
		t.Error("lista parcial deveria ser recusada")
	}
	// E a ordem anterior precisa ter sido preservada.
	depois, _ := env.Store.ListSources(ctx)
	if got := nomes(depois); got[0] != "C" || got[1] != "A" || got[2] != "B" {
		t.Errorf("a ordem mudou após uma reordenação recusada: %v", got)
	}
	// Id inexistente também é recusado.
	if err := env.Store.ReorderSources(ctx, []int64{a.ID, b.ID, 999999}); err == nil {
		t.Error("id inexistente deveria ser recusado")
	}
}

func TestCredencialDaFonteFicaCifradaNoBanco(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	src := criarFonte(t, env, "Fonte com credencial")

	const senha = "senha-secreta-da-fonte-123"
	plaintext, err := json.Marshal(map[string]string{"password": senha})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sealed, err := env.Crypto.Seal(plaintext, cryptobox.SourceCredentialAAD(src.ID))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := env.Store.SetSourceCredential(ctx, src.ID, "usuario-da-fonte", sealed, 1); err != nil {
		t.Fatalf("SetSourceCredential: %v", err)
	}

	// A senha em claro não pode existir em lugar nenhum da tabela.
	var raw []byte
	if err := env.Pool.QueryRow(ctx, `SELECT secret_enc FROM source_credentials WHERE source_id = $1`, src.ID).Scan(&raw); err != nil {
		t.Fatalf("lendo secret_enc: %v", err)
	}
	if bytes.Contains(raw, []byte(senha)) {
		t.Fatal("a senha da fonte está em claro no banco")
	}

	cred, err := env.Store.GetSourceCredential(ctx, src.ID)
	if err != nil {
		t.Fatalf("GetSourceCredential: %v", err)
	}
	opened, err := env.Crypto.Open(cred.SecretEnc, cryptobox.SourceCredentialAAD(src.ID))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(opened, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["password"] != senha {
		t.Fatalf("senha decifrada = %q", decoded["password"])
	}

	// A listagem sinaliza a existência da credencial sem devolver o segredo.
	lista, _ := env.Store.ListSources(ctx)
	if len(lista) != 1 || !lista[0].HasCredentials {
		t.Fatalf("has_credentials deveria ser true: %+v", lista)
	}

	// Substituir a credencial não cria uma segunda linha.
	if _, err := env.Store.SetSourceCredential(ctx, src.ID, "outro-usuario", sealed, 1); err != nil {
		t.Fatalf("SetSourceCredential (substituição): %v", err)
	}
	var n int
	if err := env.Pool.QueryRow(ctx, `SELECT count(*) FROM source_credentials WHERE source_id = $1`, src.ID).Scan(&n); err != nil {
		t.Fatalf("contando credenciais: %v", err)
	}
	if n != 1 {
		t.Fatalf("há %d credenciais para a fonte, esperava 1", n)
	}

	// Apagar a fonte leva a credencial junto (ON DELETE CASCADE).
	if err := env.Store.DeleteSource(ctx, src.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	if err := env.Pool.QueryRow(ctx, `SELECT count(*) FROM source_credentials`).Scan(&n); err != nil {
		t.Fatalf("contando credenciais: %v", err)
	}
	if n != 0 {
		t.Fatalf("credencial órfã sobrou após remover a fonte: %d", n)
	}
}

func TestEventosGravamEFiltram(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	src := criarFonte(t, env, "Fonte")

	must(t, env.Store.InsertEvent(ctx, store.NewEvent{
		NodeID: "node-1", Level: "info", Category: "source",
		Message: "fonte criada", Actor: "admin", SourceID: &src.ID,
		Data: map[string]any{"kind": "m3u"},
	}))
	must(t, env.Store.InsertEvent(ctx, store.NewEvent{
		NodeID: "node-1", Level: "warn", Category: "auth", Message: "login inválido",
	}))

	todos, err := env.Store.ListEvents(ctx, store.EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(todos) != 2 {
		t.Fatalf("ListEvents devolveu %d, esperava 2", len(todos))
	}

	porCategoria, _ := env.Store.ListEvents(ctx, store.EventFilter{Category: "source"})
	if len(porCategoria) != 1 || porCategoria[0].Category != "source" {
		t.Fatalf("filtro por categoria falhou: %+v", porCategoria)
	}
	if porCategoria[0].SourceID == nil || *porCategoria[0].SourceID != src.ID {
		t.Error("source_id não foi preservado")
	}
	if string(porCategoria[0].Data) == "" || string(porCategoria[0].Data) == "null" {
		t.Errorf("data do evento veio vazia: %s", porCategoria[0].Data)
	}

	porNivel, _ := env.Store.ListEvents(ctx, store.EventFilter{Level: "warn"})
	if len(porNivel) != 1 || porNivel[0].Level != "warn" {
		t.Fatalf("filtro por nível falhou: %+v", porNivel)
	}

	futuro := time.Now().Add(time.Hour)
	nenhum, _ := env.Store.ListEvents(ctx, store.EventFilter{Since: &futuro})
	if len(nenhum) != 0 {
		t.Errorf("filtro since falhou: %d eventos", len(nenhum))
	}

	// Nível fora do domínio é recusado pelo schema.
	if err := env.Store.InsertEvent(ctx, store.NewEvent{
		NodeID: "node-1", Level: "critico", Category: "x", Message: "y",
	}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("nível inválido: esperava ErrInvalid, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------

func criarUsuario(t *testing.T, env *testEnv, username, role string) *store.User {
	t.Helper()
	hash, err := auth.HashPassword("senha-de-teste-longa")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	u, err := env.Store.CreateUser(context.Background(), username, hash, role)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func criarFonte(t *testing.T, env *testEnv, name string) *store.Source {
	t.Helper()
	s, err := env.Store.CreateSource(context.Background(), store.NewSource{
		Name: name, Kind: store.SourceKindM3U, BaseURL: "http://exemplo.tld/lista.m3u",
	})
	if err != nil {
		t.Fatalf("CreateSource(%s): %v", name, err)
	}
	return s
}

func nomes(list []store.Source) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.Name
	}
	return out
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
}
