//go:build linux

package sysinfo

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Em Linux tudo vem de /proc e de statfs. É a mesma fonte que as bibliotecas de mercado
// consultam — usá-la direto evita uma dependência para ler quatro arquivos de texto.

// pontoDeMontagem é o sistema de arquivos medido.
//
// A raiz, e não o diretório do banco: numa instalação padrão os dois são o mesmo disco, e
// descobrir o caminho real do Postgres exigiria consultar o servidor. Quando encher, é a
// raiz que enche.
const pontoDeMontagem = "/"

func lerBruta() leituraBruta {
	l := leituraBruta{instante: agora(), disponivel: true}

	if total, ocioso, ok := lerCPU(); ok {
		l.cpuTotal, l.cpuOcioso, l.temCPU = total, ocioso, true
	}
	if entra, sai, ok := lerRede(); ok {
		l.redeEntra, l.redeSai, l.temRede = entra, sai, true
	}
	return l
}

// lerCPU soma os tempos da linha agregada de /proc/stat.
func lerCPU() (total, ocioso uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		campos := strings.Fields(sc.Text())
		if len(campos) < 5 || campos[0] != "cpu" {
			continue
		}
		for i, bruto := range campos[1:] {
			v, err := strconv.ParseUint(bruto, 10, 64)
			if err != nil {
				continue
			}
			total += v
			// Campos 4 e 5 são idle e iowait: a máquina não estava computando.
			if i == 3 || i == 4 {
				ocioso += v
			}
		}
		return total, ocioso, true
	}
	return 0, 0, false
}

// lerRede soma bytes de todas as interfaces reais.
func lerRede() (entra, sai uint64, ok bool) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		linha := sc.Text()
		nome, resto, achou := strings.Cut(linha, ":")
		if !achou {
			continue // as duas linhas de cabeçalho
		}
		nome = strings.TrimSpace(nome)
		// A interface de loopback contaria o tráfego entre o painel e o banco na própria
		// máquina, que não usa banda nenhuma da VPS.
		if nome == "lo" || strings.HasPrefix(nome, "docker") || strings.HasPrefix(nome, "veth") {
			continue
		}
		campos := strings.Fields(resto)
		if len(campos) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(campos[0], 10, 64)
		tx, err2 := strconv.ParseUint(campos[8], 10, 64)
		if err1 == nil && err2 == nil {
			entra += rx
			sai += tx
			ok = true
		}
	}
	return entra, sai, ok
}

func preencherEstaticos(a *Amostra) {
	if total, disponivel, swapTotal, swapLivre, ok := lerMemoria(); ok {
		a.MemoriaTotal, a.MemoriaDisponivel = total, disponivel
		a.SwapTotal, a.SwapUsada = swapTotal, swapTotal-swapLivre
	}
	if carga, ok := lerCarga(); ok {
		a.LoadAverage = carga
	}

	var st syscall.Statfs_t
	if err := syscall.Statfs(pontoDeMontagem, &st); err == nil {
		a.DiscoTotal = pontoDeMontagem
		a.DiscoBytes = st.Blocks * uint64(st.Bsize)
		// Bavail, e não Bfree: parte do disco é reservada para o root e não está
		// realmente disponível para o serviço.
		a.DiscoLivre = st.Bavail * uint64(st.Bsize)
	}
}

// lerMemoria usa MemAvailable, que é o número honesto.
//
// MemFree conta só o que está intocado e ignora o cache reciclável — num servidor de vídeo
// esse cache é grande, e olhar MemFree faria uma máquina saudável parecer sem memória.
func lerMemoria() (total, disponivel, swapTotal, swapLivre uint64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, 0, false
	}
	defer f.Close()

	campos := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		chave, resto, achou := strings.Cut(sc.Text(), ":")
		if !achou {
			continue
		}
		partes := strings.Fields(resto)
		if len(partes) == 0 {
			continue
		}
		if v, err := strconv.ParseUint(partes[0], 10, 64); err == nil {
			campos[chave] = v * 1024 // /proc/meminfo reporta em kB
		}
	}
	total, temTotal := campos["MemTotal"]
	disponivel, temDisp := campos["MemAvailable"]
	if !temDisp {
		disponivel = campos["MemFree"]
	}
	return total, disponivel, campos["SwapTotal"], campos["SwapFree"], temTotal
}

func lerCarga() ([]float64, bool) {
	bruto, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, false
	}
	campos := strings.Fields(string(bruto))
	if len(campos) < 3 {
		return nil, false
	}
	out := make([]float64, 0, 3)
	for _, c := range campos[:3] {
		v, err := strconv.ParseFloat(c, 64)
		if err != nil {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}
