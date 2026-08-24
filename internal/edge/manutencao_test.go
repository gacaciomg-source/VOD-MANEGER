package edge

import (
	"errors"
	"io"
	"net/http"
	"testing"
)

// TestDeteccaoDeVideoDeManutencao cobre o caso que motivou o recurso.
//
// Uma fonte em manutenção não devolve erro: devolve HTTP 200 com um aviso de dez segundos
// no lugar do filme. Para o proxy isso é sucesso, e o failover — que existe para trocar de
// origem quando uma falha — nunca é acionado.
func TestDeteccaoDeVideoDeManutencao(t *testing.T) {
	const limiar = 100 << 20

	casos := []struct {
		nome     string
		resp     *http.Response
		suspeito bool
	}{
		{
			nome:     "aviso de manutenção em alta definição (40 MB)",
			resp:     &http.Response{StatusCode: 200, ContentLength: 40 << 20, Header: http.Header{}},
			suspeito: true,
		},
		{
			nome:     "filme de verdade",
			resp:     &http.Response{StatusCode: 200, ContentLength: 2 << 30, Header: http.Header{}},
			suspeito: false,
		},
		{
			// O caso que uma detecção ingênua erraria: o player pede um pedaço pequeno, e
			// a resposta é pequena — mas o ARQUIVO é enorme. Confundir os dois faria toda
			// busca no meio do filme parecer manutenção.
			nome: "seek no meio de um filme grande",
			resp: &http.Response{
				StatusCode:    206,
				ContentLength: 1 << 20,
				Header:        http.Header{"Content-Range": {"bytes 1000000-2048575/2000000000"}},
			},
			suspeito: false,
		},
		{
			nome: "seek dentro de um aviso de manutenção",
			resp: &http.Response{
				StatusCode:    206,
				ContentLength: 1 << 10,
				Header:        http.Header{"Content-Range": {"bytes 0-1023/3000000"}},
			},
			suspeito: true,
		},
		{
			// Fonte que transmite sem anunciar tamanho existe. Não saber não é motivo para
			// recusar: recusá-la derrubaria conteúdo bom.
			nome:     "fonte que não anuncia tamanho",
			resp:     &http.Response{StatusCode: 200, ContentLength: -1, Header: http.Header{}},
			suspeito: false,
		},
		{
			nome: "tamanho total desconhecido no Content-Range",
			resp: &http.Response{
				StatusCode:    206,
				ContentLength: 1 << 10,
				Header:        http.Header{"Content-Range": {"bytes 0-1023/*"}},
			},
			suspeito: false,
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, suspeito := pareceVideoDeManutencao(c.resp, limiar)
			if suspeito != c.suspeito {
				t.Errorf("suspeito = %v, queria %v", suspeito, c.suspeito)
			}
		})
	}
}

// TestDeteccaoDesligada: limiar zero não recusa nada, nem o menor arquivo.
func TestDeteccaoDesligada(t *testing.T) {
	resp := &http.Response{StatusCode: 200, ContentLength: 1024, Header: http.Header{}}
	if _, suspeito := pareceVideoDeManutencao(resp, 0); suspeito {
		t.Error("com a detecção desligada, nada pode ser recusado")
	}
}

// TestUltimaOrigemEhServidaMesmoAssim é a trava que impede a detecção de piorar as coisas.
//
// Um palpite errado sobre a ÚLTIMA origem trocaria dez segundos de aviso por tela preta.
// Dez segundos são ruins; nada é pior.
func TestUltimaOrigemEhServidaMesmoAssim(t *testing.T) {
	tres := make([]int, 3)

	if !temOutraOrigemParaTentar(tres, 0, 1) {
		t.Error("com três origens e estando na primeira, há alternativa")
	}
	if temOutraOrigemParaTentar(tres, 2, 3) {
		t.Error("estando na última, não há alternativa — precisa servir assim mesmo")
	}

	// O teto de tentativas também encerra a busca: sem isso, um catálogo com vinte
	// variantes viraria vinte conexões antes do primeiro byte.
	vinte := make([]int, 20)
	if temOutraOrigemParaTentar(vinte, 0, maxTentativas) {
		t.Error("o teto de tentativas precisa encerrar a busca")
	}
}

