package store

import (
	"testing"
	"time"
)

func gb(n int64) *int64 { b := n * 1024 * 1024 * 1024; return &b }

// A cota é o que sustenta a venda por volume. Errar para o lado permissivo entrega banda
// de graça; errar para o restritivo corta um cliente que ainda tem pacote.
func TestCotaDeBanda(t *testing.T) {
	agora := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	casos := []struct {
		nome     string
		cred     StreamCredential
		esgotada bool
	}{
		{
			nome:     "sem cota nunca esgota",
			cred:     StreamCredential{Enabled: true, BytesCiclo: 999 << 30},
			esgotada: false,
		},
		{
			nome: "abaixo da cota",
			cred: StreamCredential{Enabled: true, BytesLimit: gb(4),
				BytesCiclo: 3 << 30, CicloInicio: agora},
			esgotada: false,
		},
		{
			nome: "exatamente na cota já bloqueia",
			cred: StreamCredential{Enabled: true, BytesLimit: gb(4),
				BytesCiclo: 4 << 30, CicloInicio: agora},
			esgotada: true,
		},
		{
			nome: "acima da cota",
			cred: StreamCredential{Enabled: true, BytesLimit: gb(4),
				BytesCiclo: 5 << 30, CicloInicio: agora},
			esgotada: true,
		},
		{
			// Balde único não renova: os 4 GB acabaram e acabou.
			nome: "sem ciclo, consumo antigo continua contando",
			cred: StreamCredential{Enabled: true, BytesLimit: gb(4), Ciclo: "nenhum",
				BytesCiclo: 5 << 30, CicloInicio: agora.AddDate(0, -3, 0)},
			esgotada: true,
		},
		{
			// Mensal renova na virada do mês, sem precisar de ninguém para zerar.
			nome: "mensal libera quando o mês vira",
			cred: StreamCredential{Enabled: true, BytesLimit: gb(4), Ciclo: "mensal",
				BytesCiclo: 5 << 30, CicloInicio: agora.AddDate(0, -1, 0)},
			esgotada: false,
		},
		{
			nome: "mensal ainda no mesmo mês continua bloqueado",
			cred: StreamCredential{Enabled: true, BytesLimit: gb(4), Ciclo: "mensal",
				BytesCiclo: 5 << 30, CicloInicio: agora.AddDate(0, 0, -5)},
			esgotada: true,
		},
	}

	for _, c := range casos {
		if got := c.cred.CotaEsgotada(agora); got != c.esgotada {
			t.Errorf("%s: esgotada = %v, esperava %v", c.nome, got, c.esgotada)
		}
		// Ativa precisa refletir a cota: é ela que o caminho do vídeo consulta.
		if c.esgotada && c.cred.Ativa(agora) {
			t.Errorf("%s: Ativa devolveu true com a cota esgotada", c.nome)
		}
	}
}

// O restante é o que o painel mostra ao cliente. Zero e "sem limite" precisam ser
// distinguíveis, senão o painel exibiria "0 B restantes" para quem não tem cota nenhuma.
func TestBytesRestantes(t *testing.T) {
	agora := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	semCota := StreamCredential{Enabled: true, BytesCiclo: 10 << 30}
	if got := semCota.BytesRestantes(agora); got != -1 {
		t.Errorf("sem cota = %d, esperava -1 (sem limite)", got)
	}

	metade := StreamCredential{Enabled: true, BytesLimit: gb(4), BytesCiclo: 2 << 30, CicloInicio: agora}
	if got := metade.BytesRestantes(agora); got != 2<<30 {
		t.Errorf("restante = %d, esperava %d", got, int64(2)<<30)
	}

	// Estourado devolve zero, nunca negativo: um número negativo apareceria no painel.
	estourado := StreamCredential{Enabled: true, BytesLimit: gb(4), BytesCiclo: 9 << 30, CicloInicio: agora}
	if got := estourado.BytesRestantes(agora); got != 0 {
		t.Errorf("estourado = %d, esperava 0", got)
	}

	// Mensal com o mês virado mostra a cota cheia de novo.
	renovado := StreamCredential{Enabled: true, BytesLimit: gb(4), Ciclo: "mensal",
		BytesCiclo: 9 << 30, CicloInicio: agora.AddDate(0, -1, 0)}
	if got := renovado.BytesRestantes(agora); got != 4<<30 {
		t.Errorf("renovado = %d, esperava %d", got, int64(4)<<30)
	}
}

// A cota não pode mascarar uma revogação: quem foi cortado continua cortado, com ou sem
// pacote sobrando.
func TestRevogadaContinuaBloqueadaMesmoComCota(t *testing.T) {
	agora := time.Now()
	revogada := StreamCredential{Enabled: true, RevokedAt: &agora,
		BytesLimit: gb(100), BytesCiclo: 0, CicloInicio: agora}
	if revogada.Ativa(agora) {
		t.Error("credencial revogada ficou ativa por ter cota sobrando")
	}
}
