package auth

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHashPasswordVerifyRoundTrip(t *testing.T) {
	const senha = "senha-de-administrador-forte"
	hash, err := HashPassword(senha)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(hash, senha) {
		t.Fatal("a senha aparece em claro dentro do hash")
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash não está em formato PHC argon2id: %q", hash)
	}

	ok, err := VerifyPassword(senha, hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("senha correta foi rejeitada")
	}

	ok, err = VerifyPassword(senha+"x", hash)
	if err != nil {
		t.Fatalf("VerifyPassword (senha errada): %v", err)
	}
	if ok {
		t.Fatal("senha errada foi aceita")
	}
}

func TestHashPasswordUsaSaltAleatorio(t *testing.T) {
	a, err := HashPassword("mesma-senha-longa-aqui")
	if err != nil {
		t.Fatalf("HashPassword a: %v", err)
	}
	b, err := HashPassword("mesma-senha-longa-aqui")
	if err != nil {
		t.Fatalf("HashPassword b: %v", err)
	}
	if a == b {
		t.Fatal("dois hashes da mesma senha ficaram idênticos: salt não é aleatório")
	}
}

func TestHashPasswordRejeitaSenhaCurta(t *testing.T) {
	if _, err := HashPassword("curta"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("esperava ErrPasswordTooShort, obtive %v", err)
	}
}

func TestVerifyPasswordRejeitaHashInvalido(t *testing.T) {
	cases := map[string]string{
		"vazio":            "",
		"sem cifras":       "texto-qualquer",
		"algoritmo errado": "$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"campos de menos":  "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA",
		"params quebrados": "$argon2id$v=19$m=abc,t=3,p=4$c2FsdA$aGFzaA",
		"param zerado":     "$argon2id$v=19$m=0,t=3,p=4$c2FsdA$aGFzaA",
		"base64 inválido":  "$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA",
	}
	for name, hash := range cases {
		ok, err := VerifyPassword("qualquer-senha-aqui", hash)
		if ok {
			t.Errorf("%s: aceitou senha contra hash inválido", name)
		}
		if err == nil {
			t.Errorf("%s: esperava erro de hash inválido", name)
		}
	}
}

func TestVerifyPasswordDetectaVersaoIncompativel(t *testing.T) {
	_, err := VerifyPassword("qualquer-senha-aqui", "$argon2id$v=16$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNo")
	if !errors.Is(err, ErrIncompatibleVersion) {
		t.Fatalf("esperava ErrIncompatibleVersion, obtive %v", err)
	}
}

func TestGeneratePasswordAtendeOMinimo(t *testing.T) {
	p, err := GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword: %v", err)
	}
	if len(p) < MinPasswordLength {
		t.Fatalf("senha gerada tem %d caracteres, mínimo é %d", len(p), MinPasswordLength)
	}
	if _, err := HashPassword(p); err != nil {
		t.Fatalf("senha gerada foi rejeitada pelo HashPassword: %v", err)
	}
}

func TestNewTokenNaoPersisteOSegredo(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if len(tok.Hash) != 32 {
		t.Fatalf("hash tem %d bytes, esperava 32", len(tok.Hash))
	}
	if strings.Contains(string(tok.Hash), tok.Plain) {
		t.Fatal("o token em claro aparece dentro do hash")
	}
	if !EqualHash(tok.Hash, HashToken(tok.Plain)) {
		t.Fatal("HashToken não reproduz o hash gerado por NewToken")
	}
	if !strings.HasPrefix(tok.Plain, tok.Prefix) || len(tok.Prefix) != TokenPrefixLen {
		t.Fatalf("prefixo %q inconsistente com o token %q", tok.Prefix, tok.Plain)
	}
}

func TestNewTokenNaoRepete(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 64; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if seen[tok.Plain] {
			t.Fatal("NewToken repetiu um token")
		}
		seen[tok.Plain] = true
	}
}

func TestHashTokenIgnoraEspacosEmVolta(t *testing.T) {
	if !EqualHash(HashToken("abc"), HashToken("  abc\n")) {
		t.Fatal("HashToken deveria ignorar espaços nas bordas")
	}
	if EqualHash(HashToken("abc"), HashToken("abd")) {
		t.Fatal("tokens diferentes geraram o mesmo hash")
	}
}

func TestLoginLimiterBloqueiaAposOMaximo(t *testing.T) {
	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	lim := NewLoginLimiter(3, time.Minute)
	lim.now = func() time.Time { return clock }

	for i := 1; i <= 3; i++ {
		if ok, _ := lim.Allow("admin|10.0.0.1"); !ok {
			t.Fatalf("tentativa %d deveria ser permitida", i)
		}
	}
	ok, retry := lim.Allow("admin|10.0.0.1")
	if ok {
		t.Fatal("quarta tentativa deveria ser bloqueada")
	}
	if retry <= 0 || retry > time.Minute {
		t.Fatalf("retryAfter fora do esperado: %v", retry)
	}

	// Outra chave não é afetada.
	if ok, _ := lim.Allow("admin|10.0.0.2"); !ok {
		t.Fatal("IP diferente não deveria estar bloqueado")
	}
}

func TestLoginLimiterLiberaAposAJanela(t *testing.T) {
	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	lim := NewLoginLimiter(2, time.Minute)
	lim.now = func() time.Time { return clock }

	lim.Allow("k")
	lim.Allow("k")
	if ok, _ := lim.Allow("k"); ok {
		t.Fatal("deveria estar bloqueado")
	}

	clock = clock.Add(61 * time.Second)
	if ok, _ := lim.Allow("k"); !ok {
		t.Fatal("deveria liberar após a janela expirar")
	}
}

func TestLoginLimiterResetELimpeza(t *testing.T) {
	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	lim := NewLoginLimiter(1, time.Minute)
	lim.now = func() time.Time { return clock }

	lim.Allow("k")
	if ok, _ := lim.Allow("k"); ok {
		t.Fatal("deveria estar bloqueado")
	}
	lim.Reset("k")
	if ok, _ := lim.Allow("k"); !ok {
		t.Fatal("Reset deveria liberar a chave")
	}

	clock = clock.Add(2 * time.Minute)
	if removed := lim.Sweep(); removed != 1 {
		t.Fatalf("Sweep removeu %d chaves, esperava 1", removed)
	}
	if got := len(lim.buckets); got != 0 {
		t.Fatalf("mapa ficou com %d chaves após Sweep, esperava 0", got)
	}
}

func TestLoginLimiterConcorrente(t *testing.T) {
	lim := NewLoginLimiter(100, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lim.Allow("mesma-chave")
			lim.Sweep()
		}()
	}
	wg.Wait()
}