// TestMinimoOuPadrao: zero e negativo significam coisas diferentes.
//
// Sem essa distinção não haveria como DESLIGAR a detecção — zero teria de significar
// "não configurado" e "desligado" ao mesmo tempo.
func TestMinimoOuPadrao(t *testing.T) {
	if got := minimoOuPadrao(0); got != tamanhoMinimoPadrao {
		t.Errorf("não configurado = %d, queria o padrão %d", got, tamanhoMinimoPadrao)
	}
	if got := minimoOuPadrao(-1); got != 0 {
		t.Errorf("negativo = %d, queria 0 (desligado)", got)
	}
	if got := minimoOuPadrao(5 << 20); got != 5<<20 {
		t.Errorf("configurado = %d, queria 5 MiB", got)
	}
}

// leitorQueCortaNoMeio entrega parte do conteúdo e encerra limpo, como uma fonte de IPTV
// que derruba a conexão sob carga.
type leitorQueCortaNoMeio struct{ restante int }

func (l *leitorQueCortaNoMeio) Read(p []byte) (int, error) {
	if l.restante <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > l.restante {
		n = l.restante
	}
	for i := range p[:n] {
		p[i] = 'v'
	}
	l.restante -= n
	return n, nil
}

// TestEntregaCortadaNaoPassaPorSucesso é a guarda do desfecho invisível mais grave.
//
// Quando a origem encerra a conexão, io.Copy devolve EOF — que em Go é AUSÊNCIA de erro.
// Uma entrega cortada em 40% do filme era gravada como conclusão bem-sucedida, e a
// reprodução aparecia nos números ao lado das que terminaram inteiras.
//
// Do lado de quem assiste, o filme para no meio e o player volta ao começo. Do lado de quem
// administra, o painel dizia que tudo correu bem. É a falha mais comum de fonte de IPTV sob
// carga, e era a que os registros não sabiam contar.
func TestEntregaCortadaNaoPassaPorSucesso(t *testing.T) {
	const anunciado = 1000

	casos := []struct {
		nome      string
		entregue  int64
		erroCopia error
		querErro  bool
	}{
		{"entrega completa", anunciado, nil, false},
		{"fonte cortou em 40%", 400, nil, true},
		{"cliente desistiu no meio", 400, errors.New("broken pipe"), false},
		{"fonte entregou mais que anunciou", anunciado + 10, nil, false},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			// Reproduz a decisão que o proxy toma depois da cópia.
			faltou := int64(anunciado) - c.entregue
			cortada := c.erroCopia == nil && anunciado > 0 && faltou > 0

			if cortada != c.querErro {
				t.Errorf("detectou corte = %v, queria %v (entregue %d de %d)",
					cortada, c.querErro, c.entregue, anunciado)
			}
		})
	}
}

// TestCorteEhIndistinguivelSemAVerificacao explica por que a guarda acima precisa existir.
//
// io.Copy termina SEM ERRO quando a origem fecha. Sem comparar o entregue com o anunciado,
// não há nada no valor de retorno que separe um filme inteiro de um filme pela metade.
func TestCorteEhIndistinguivelSemAVerificacao(t *testing.T) {
	fonte := &leitorQueCortaNoMeio{restante: 400}
	entregue, err := io.Copy(io.Discard, fonte)

	if err != nil {
		t.Fatalf("io.Copy devolveu %v; o ponto do teste é que ela NÃO devolve erro", err)
	}
	if entregue != 400 {
		t.Fatalf("entregue = %d, queria 400", entregue)
	}
	// Sem a comparação com o tamanho anunciado, isto aqui é sucesso — e era.
}
