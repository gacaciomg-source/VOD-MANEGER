package auth

import (
	"sync"
	"time"
)

// LoginLimiter limita tentativas de login por chave (usuário+IP), com janela deslizante.
//
// É in-process de propósito: na v1 há um único processo. Quando houver múltiplos nós, a
// contagem migra para a interface Coordinator (ver doc 05, "Preparação para Manager/Nodes"),
// sem alterar quem chama.
type LoginLimiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string][]time.Time
}

// NewLoginLimiter cria um limitador que permite `max` tentativas por `window`.
func NewLoginLimiter(max int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{
		max:     max,
		window:  window,
		now:     time.Now,
		buckets: make(map[string][]time.Time),
	}
}

// Allow registra uma tentativa e informa se ela é permitida.
// Devolve também quanto falta para a próxima tentativa quando bloqueado.
func (l *LoginLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)

	attempts := l.buckets[key][:0:0]
	for _, t := range l.buckets[key] {
		if t.After(cutoff) {
			attempts = append(attempts, t)
		}
	}

	if len(attempts) >= l.max {
		l.buckets[key] = attempts
		retryAfter := attempts[0].Add(l.window).Sub(now)
		if retryAfter < 0 {
			retryAfter = 0
		}
		return false, retryAfter
	}

	l.buckets[key] = append(attempts, now)
	return true, 0
}

// Reset limpa o histórico de uma chave. Chamado após login bem-sucedido.
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// Sweep remove chaves cujas tentativas já saíram da janela. Deve rodar periodicamente
// para o mapa não crescer sem limite sob ataque distribuído.
func (l *LoginLimiter) Sweep() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := l.now().Add(-l.window)
	removed := 0
	for key, attempts := range l.buckets {
		kept := attempts[:0]
		for _, t := range attempts {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(l.buckets, key)
			removed++
			continue
		}
		l.buckets[key] = kept
	}
	return removed
}
