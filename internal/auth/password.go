// Package auth cuida de senhas, sessões, tokens de API e autorização.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parâmetros do Argon2id para senhas de administrador. Custo deliberadamente alto:
// login é raro e não está no caminho crítico de streaming.
//
// NOTA: as credenciais de streaming (D7) NÃO usam este KDF — elas são verificadas a cada
// requisição de vídeo e usam HMAC, conforme documentado na doc 01 §6 D7.
const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // 64 MiB
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// MinPasswordLength é o tamanho mínimo aceito para senha de usuário.
const MinPasswordLength = 12

var (
	// ErrPasswordTooShort indica senha abaixo do mínimo.
	ErrPasswordTooShort = fmt.Errorf("a senha precisa ter ao menos %d caracteres", MinPasswordLength)
	// ErrInvalidHash indica hash armazenado em formato inválido.
	ErrInvalidHash = errors.New("hash de senha em formato inválido")
	// ErrIncompatibleVersion indica hash gerado por outra versão do Argon2.
	ErrIncompatibleVersion = errors.New("hash de senha gerado por versão incompatível do Argon2")
)

// HashPassword produz um hash Argon2id no formato PHC.
func HashPassword(password string) (string, error) {
	if len([]rune(password)) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("gerando salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword compara a senha com o hash em tempo constante.
//
// Devolve (false, nil) quando a senha simplesmente não confere, e (false, err) quando o
// hash armazenado está corrompido — casos que o chamador deve tratar de forma diferente.
func VerifyPassword(password, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrInvalidHash
	}
	if version != argon2.Version {
		return false, ErrIncompatibleVersion
	}

	memory, time, threads, err := parseArgonParams(parts[3])
	if err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHash
	}
	if len(salt) == 0 || len(want) == 0 {
		return false, ErrInvalidHash
	}

	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseArgonParams(s string) (memory, time uint32, threads uint8, err error) {
	fields := strings.Split(s, ",")
	if len(fields) != 3 {
		return 0, 0, 0, ErrInvalidHash
	}
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return 0, 0, 0, ErrInvalidHash
		}
		n, convErr := strconv.ParseUint(v, 10, 32)
		if convErr != nil || n == 0 {
			return 0, 0, 0, ErrInvalidHash
		}
		switch k {
		case "m":
			memory = uint32(n)
		case "t":
			time = uint32(n)
		case "p":
			if n > 255 {
				return 0, 0, 0, ErrInvalidHash
			}
			threads = uint8(n)
		default:
			return 0, 0, 0, ErrInvalidHash
		}
	}
	if memory == 0 || time == 0 || threads == 0 {
		return 0, 0, 0, ErrInvalidHash
	}
	return memory, time, threads, nil
}

// GeneratePassword produz uma senha aleatória forte (usada no bootstrap do admin).
func GeneratePassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gerando senha: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
