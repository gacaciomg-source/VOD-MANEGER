package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"net/url"
	"strings"
)

// queryEscape codifica um valor para uso em query string.
func queryEscape(s string) string { return url.QueryEscape(s) }

// pathEscape codifica um valor para uso em um segmento de path.
//
// Importante para credenciais: um usuário com "/" ou "?" quebraria a URL montada.
func pathEscape(s string) string { return url.PathEscape(s) }

// streamHasher calcula o digest do conteúdo enquanto ele é lido.
//
// Sem isso, saber se a playlist mudou exigiria uma segunda passagem ou manter o arquivo
// inteiro em memória — as duas coisas que o parser em streaming existe para evitar.
type streamHasher struct {
	r io.Reader
	h hash.Hash
	n int64
}

func newStreamHasher(r io.Reader) *streamHasher {
	return &streamHasher{r: r, h: sha256.New()}
}

func (s *streamHasher) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.h.Write(p[:n])
		s.n += int64(n)
	}
	return n, err
}

// Digest devolve o hash do que foi lido até agora.
func (s *streamHasher) Digest() string { return hex.EncodeToString(s.h.Sum(nil)) }

// Bytes devolve quantos bytes passaram pelo leitor.
func (s *streamHasher) Bytes() int64 { return s.n }

// NormalizeBaseURL limpa a URL base de uma fonte.
func NormalizeBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}
