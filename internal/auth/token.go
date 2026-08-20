package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
)

// tokenBytes é a entropia dos tokens opacos (sessão e API): 32 bytes.
const tokenBytes = 32

// TokenPrefixLen é quantos caracteres do token são guardados em claro, apenas para o
// administrador reconhecer o token na listagem. Não permite reconstruir o segredo.
const TokenPrefixLen = 8

// Token é um segredo opaco recém-gerado, com seu hash de armazenamento.
type Token struct {
	// Plain é o valor entregue ao cliente. Nunca é persistido.
	Plain string
	// Hash é o SHA-256 do valor, o que vai para o banco.
	Hash []byte
	// Prefix são os primeiros caracteres, para exibição.
	Prefix string
}

// NewToken gera um token opaco novo.
//
// Guardamos SHA-256 puro (sem KDF lento) porque o token tem 256 bits de entropia gerada
// pela máquina: não há dicionário a atacar, e a verificação precisa ser barata.
func NewToken() (Token, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return Token{}, fmt.Errorf("gerando token: %w", err)
	}
	plain := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(plain))
	prefix := plain
	if len(prefix) > TokenPrefixLen {
		prefix = prefix[:TokenPrefixLen]
	}
	return Token{Plain: plain, Hash: sum[:], Prefix: prefix}, nil
}

// HashToken devolve o hash de armazenamento de um token recebido do cliente.
func HashToken(plain string) []byte {
	sum := sha256.Sum256([]byte(strings.TrimSpace(plain)))
	return sum[:]
}

// EqualHash compara dois hashes em tempo constante.
func EqualHash(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
