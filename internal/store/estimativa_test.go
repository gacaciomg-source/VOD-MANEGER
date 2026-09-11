package store

import "testing"

// "Mais leve" decide pelo peso, e não pela ordem das faixas: a desconhecida não tem lugar
// na ordem, mas tem peso — e pode ser justamente a menor.
func TestMaisLeveDecidePeloPeso(t *testing.T) {
	peso := func(f string) int64 {
		return map[string]int64{"sd": 900, "fhd": 3000, "?": 500}[f]
	}
	if got := maisLeve([]string{"fhd", "sd", "?"}, peso); got != "?" {
		t.Fatalf("mais leve = %q; a desconhecida pesava menos", got)
	}
	if got := maisLeve(nil, peso); got != FaixaDesconhecida {
		t.Fatalf("sem faixas = %q, esperava a desconhecida", got)
	}
}

// "Melhor qualidade" só considera o que foi DECLARADO. Não dá para dizer que um arquivo sem
// marca nenhuma é melhor que um 720p.
func TestMelhorQualidadeIgnoraADesconhecida(t *testing.T) {
	if got := melhorQualidade([]string{"?", "hd", "sd"}); got != FaixaHD {
		t.Fatalf("melhor = %q, esperava hd", got)
	}
	if got := melhorQualidade([]string{"?"}); got != FaixaDesconhecida {
		t.Fatalf("só desconhecida = %q", got)
	}
}
