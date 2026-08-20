package sysinfo

import (
	"strings"
	"testing"
)

// base é uma máquina folgada: serve de ponto de partida para variar um recurso por vez.
func base() Amostra {
	return Amostra{
		Disponivel:        true,
		CPUs:              4,
		CPUPercent:        12,
		MemoriaTotal:      8 << 30,
		MemoriaDisponivel: 5 << 30,
		DiscoBytes:        80 << 30,
		DiscoLivre:        50 << 30,
		RedeSaidaBps:      10 << 20,
		RedeEntradaBps:    10 << 20,
	}
}

func nivelDe(v Veredito, recurso string) string {
	for _, p := range v.Pontos {
		if p.Recurso == recurso {
			return p.Nivel
		}
	}
	return "ausente"
}

func TestMaquinaFolgadaNaoRecomendaTroca(t *testing.T) {
	v := Avaliar(base(), Contexto{})
	if v.Nivel != "ok" {
		t.Fatalf("nível = %q, esperava ok: %+v", v.Nivel, v.Pontos)
	}
	for _, p := range v.Pontos {
		if p.Sugestao != "" {
			t.Errorf("recurso %s sugeriu ação sem necessidade: %s", p.Recurso, p.Sugestao)
		}
	}
}

func TestOPiorRecursoDefineOVeredito(t *testing.T) {
	a := base()
	// CPU ociosa, disco quase cheio: o veredito não pode ser "ok".
	a.DiscoLivre = 4 << 30 // 95% usado
	v := Avaliar(a, Contexto{})

	if v.Nivel != "critico" {
		t.Fatalf("nível = %q, esperava critico", v.Nivel)
	}
	if nivelDe(v, "Processador") != "ok" {
		t.Error("a CPU ociosa deveria continuar marcada como ok")
	}
	if nivelDe(v, "Disco") != "critico" {
		t.Error("o disco quase cheio deveria ser crítico")
	}
}

// CPU alta durante sincronização é esperado. Recomendar troca de VPS nessa hora faria o
// administrador gastar dinheiro por um pico que termina sozinho.
func TestCPUAltaDuranteSincronizacaoNaoAlarma(t *testing.T) {
	a := base()
	a.CPUPercent = 95

	semSync := Avaliar(a, Contexto{})
	if nivelDe(semSync, "Processador") != "critico" {
		t.Errorf("sem sincronização, 95%% de CPU deveria ser crítico")
	}

	comSync := Avaliar(a, Contexto{SincronizandoAgora: true})
	if nivelDe(comSync, "Processador") != "ok" {
		t.Errorf("durante sincronização, 95%% de CPU não deveria alarmar")
	}
	if comSync.Nivel != "ok" {
		t.Errorf("veredito = %q, esperava ok durante sincronização", comSync.Nivel)
	}
}

func TestSwapEmUsoEhSempreCritico(t *testing.T) {
	a := base()
	a.SwapTotal = 2 << 30
	a.SwapUsada = 1 << 30

	v := Avaliar(a, Contexto{})
	if nivelDe(v, "Swap") != "critico" {
		t.Fatalf("swap em uso deveria ser crítico: %+v", v.Pontos)
	}
	if v.Nivel != "critico" {
		t.Errorf("veredito = %q, esperava critico", v.Nivel)
	}
}

// Um pouco de swap tocado, sem pressão real, não vira alarme.
func TestSwapResidualNaoAlarma(t *testing.T) {
	a := base()
	a.SwapTotal = 2 << 30
	a.SwapUsada = 50 << 20 // 2,4% do swap

	if n := nivelDe(Avaliar(a, Contexto{}), "Swap"); n != "ausente" {
		t.Errorf("swap residual gerou observação de nível %q", n)
	}
}

func TestMemoriaApertadaSugereAumento(t *testing.T) {
	a := base()
	a.MemoriaDisponivel = 300 << 20 // ~96% em uso

	v := Avaliar(a, Contexto{})
	if nivelDe(v, "Memória") != "critico" {
		t.Fatalf("memória quase esgotada deveria ser crítica: %+v", v.Pontos)
	}
	for _, p := range v.Pontos {
		if p.Recurso == "Memória" && !strings.Contains(p.Sugestao, "memória") {
			t.Errorf("a sugestão não fala em memória: %q", p.Sugestao)
		}
	}
}

// A banda é o recurso que satura primeiro, e conta duas vezes por causa do passthrough.
func TestRedePertoDoLimiteAlerta(t *testing.T) {
	a := base()
	a.RedeSaidaBps = 480e6 / 8
	a.RedeEntradaBps = 480e6 / 8 // ~960 Mbps somados

	v := Avaliar(a, Contexto{StreamsAtivos: 120})
	if nivelDe(v, "Rede") != "critico" {
		t.Fatalf("perto de 1 Gbps deveria ser crítico: %+v", v.Pontos)
	}
	for _, p := range v.Pontos {
		if p.Recurso == "Rede" && !strings.Contains(p.Situacao, "120") {
			t.Errorf("a situação deveria citar as reproduções ativas: %q", p.Situacao)
		}
	}
}

// Sem medição, o veredito diz que não sabe — nunca "está tudo bem".
func TestSemMedicaoNaoAfirmaQueEstaTudoBem(t *testing.T) {
	v := Avaliar(Amostra{Disponivel: false, Motivo: "só em Linux"}, Contexto{})
	if v.Nivel != "desconhecido" {
		t.Fatalf("nível = %q, esperava desconhecido", v.Nivel)
	}
	if strings.Contains(strings.ToLower(v.Titulo), "folgada") {
		t.Errorf("sem medição não se pode afirmar folga: %q", v.Titulo)
	}
}

// CPU ainda sem duas amostras não pode aparecer como 0% — seria dizer "ocioso" sobre uma
// máquina que talvez esteja saturada.
func TestCPUSemAmostraSuficienteNaoEhReportada(t *testing.T) {
	a := base()
	a.CPUPercent = -1

	if n := nivelDe(Avaliar(a, Contexto{}), "Processador"); n != "ausente" {
		t.Errorf("CPU sem medida gerou observação de nível %q", n)
	}
}

func TestBytesHumano(t *testing.T) {
	casos := map[uint64]string{
		512:                   "512 B",
		1024:                  "1.0 KB",
		1536:                  "1.5 KB",
		1 << 20:               "1.0 MB",
		uint64(1)<<30 + 1<<29: "1.5 GB",
		uint64(3) << 40:       "3.0 TB",
	}
	for entrada, esperado := range casos {
		if got := bytesHumano(entrada); got != esperado {
			t.Errorf("bytesHumano(%d) = %q, esperava %q", entrada, got, esperado)
		}
	}
}
