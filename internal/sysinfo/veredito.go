package sysinfo

import "fmt"

// Veredito é uma leitura interpretada: o que está apertado e o que fazer.
//
// Números crus não respondem a pergunta que o administrador realmente faz. "Memória em
// 78%" não diz se é para trocar de VPS — em Linux, 78% com cache é saudável, e 78% com
// swap ativa é problema. A interpretação mora aqui, junto da explicação.
type Veredito struct {
	// Nivel: "ok", "atencao" ou "critico".
	Nivel string `json:"nivel"`
	// Titulo é a conclusão em uma linha.
	Titulo string `json:"titulo"`
	// Pontos são as observações individuais, cada uma com sua recomendação.
	Pontos []Observacao `json:"pontos"`
}

// Observacao é um aspecto avaliado.
type Observacao struct {
	Nivel     string `json:"nivel"`
	Recurso   string `json:"recurso"`
	Situacao  string `json:"situacao"`
	Sugestao  string `json:"sugestao,omitempty"`
	Percentil int    `json:"percentual,omitempty"`
}

// Contexto traz o que só o sistema sabe sobre si.
type Contexto struct {
	// StreamsAtivos é quantas reproduções estão em andamento agora.
	StreamsAtivos int
	// TamanhoBanco em bytes, ou zero se não foi possível consultar.
	TamanhoBanco uint64
	// SincronizandoAgora indica que há sincronização em andamento — CPU alta durante
	// uma sincronização é esperado e não significa VPS pequena.
	SincronizandoAgora bool
}

// Avaliar interpreta a amostra.
func Avaliar(a Amostra, c Contexto) Veredito {
	v := Veredito{Nivel: "ok", Titulo: "A máquina está folgada para a carga atual."}

	if !a.Disponivel {
		return Veredito{
			Nivel:  "desconhecido",
			Titulo: "Não é possível medir os recursos desta máquina.",
			Pontos: []Observacao{{
				Nivel:    "desconhecido",
				Recurso:  "Sistema",
				Situacao: a.Motivo,
			}},
		}
	}

	v.Pontos = append(v.Pontos, avaliarCPU(a, c)...)
	v.Pontos = append(v.Pontos, avaliarMemoria(a)...)
	v.Pontos = append(v.Pontos, avaliarDisco(a, c)...)
	v.Pontos = append(v.Pontos, avaliarRede(a, c)...)

	// O pior ponto define o veredito: uma máquina com disco cheio não está "ok" só
	// porque a CPU está ociosa.
	criticos, atencoes := 0, 0
	for _, p := range v.Pontos {
		switch p.Nivel {
		case "critico":
			criticos++
		case "atencao":
			atencoes++
		}
	}
	switch {
	case criticos > 0:
		v.Nivel = "critico"
		v.Titulo = "Há um limite sendo alcançado. Vale agir antes do próximo horário de pico."
	case atencoes > 0:
		v.Nivel = "atencao"
		v.Titulo = "A máquina dá conta agora, mas há pouca folga para crescer."
	}
	return v
}

func avaliarCPU(a Amostra, c Contexto) []Observacao {
	if a.CPUPercent < 0 {
		return nil
	}
	o := Observacao{Recurso: "Processador", Percentil: int(a.CPUPercent + 0.5)}

	// Carga alta DURANTE uma sincronização não é sinal de VPS pequena: a sincronização é
	// o único trabalho pesado de CPU do sistema, e ela termina.
	if c.SincronizandoAgora && a.CPUPercent >= 70 {
		o.Nivel = "ok"
		o.Situacao = fmt.Sprintf("Em %.0f%%, mas há sincronização em andamento.", a.CPUPercent)
		o.Sugestao = "É o comportamento esperado. Avalie a CPU de novo com a sincronização parada."
		return []Observacao{o}
	}

	switch {
	case a.CPUPercent >= 90:
		o.Nivel = "critico"
		o.Situacao = fmt.Sprintf("Em %.0f%%, praticamente saturado.", a.CPUPercent)
		o.Sugestao = "Dobrar os núcleos é o caminho mais direto. Se acontecer só durante sincronizações, agende-as para fora do horário de pico antes de trocar de plano."
	case a.CPUPercent >= 70:
		o.Nivel = "atencao"
		o.Situacao = fmt.Sprintf("Em %.0f%%, com pouca sobra.", a.CPUPercent)
		o.Sugestao = "Ainda dá conta, mas um pico de audiência não teria para onde crescer."
	default:
		o.Nivel = "ok"
		o.Situacao = fmt.Sprintf("Em %.0f%% de %d núcleos.", a.CPUPercent, a.CPUs)
	}
	return []Observacao{o}
}

