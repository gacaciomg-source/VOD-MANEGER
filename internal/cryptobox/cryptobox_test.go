package cryptobox

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func newTestBox(t *testing.T) *Box {
	t.Helper()
	encoded, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	key, err := ParseKey(encoded)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	box, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return box
}

func TestSealOpenRoundTrip(t *testing.T) {
	box := newTestBox(t)
	aad := SourceCredentialAAD(42)
	secret := []byte(`{"password":"s3nh4-da-fonte","extra":{"token":"abc"}}`)

	blob, err := box.Seal(secret, aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(blob, []byte("s3nh4-da-fonte")) {
		t.Fatal("o segredo aparece em claro dentro do blob cifrado")
	}

	got, err := box.Open(blob, aad)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("Open devolveu %q, esperava %q", got, secret)
	}
}

func TestSealProduzBlobsDiferentesParaMesmoTexto(t *testing.T) {
	box := newTestBox(t)
	aad := SourceCredentialAAD(1)
	a, err := box.Seal([]byte("mesma-senha"), aad)
	if err != nil {
		t.Fatalf("Seal a: %v", err)
	}
	b, err := box.Seal([]byte("mesma-senha"), aad)
	if err != nil {
		t.Fatalf("Seal b: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("nonce não está sendo aleatorizado: dois Seal iguais produziram o mesmo blob")
	}
}

// Este é o teste que justifica o AAD: um blob copiado da fonte 42 para a linha da
// fonte 99 não pode abrir.
func TestOpenFalhaComAADDeOutroRegistro(t *testing.T) {
	box := newTestBox(t)
	blob, err := box.Seal([]byte("segredo"), SourceCredentialAAD(42))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := box.Open(blob, SourceCredentialAAD(99)); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("esperava ErrDecrypt ao trocar o AAD, obtive %v", err)
	}
}

func TestOpenFalhaComChaveDiferente(t *testing.T) {
	boxA := newTestBox(t)
	boxB := newTestBox(t)
	aad := SourceCredentialAAD(7)
	blob, err := boxA.Seal([]byte("segredo"), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := boxB.Open(blob, aad); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("esperava ErrDecrypt com outra chave, obtive %v", err)
	}
}

func TestOpenDetectaAdulteracao(t *testing.T) {
	box := newTestBox(t)
	aad := SourceCredentialAAD(7)
	blob, err := box.Seal([]byte("segredo importante"), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	tampered := bytes.Clone(blob)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := box.Open(tampered, aad); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("esperava ErrDecrypt em blob adulterado, obtive %v", err)
	}
}

func TestOpenRejeitaBlobMalformado(t *testing.T) {
	box := newTestBox(t)
	aad := SourceCredentialAAD(1)
	blob, err := box.Seal([]byte("x"), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	cases := map[string][]byte{
		"vazio":               {},
		"curto demais":        blob[:5],
		"versão desconhecida": append([]byte{0x99}, blob[1:]...),
	}
	for name, bad := range cases {
		if _, err := box.Open(bad, aad); err == nil {
			t.Errorf("%s: esperava erro", name)
		}
	}
}

func TestChaveDeTamanhoInvalido(t *testing.T) {
	if _, err := New(make([]byte, 16)); !errors.Is(err, ErrKeySize) {
		t.Errorf("New com 16 bytes: esperava ErrKeySize, obtive %v", err)
	}
	short := base64.StdEncoding.EncodeToString(make([]byte, 31))
	if _, err := ParseKey(short); !errors.Is(err, ErrKeySize) {
		t.Errorf("ParseKey com 31 bytes: esperava ErrKeySize, obtive %v", err)
	}
	if _, err := ParseKey("isto não é base64!!!"); err == nil {
		t.Error("ParseKey com lixo: esperava erro")
	}
}

func TestParseKeyAceitaBase64URLSafe(t *testing.T) {
	raw := make([]byte, KeySize)
	for i := range raw {
		raw[i] = byte(i)
	}
	urlSafe := base64.RawURLEncoding.EncodeToString(raw)
	key, err := ParseKey(urlSafe)
	if err != nil {
		t.Fatalf("ParseKey url-safe: %v", err)
	}
	if !bytes.Equal(key, raw) {
		t.Fatal("ParseKey url-safe devolveu chave diferente")
	}
}

func TestGenerateKeyTemEntropia(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 32; i++ {
		k, err := GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		if seen[k] {
			t.Fatal("GenerateKey repetiu uma chave")
		}
		seen[k] = true
	}
}

func TestSourceCredentialAAD(t *testing.T) {
	if got := string(SourceCredentialAAD(42)); !strings.HasSuffix(got, ":42") {
		t.Errorf("AAD = %q, esperava terminar em :42", got)
	}
}
