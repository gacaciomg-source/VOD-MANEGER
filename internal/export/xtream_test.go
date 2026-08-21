package export

import (
	"testing"

	"vodmanager/internal/store"
)

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

// O nome do episódio é o que identifica o item num painel que importa tudo de forma plana.
// "Episódio 5" sozinho é indistinguível entre cinco séries diferentes.
func TestNomeDeEpisodio(t *testing.T) {
	base := store.ExportEpisode{SeriesTitle: "Arquivo X", SeasonNumber: 2, Number: 5}

	casos := []struct {
		nome     string
		ajustar  func(*store.ExportEpisode)
		esperado string
	}{
		{
			nome:     "sem título da fonte",
			ajustar:  func(e *store.ExportEpisode) {},
			esperado: "Arquivo X S02E05",
		},
		{
			nome:     "com título de verdade",
			ajustar:  func(e *store.ExportEpisode) { e.Title = "O Retorno" },
			esperado: "Arquivo X S02E05 - O Retorno",
		},
		{
			nome:     "legendado é marcado",
			ajustar:  func(e *store.ExportEpisode) { e.LanguageKey = "leg" },
			esperado: "Arquivo X (Legendado) S02E05",
		},
		{
			// A fonte preenche o título com a própria numeração quando não tem o nome.
			// Repetir depois de S02E05 só alonga o nome sem informar nada.
			nome:     "título que só repete o número é descartado",
			ajustar:  func(e *store.ExportEpisode) { e.Title = "Episódio 5" },
			esperado: "Arquivo X S02E05",
		},
		{
			nome:     "variação EP 05 também é descartada",
			ajustar:  func(e *store.ExportEpisode) { e.Title = "EP 05" },
			esperado: "Arquivo X S02E05",
		},
		{
			nome:     "número solto é descartado",
			ajustar:  func(e *store.ExportEpisode) { e.Title = "5" },
			esperado: "Arquivo X S02E05",
		},
		{
			// Um título que POR ACASO contém o número precisa sobreviver.
			nome:     "título com o número dentro é mantido",
			ajustar:  func(e *store.ExportEpisode) { e.Title = "Os 5 Irmãos" },
			esperado: "Arquivo X S02E05 - Os 5 Irmãos",
		},
		{
			nome:     "temporada e episódio com dois dígitos",
			ajustar:  func(e *store.ExportEpisode) { e.SeasonNumber = 12; e.Number = 34 },
			esperado: "Arquivo X S12E34",
		},
	}

	for _, c := range casos {
		e := base
		c.ajustar(&e)
		if got := nomeDeEpisodio(e); got != c.esperado {
			t.Errorf("%s: %q, esperava %q", c.nome, got, c.esperado)
		}
	}
}
