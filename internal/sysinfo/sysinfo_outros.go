//go:build !linux

package sysinfo

// Fora do Linux não medimos o host.
//
// O sistema roda em produção em Ubuntu/Debian; Windows e macOS são ambientes de
// desenvolvimento. Escrever leitura de contadores para eles seria código sem uso real, e
// ainda assim precisaria ser mantido.
//
// O que fica: os números do próprio processo, que vêm do runtime do Go e valem em qualquer
// sistema. O resto volta marcado como indisponível, e o painel diz isso — em vez de exibir
// um número plausível e falso.
func lerBruta() leituraBruta {
	return leituraBruta{
		instante:   agora(),
		disponivel: false,
		motivo:     "a medição de CPU, memória e disco da máquina só existe em Linux; em produção (Ubuntu/Debian) ela aparece completa",
	}
}

func preencherEstaticos(*Amostra) {}
