package export

import "testing"

// A marca de idioma é o que separa dublado de legendado numa lista onde os dois têm o
// título idêntico. Sem ela o cliente vê dois itens iguais.
func TestMarcaDeIdioma(t *testing.T) {
	casos := map[string]string{
		"":       "",
		"leg":    " (Legendado)",
		"en+leg": " (Legendado)",
		"leg+pt": " (Legendado)",
		// Só o legendado é marcado: o dublado é o padrão do acervo, e marcá-lo poluiria a
		// lista inteira para distinguir uma minoria.
		"en": "",
		"es": "",
		"pt": "",
		// "legendado" por extenso não é a chave canônica; marcar por prefixo confundiria
		// idiomas que começam com as mesmas letras.
		"legacy": "",
	}
	for chave, esperado := range casos {
		if got := marcaDeIdioma(chave); got != esperado {
			t.Errorf("marcaDeIdioma(%q) = %q, esperava %q", chave, got, esperado)
		}
	}
}
