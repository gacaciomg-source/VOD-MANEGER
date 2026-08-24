package edge

import (
	"context"
	"testing"
	"time"

	"vodmanager/internal/store"
)

func credencialCom(limite int) *store.StreamCredential {
	return &store.StreamCredential{ID: 1, Name: "teste", MaxConnections: &limite}
}

// TestVagaEsperaConexaoQueEstaFechando é a guarda do defeito intermitente.
//
// Quem consome não abre UMA conexão por filme: um painel que faz direct stream lê em
// pedaços — abre, puxa um trecho, fecha, reabre. A cada reabertura a conexão anterior ainda
// pode estar terminando, e por uma fração de segundo a MESMA pessoa ocupa duas vagas.
//
// Recusando de imediato, o espectador vê o vídeo morrer sem que haja gente demais
// assistindo. E como depende de as duas se cruzarem no tempo, a falha é intermitente — o
// que é exatamente o que torna esse tipo de defeito difícil de acreditar.
func TestVagaEsperaConexaoQueEstaFechando(t *testing.T) {
	c := NovoContadorConexoes()
	cred := credencialCom(1)

	liberar, ok := c.Ocupar(cred)
	if !ok {
		t.Fatal("a primeira conexão deveria entrar")
	}

	// A conexão anterior termina de fechar logo depois — como uma que já está morrendo.
	go func() {
		time.Sleep(80 * time.Millisecond)
		liberar()
	}()

	inicio := time.Now()
	segunda, ok := c.OcuparComEspera(context.Background(), cred)
	if !ok {
		t.Fatal("a segunda conexão deveria ter esperado a vaga, não sido recusada")
	}
	segunda()

	if d := time.Since(inicio); d > esperaPorVaga {
		t.Errorf("esperou %s, mais que o prazo de %s", d, esperaPorVaga)
	}
}

// TestVagaRecusaLimiteDeVerdade: esperar não pode virar afrouxar o limite.
//
// Quem tem gente demais assistindo continua sendo recusado — só que alguns segundos depois.
// O limite continua valendo para o que ele existe para conter.
func TestVagaRecusaLimiteDeVerdade(t *testing.T) {
	original := esperaPorVaga
	defer func() { esperaPorVagaParaTeste(original) }()
	esperaPorVagaParaTeste(60 * time.Millisecond)

	c := NovoContadorConexoes()
	cred := credencialCom(1)

	if _, ok := c.Ocupar(cred); !ok {
		t.Fatal("a primeira conexão deveria entrar")
	}
	// Ninguém libera: é limite de verdade.
	if _, ok := c.OcuparComEspera(context.Background(), cred); ok {
		t.Fatal("com o limite realmente cheio, a recusa precisa acontecer")
	}
}

// TestVagaNaoSeguraClienteQueDesistiu: um espectador que fecha o player enquanto espera não
// pode segurar uma goroutine até o fim do prazo.
func TestVagaNaoSeguraClienteQueDesistiu(t *testing.T) {
	c := NovoContadorConexoes()
	cred := credencialCom(1)

	if _, ok := c.Ocupar(cred); !ok {
		t.Fatal("a primeira conexão deveria entrar")
	}

	ctx, cancelar := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancelar()
	}()

	inicio := time.Now()
	if _, ok := c.OcuparComEspera(ctx, cred); ok {
		t.Fatal("não havia vaga para dar")
	}
	if d := time.Since(inicio); d >= esperaPorVaga {
		t.Errorf("esperou %s: o cancelamento do cliente precisa encerrar a espera antes do prazo", d)
	}
}

// TestSemLimiteNaoEspera: credencial sem limite passa direto, sempre.
func TestSemLimiteNaoEspera(t *testing.T) {
	c := NovoContadorConexoes()
	cred := &store.StreamCredential{ID: 2, Name: "ilimitada"}

	for i := 0; i < 50; i++ {
		if _, ok := c.OcuparComEspera(context.Background(), cred); !ok {
			t.Fatalf("a conexão %d foi recusada numa credencial sem limite", i)
		}
	}
	if n := c.Ativas(2); n != 50 {
		t.Errorf("ativas = %d, queria 50 — sem limite ainda precisa CONTAR", n)
	}
}
