// Package sysinfo mede o consumo de recursos da máquina.
//
// Existe para responder uma pergunta concreta do administrador: "a VPS que eu tenho dá
// conta, ou está na hora de trocar?". Sem medida, essa decisão vira palpite — e palpite
// erra para os dois lados: paga-se por sobra ou descobre-se o limite durante o horário de
// pico, com clientes assistindo.
//
// Duas regras guiaram o pacote:
//
//  1. Sem dependência externa. Em Linux tudo vem de /proc e de statfs, que é onde as
//     bibliotecas de mercado buscam de qualquer forma.
//  2. Nunca inventar número. O que este sistema operacional não expõe volta marcado como
//     indisponível, e o painel diz isso — um valor plausível e falso é pior que a
//     ausência dele, porque a decisão seria tomada com confiança indevida.
package sysinfo

import (
	"runtime"
	"sync"
	"time"
)

// Amostra é a fotografia dos recursos num instante.
type Amostra struct {
	// Disponivel é falso quando o sistema operacional não expõe as medidas de host.
	// Nesse caso só os campos do próprio processo têm valor.
	Disponivel bool   `json:"disponivel"`
	Motivo     string `json:"motivo,omitempty"`

	CPUs int `json:"cpus"`
	// CPUPercent é o uso agregado desde a amostragem anterior. Negativo enquanto não há
	// duas amostras para comparar.
	CPUPercent  float64   `json:"cpu_percent"`
	LoadAverage []float64 `json:"load_average,omitempty"`

	MemoriaTotal      uint64 `json:"memoria_total"`
	MemoriaDisponivel uint64 `json:"memoria_disponivel"`
	SwapTotal         uint64 `json:"swap_total"`
	SwapUsada         uint64 `json:"swap_usada"`

	DiscoTotal string `json:"disco_ponto,omitempty"`
	DiscoBytes uint64 `json:"disco_total"`
	DiscoLivre uint64 `json:"disco_livre"`

	// Rede em bytes por segundo, medido entre as duas últimas amostras.
	RedeEntradaBps float64 `json:"rede_entrada_bps"`
	RedeSaidaBps   float64 `json:"rede_saida_bps"`

	// Do próprio processo, sempre disponível.
	ProcessoMemoria uint64        `json:"processo_memoria"`
	Goroutines      int           `json:"goroutines"`
	Uptime          time.Duration `json:"-"`
	UptimeSegundos  int64         `json:"uptime_segundos"`
}

// Coletor amostra periodicamente e guarda a última leitura.
//
// A amostragem é periódica, e não sob demanda, por uma razão técnica: uso de CPU e taxa de
// rede são DIFERENÇAS entre dois instantes. Calculá-las no momento em que o painel pede
// exigiria segurar a requisição por um intervalo, ou devolver o número errado.
type Coletor struct {
	mu       sync.RWMutex
	ultima   Amostra
	anterior *leituraBruta
	inicio   time.Time
	parar    chan struct{}
	fim      sync.Once
}

// leituraBruta são os contadores acumulados que só fazem sentido em pares.
type leituraBruta struct {
	instante   time.Time
	cpuTotal   uint64
	cpuOcioso  uint64
	redeEntra  uint64
	redeSai    uint64
	temCPU     bool
	temRede    bool
	disponivel bool
	motivo     string
}

// intervaloAmostra é o passo entre leituras.
//
// Cinco segundos suaviza o pico de um seek de player sem esconder um problema real: um
// servidor saturado fica saturado por minutos, não por um instante.
const intervaloAmostra = 5 * time.Second

// NovoColetor cria e inicia o coletor.
func NovoColetor() *Coletor {
	c := &Coletor{inicio: time.Now(), parar: make(chan struct{})}
	c.amostrar() // primeira leitura: estabelece a base das diferenças
	go c.laco()
	return c
}

func (c *Coletor) laco() {
	t := time.NewTicker(intervaloAmostra)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			c.amostrar()
		case <-c.parar:
			return
		}
	}
}

// Fechar encerra a rotina de amostragem.
func (c *Coletor) Fechar() {
	c.fim.Do(func() { close(c.parar) })
}

// Atual devolve a última amostra.
func (c *Coletor) Atual() Amostra {
	c.mu.RLock()
	defer c.mu.RUnlock()
	a := c.ultima
	a.Uptime = time.Since(c.inicio)
	a.UptimeSegundos = int64(a.Uptime.Seconds())
	return a
}

func (c *Coletor) amostrar() {
	bruta := lerBruta()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	a := Amostra{
		Disponivel:      bruta.disponivel,
		Motivo:          bruta.motivo,
		CPUs:            runtime.NumCPU(),
		CPUPercent:      -1,
		ProcessoMemoria: mem.Sys,
		Goroutines:      runtime.NumGoroutine(),
	}
	preencherEstaticos(&a)

	c.mu.Lock()
	anterior := c.anterior
	c.mu.Unlock()

	if anterior != nil {
		decorrido := bruta.instante.Sub(anterior.instante).Seconds()
		if decorrido > 0 {
			if bruta.temCPU && anterior.temCPU {
				dTotal := float64(bruta.cpuTotal - anterior.cpuTotal)
				dOcioso := float64(bruta.cpuOcioso - anterior.cpuOcioso)
				if dTotal > 0 {
					a.CPUPercent = arredondar((dTotal - dOcioso) / dTotal * 100)
				}
			}
			if bruta.temRede && anterior.temRede {
				a.RedeEntradaBps = arredondar(float64(bruta.redeEntra-anterior.redeEntra) / decorrido)
				a.RedeSaidaBps = arredondar(float64(bruta.redeSai-anterior.redeSai) / decorrido)
			}
		}
	}

	c.mu.Lock()
	c.ultima = a
	c.anterior = &bruta
	c.mu.Unlock()
}

func arredondar(v float64) float64 {
	if v < 0 {
		return 0
	}
	return float64(int64(v*10+0.5)) / 10
}
