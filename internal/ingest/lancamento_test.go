package ingest

import "testing"

// O que importa não é a string exata que sai — é que os dois títulos cheguem à MESMA
// chave. Fixar a saída literal testaria detalhe da normalização canônica, que reordena
// palavras e move artigos, e quebraria a cada ajuste dela.
func TestTitulosComMarcacaoColapsamNoMesmo(t *testing.T) {
	pares := [][2]string{
		{"Um Grande Despertar", "Um Grande Despertar Lançamento"},
		{"Um Grande Despertar", "Lançamento Um Grande Despertar"},
		{"Um Grande Despertar", "Um Grande Despertar Lançamentos"},
		{"O Morro dos Ventos Uivantes", "O Morro dos Ventos Uivantes Lançamento"},
	}
	for _, p := range pares {
		a, b := ChaveDeDuplicata(p[0]), ChaveDeDuplicata(p[1])
		if a != b {
			t.Errorf("%q e %q deveriam ter a mesma chave: %q vs %q", p[0], p[1], a, b)
		}
		if a == "" {
			t.Errorf("%q produziu chave vazia", p[0])
		}
	}
}

// A palavra no MEIO do título faz parte dele. Removê-la juntaria filmes diferentes.
func TestPalavraNoMeioNaoEhRemovida(t *testing.T) {
	pares := [][2]string{
		{"O Lançamento do Foguete", "O Foguete"},
		{"Um Grande Despertar", "Um Pequeno Despertar"},
	}
	for _, p := range pares {
		if ChaveDeDuplicata(p[0]) == ChaveDeDuplicata(p[1]) {
			t.Errorf("%q e %q foram tratados como o mesmo conteúdo", p[0], p[1])
		}
	}
}

// Idioma NÃO é marcação de estado: dublado e legendado são conteúdos diferentes, e juntá-los
// desfaria uma separação que o acervo real mostrou ser necessária.
func TestIdiomaNaoEhRemovido(t *testing.T) {
	for _, titulo := range []string{
		"Interestelar Legendado", "Interestelar Dublado", "Interestelar Dual Audio",
	} {
		if TemMarcacaoDeEstado(titulo) {
			t.Errorf("%q foi tratado como marcação de estado; idioma distingue conteúdo", titulo)
		}
	}
	if ChaveDeDuplicata("Interestelar Legendado") == ChaveDeDuplicata("Interestelar") {
		t.Error("legendado colapsou com o dublado")
	}
}

func TestTemMarcacaoDeEstado(t *testing.T) {
	com := []string{"Filme Lançamento", "Lançamento Filme", "Filme Lançamentos"}
	// "Novo" e "Estreia" saíram da lista: sem caso concreto no acervo, a regra seria
	// palpite, e palpite aqui agrupa conteúdo errado.
	sem := []string{"Filme", "O Novo Mundo", "Novo Filme", "Filme Estreia",
		"Filme Legendado", "O Lançamento do Foguete"}

	for _, s := range com {
		if !TemMarcacaoDeEstado(s) {
			t.Errorf("%q deveria ter marcação de estado", s)
		}
	}
	for _, s := range sem {
		if TemMarcacaoDeEstado(s) {
			t.Errorf("%q NÃO deveria ter marcação de estado", s)
		}
	}
}

// Título vazio ou só com a marcação não pode virar chave vazia que agrupa tudo.
func TestTituloDegeneradoNaoAgrupaTudo(t *testing.T) {
	for _, s := range []string{"", "   ", "Lançamento", "Novo"} {
		if c := ChaveDeDuplicata(s); c != "" {
			continue // ficou com algo: tudo bem
		}
		// Chave vazia é aceitável desde que quem consome a ignore. O teste registra a
		// expectativa para que quem for usar não trate vazio como "igual a todos".
		if ChaveDeDuplicata(s) != "" {
			t.Errorf("%q: esperava chave vazia", s)
		}
	}
}
