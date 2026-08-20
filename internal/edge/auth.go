// Package edge é o plano de dados: entrega os bytes de vídeo ao cliente.
//
// É o caminho crítico do sistema. Tudo aqui é otimizado para latência e para não fazer
// trabalho desnecessário entre receber a requisição e enviar o primeiro byte.
package edge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"vodmanager/internal/store"
)

var (
	// ErrCredencialInvalida cobre usuário inexistente e senha errada. É um erro único de
	// propósito: a resposta não deve revelar qual dos dois ocorreu.
	ErrCredencialInvalida = errors.New("credencial de streaming inválida")
	// ErrCredencialRevogada indica credencial que existia e foi desativada.
	ErrCredencialRevogada = errors.New("credencial revogada ou expirada")
	// ErrOrigemNaoPermitida indica IP fora dos CIDRs autorizados.
	ErrOrigemNaoPermitida = errors.New("origem não autorizada para esta credencial")
)

// ttlCacheCredencial é por quanto tempo uma credencial verificada fica em memória.
//
// Curto de propósito: é o atraso máximo entre revogar uma credencial no painel e ela
// parar de funcionar. Cinco segundos é imperceptível para quem revoga e elimina uma
// consulta ao banco por requisição de vídeo.
const ttlCacheCredencial = 5 * time.Second

// Authenticator valida as credenciais de saída.
//
// Mantém um cache curto em memória: sem ele, cada requisição de vídeo — inclusive cada
// seek do player, que gera uma nova requisição com Range — faria uma consulta ao banco
// antes do primeiro byte.
type Authenticator struct {
	store     *store.Store
	chaveHMAC []byte

	mu    sync.RWMutex
	cache map[string]entradaCache
}

type entradaCache struct {
	cred   *store.StreamCredential
	expira time.Time
}

// NewAuthenticator cria o autenticador.
//
// A chave de assinatura é DERIVADA da chave mestra, não é a chave mestra: assim, um
// vazamento de URL assinada não expõe nada sobre as credenciais das fontes.
func NewAuthenticator(st *store.Store, chaveMestra []byte) *Authenticator {
	mac := hmac.New(sha256.New, chaveMestra)
	mac.Write([]byte("vodmanager:stream-credentials:v1"))

	return &Authenticator{
		store:     st,
		chaveHMAC: mac.Sum(nil),
		cache:     map[string]entradaCache{},
	}
}

// HashSenha produz o valor guardado no banco para uma senha de streaming.
func (a *Authenticator) HashSenha(senha string) []byte {
	mac := hmac.New(sha256.New, a.chaveHMAC)
	mac.Write([]byte(senha))
	return mac.Sum(nil)
}

// Autenticar valida usuário e senha vindos da URL.
func (a *Authenticator) Autenticar(ctx context.Context, username, senha, clientIP string) (*store.StreamCredential, error) {
	username = strings.TrimSpace(username)
	if username == "" || senha == "" {
		return nil, ErrCredencialInvalida
	}

	cred, err := a.buscar(ctx, username)
	if err != nil {
		return nil, err
	}

	// Comparação em tempo constante, sempre — mesmo quando a credencial já está inativa,
	// para o tempo de resposta não revelar o estado dela.
	senhaOK := hmac.Equal(cred.PasswordHMAC, a.HashSenha(senha))
	if !senhaOK {
		return nil, ErrCredencialInvalida
	}
	if !cred.Ativa(time.Now()) {
		return nil, ErrCredencialRevogada
	}
	if !origemPermitida(cred.AllowedCIDRs, clientIP) {
		return nil, ErrOrigemNaoPermitida
	}
	return cred, nil
}

// buscar consulta o cache e cai no banco quando necessário.
func (a *Authenticator) buscar(ctx context.Context, username string) (*store.StreamCredential, error) {
	a.mu.RLock()
	e, ok := a.cache[username]
	a.mu.RUnlock()
	if ok && time.Now().Before(e.expira) {
		if e.cred == nil {
			return nil, ErrCredencialInvalida
		}
		return e.cred, nil
	}

	cred, err := a.store.GetStreamCredentialByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Cacheia a ausência também: senão, um cliente com credencial errada
			// consulta o banco a cada tentativa.
			a.guardar(username, nil)
			return nil, ErrCredencialInvalida
		}
		return nil, err
	}
	a.guardar(username, cred)
	return cred, nil
}

func (a *Authenticator) guardar(username string, cred *store.StreamCredential) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache[username] = entradaCache{cred: cred, expira: time.Now().Add(ttlCacheCredencial)}
}

// Invalidar remove uma credencial do cache imediatamente.
//
// Chamado ao revogar: sem isso, a revogação levaria até o TTL do cache para valer.
func (a *Authenticator) Invalidar(username string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.cache, username)
}

// InvalidarTudo limpa o cache inteiro.
func (a *Authenticator) InvalidarTudo() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache = map[string]entradaCache{}
}

// origemPermitida verifica o IP do cliente contra a lista de CIDRs.
// Lista vazia significa "qualquer origem".
func origemPermitida(cidrs []string, clientIP string) bool {
	if len(cidrs) == 0 {
		return true
	}
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		_, rede, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			continue
		}
		if rede.Contains(ip) {
			return true
		}
	}
	return false
}

// --- URLs assinadas ----------------------------------------------------------

// AssinarURL produz a assinatura de um caminho com validade.
//
// É o modo alternativo ao par usuário/senha: útil para links temporários entregues a um
// player, sem criar credencial permanente.
func (a *Authenticator) AssinarURL(caminho string, validade time.Duration) (expira int64, assinatura string) {
	expira = time.Now().Add(validade).Unix()
	return expira, a.assinatura(caminho, expira)
}

// VerificarAssinatura confere uma URL assinada.
func (a *Authenticator) VerificarAssinatura(caminho, expiraStr, assinatura string) error {
	expira, err := strconv.ParseInt(expiraStr, 10, 64)
	if err != nil {
		return ErrCredencialInvalida
	}
	if time.Now().Unix() > expira {
		return ErrCredencialRevogada
	}
	esperada := a.assinatura(caminho, expira)
	if !hmac.Equal([]byte(esperada), []byte(assinatura)) {
		return ErrCredencialInvalida
	}
	return nil
}

func (a *Authenticator) assinatura(caminho string, expira int64) string {
	mac := hmac.New(sha256.New, a.chaveHMAC)
	fmt.Fprintf(mac, "%s|%d", caminho, expira)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
