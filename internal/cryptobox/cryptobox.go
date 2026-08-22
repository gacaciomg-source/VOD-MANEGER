// Package cryptobox cifra segredos em repouso (credenciais de fontes) com AES-256-GCM.
//
// Formato do blob armazenado:
//
//	byte 0      : versão do formato (0x01)
//	bytes 1..12 : nonce (12 bytes, aleatório por operação)
//	bytes 13..N : ciphertext + tag GCM
//
// A versão da CHAVE não vai no blob: fica na coluna key_version da tabela, para permitir
// rotação com re-cifra controlada e auditável linha a linha.
//
// Todo Seal/Open recebe um "associated data" (AAD) que amarra o ciphertext ao registro
// dono do segredo (ex.: "source_credential:42"). Um blob copiado para outra linha não
// abre — o AAD não bate.
package cryptobox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeySize é o tamanho exigido da chave mestra: AES-256.
const KeySize = 32

const formatVersion byte = 0x01

var (
	// ErrKeySize indica chave mestra com tamanho inválido.
	ErrKeySize = errors.New("cryptobox: a chave mestra precisa ter exatamente 32 bytes")
	// ErrMalformed indica blob truncado ou com versão desconhecida.
	ErrMalformed = errors.New("cryptobox: blob cifrado malformado")
	// ErrDecrypt indica falha de autenticação (chave errada, AAD errado ou adulteração).
	ErrDecrypt = errors.New("cryptobox: falha ao decifrar (chave, AAD ou dado adulterado)")
)

// Box cifra e decifra segredos com uma chave mestra.
type Box struct {
	aead cipher.AEAD
}

// New cria um Box a partir da chave mestra de 32 bytes.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w (recebido: %d)", ErrKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cryptobox: criando cifra: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cryptobox: criando GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

// ParseKey decodifica uma chave mestra em base64 (padrão ou url-safe).
func ParseKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("cryptobox: chave mestra não é base64 válido: %w", err)
		}
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w (recebido: %d)", ErrKeySize, len(key))
	}
	return key, nil
}

// GenerateKey produz uma chave mestra nova, já em base64, para uso em configuração.
func GenerateKey() (string, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("cryptobox: gerando chave: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// Seal cifra plaintext amarrando o resultado ao AAD informado.
func (b *Box) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("cryptobox: gerando nonce: %w", err)
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+b.aead.Overhead())
	out = append(out, formatVersion)
	out = append(out, nonce...)
	return b.aead.Seal(out, nonce, plaintext, aad), nil
}

// Open decifra um blob produzido por Seal, exigindo o mesmo AAD.
func (b *Box) Open(blob, aad []byte) ([]byte, error) {
	nonceSize := b.aead.NonceSize()
	if len(blob) < 1+nonceSize+b.aead.Overhead() {
		return nil, ErrMalformed
	}
	if blob[0] != formatVersion {
		return nil, fmt.Errorf("%w: versão %#x desconhecida", ErrMalformed, blob[0])
	}
	nonce := blob[1 : 1+nonceSize]
	ciphertext := blob[1+nonceSize:]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// SourceCredentialAAD é o AAD canônico das credenciais de fonte.
func SourceCredentialAAD(sourceID int64) []byte {
	return []byte(fmt.Sprintf("source_credential:%d", sourceID))
}

// StreamCredentialAAD é o AAD canônico das credenciais de SAÍDA.
//
// Liga o texto cifrado ao usuário dono dele: mover o blob de uma linha para outra no
// banco não produz uma senha válida, produz falha de decifragem.
func StreamCredentialAAD(username string) []byte {
	return []byte("stream_credential:" + username)
}

// NuvemAAD é o AAD canônico das credenciais de uma conta de nuvem.
//
// Liga o texto cifrado ao nome da conta: mover o blob de uma linha para outra no banco não
// produz credencial válida, produz falha de decifragem. É também o motivo de o nome de uma
// conta não poder ser alterado depois de cadastrada.
func NuvemAAD(nome string) []byte {
	return []byte("nuvem:" + nome)
}