func avaliarMemoria(a Amostra) []Observacao {
	if a.MemoriaTotal == 0 {
		return nil
	}
	usada := a.MemoriaTotal - a.MemoriaDisponivel
	pct := float64(usada) / float64(a.MemoriaTotal) * 100

	o := Observacao{Recurso: "Memória", Percentil: int(pct + 0.5)}
	switch {
	case pct >= 92:
		o.Nivel = "critico"
		o.Situacao = fmt.Sprintf("%s de %s em uso.", bytesHumano(usada), bytesHumano(a.MemoriaTotal))
		o.Sugestao = "Aumentar a memória. Sem folga, o Postgres perde o cache das consultas e o painel fica lento em tudo."
	case pct >= 80:
		o.Nivel = "atencao"
		o.Situacao = fmt.Sprintf("%s de %s em uso.", bytesHumano(usada), bytesHumano(a.MemoriaTotal))
		o.Sugestao = "Funciona, mas o banco tem pouco espaço para cache. O próximo plano de memória traria ganho perceptível na navegação."
	default:
		o.Nivel = "ok"
		o.Situacao = fmt.Sprintf("%s livres de %s.", bytesHumano(a.MemoriaDisponivel), bytesHumano(a.MemoriaTotal))
	}
	saida := []Observacao{o}

	// Swap em uso num servidor de vídeo é sempre notícia ruim: significa que algo já foi
	// empurrado para o disco, e disco é ordens de grandeza mais lento que memória.
	if a.SwapTotal > 0 && a.SwapUsada > a.SwapTotal/10 {
		saida = append(saida, Observacao{
			Nivel:    "critico",
			Recurso:  "Swap",
			Situacao: fmt.Sprintf("%s de swap em uso.", bytesHumano(a.SwapUsada)),
			Sugestao: "A máquina já ficou sem memória e recorreu ao disco. Aumentar a memória é a correção — não há ajuste de configuração que compense isso.",
		})
	}
	return saida
}

func avaliarDisco(a Amostra, c Contexto) []Observacao {
	if a.DiscoBytes == 0 {
		return nil
	}
	usado := a.DiscoBytes - a.DiscoLivre
	pct := float64(usado) / float64(a.DiscoBytes) * 100

	o := Observacao{Recurso: "Disco", Percentil: int(pct + 0.5)}
	detalhe := ""
	if c.TamanhoBanco > 0 {
		detalhe = fmt.Sprintf(" O banco ocupa %s.", bytesHumano(c.TamanhoBanco))
	}

	switch {
	case pct >= 90:
		o.Nivel = "critico"
		o.Situacao = fmt.Sprintf("%s livres de %s.%s", bytesHumano(a.DiscoLivre), bytesHumano(a.DiscoBytes), detalhe)
		o.Sugestao = "Disco cheio derruba o Postgres, e com ele o sistema inteiro. Aumente o disco ou remova o que não é do serviço."
	case pct >= 75:
		o.Nivel = "atencao"
		o.Situacao = fmt.Sprintf("%s livres de %s.%s", bytesHumano(a.DiscoLivre), bytesHumano(a.DiscoBytes), detalhe)
		o.Sugestao = "Vale planejar o aumento antes que aperte — principalmente porque o cache de vídeo, quando existir, mora no disco."
	default:
		o.Nivel = "ok"
		o.Situacao = fmt.Sprintf("%s livres de %s.%s", bytesHumano(a.DiscoLivre), bytesHumano(a.DiscoBytes), detalhe)
	}
	return []Observacao{o}
}

func avaliarRede(a Amostra, c Contexto) []Observacao {
	if a.RedeSaidaBps == 0 && a.RedeEntradaBps == 0 {
		return nil
	}
	o := Observacao{Recurso: "Rede"}

	// Hoje a entrega é direta da fonte: cada byte que sai para o espectador entrou vindo
	// da fonte. A banda conta duas vezes, e é o recurso que satura primeiro.
	total := a.RedeEntradaBps + a.RedeSaidaBps
	o.Situacao = fmt.Sprintf("Saindo %s/s, entrando %s/s (%d reproduções agora).",
		bytesHumano(uint64(a.RedeSaidaBps)), bytesHumano(uint64(a.RedeEntradaBps)), c.StreamsAtivos)

	switch {
	case total >= 900e6/8: // ~900 Mbps somados, perto do limite de um link de 1 Gbps
		o.Nivel = "critico"
		o.Sugestao = "Perto do limite de um link de 1 Gbps. Como a entrega é direta da fonte, cada espectador consome banda duas vezes — o cache, quando existir, é o que corta isso pela metade."
	case total >= 500e6/8:
		o.Nivel = "atencao"
		o.Sugestao = "Passou da metade de um link de 1 Gbps. Confira o limite de tráfego mensal do seu plano, que costuma apertar antes da velocidade."
	default:
		o.Nivel = "ok"
	}
	return []Observacao{o}
}

// bytesHumano formata bytes na unidade que cabe.
func bytesHumano(b uint64) string {
	const unidade = 1024
	if b < unidade {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unidade), 0
	for n := b / unidade; n >= unidade && exp < 4; n /= unidade {
		div *= unidade
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTP"[exp])
}
