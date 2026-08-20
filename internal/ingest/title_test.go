package ingest

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func agora() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) }

func dicionario(t *testing.T) *TagDictionary {
	t.Helper()
	d, err := DefaultTagDictionary()
	if err != nil {
		t.Fatalf("DefaultTagDictionary: %v", err)
	}
	return d
}

func TestCleanTitle(t *testing.T) {
	dict := dicionario(t)

	tests := []struct {
		nome        string
		entrada     string
		normalizado string
		ano         *int
		qualidade   []string
		idioma      []string
	}{
		{
			nome: "ano entre parênteses", entrada: "Interestelar (2014)",
			normalizado: "interestelar", ano: ptr(2014),
		},
		{
			nome: "ano solto e tags", entrada: "A Origem 2010 1080p DUAL",
			normalizado: "origem a", ano: ptr(2010),
			qualidade: []string{"1080p"}, idioma: []string{"dual"},
		},
		{
			nome: "colchetes com ano e tags", entrada: "O Poderoso Chefão [1972] [4K] [LEG]",
			normalizado: "poderoso chefao o", ano: ptr(1972),
			qualidade: []string{"4k"}, idioma: []string{"leg"},
		},
		{
			nome: "acentos removidos", entrada: "Coração Valente (1995) DUBLADO",
			normalizado: "coracao valente", ano: ptr(1995), idioma: []string{"dublado"},
		},
		{
			nome: "numeral romano final", entrada: "Rocky II",
			normalizado: "rocky 2",
		},
		{
			nome: "separadores por ponto", entrada: "Filme Qualquer.2001.720p.WEB-DL",
			normalizado: "filme qualquer", ano: ptr(2001),
			qualidade: []string{"720p", "web-dl"},
		},
		{
			nome: "sem ano nem tags", entrada: "Toy Story",
			normalizado: "toy story",
		},
		{
			nome: "artigo inicial vai para o fim", entrada: "O Senhor dos Anéis",
			normalizado: "senhor dos aneis o",
		},
		{
			nome: "artigo em inglês", entrada: "The Godfather",
			normalizado: "godfather the",
		},
		{
			nome: "ano fora da faixa é ignorado", entrada: "Filme 1500",
			normalizado: "filme 1500",
		},
		{
			// Bug visto no catálogo real: o "2001" virava ano e o título ficava
			// ": Uma Odisséia no Espaço".
			nome: "número no início do título não é ano", entrada: "2001: Uma Odisséia no Espaço",
			normalizado: "2001 uma odisseia no espaco",
		},
		{
			nome: "título que é só um número", entrada: "1917",
			normalizado: "1917",
		},
		{
			nome: "ano depois do nome ainda é reconhecido", entrada: "1917 (2019)",
			normalizado: "1917", ano: ptr(2019),
		},
		{
			nome: "ano futuro demais é ignorado", entrada: "Filme 2099",
			normalizado: "filme 2099",
		},
		{
			nome: "título só com tags mantém o original", entrada: "1080p DUAL",
			normalizado: "1080p dual",
		},
		{
			nome: "pontuação vira espaço", entrada: "Título, Com Vírgula (2020)",
			normalizado: "titulo com virgula", ano: ptr(2020),
		},
		{
			nome: "tag composta", entrada: "Filme X Dolby Vision 2020",
			normalizado: "filme x", ano: ptr(2020), qualidade: []string{"dolby vision"},
		},
		{
			// Bug real: descartar todo bloco entre parênteses colapsava este título em
			// "natal", colidindo com outro filme chamado "#Natal".
			nome:    "bloco desconhecido entre parênteses permanece no título",
			entrada: "Natal (Ao Vivo)", normalizado: "natal ao vivo",
		},
		{
			nome: "bloco só com tags é removido", entrada: "Filme (1080p DUAL)",
			normalizado: "filme", qualidade: []string{"1080p"}, idioma: []string{"dual"},
		},
		{
			nome: "bloco misto mantém a parte desconhecida", entrada: "Filme (Especial 1080p)",
			normalizado: "filme especial", qualidade: []string{"1080p"},
		},
		{
			// A convenção brasileira "[L]" marca a versão legendada.
			nome: "sufixo [L] vira tag de idioma", entrada: "#SeAcabó: Diário das Campeãs [L]",
			normalizado: "seacabo diario das campeas", idioma: []string{"l"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.nome, func(t *testing.T) {
			got := CleanTitle(tc.entrada, dict, agora())
			if got.Normalized != tc.normalizado {
				t.Errorf("normalizado = %q, esperava %q", got.Normalized, tc.normalizado)
			}
			if !mesmoAno(got.Year, tc.ano) {
				t.Errorf("ano = %v, esperava %v", mostrarAno(got.Year), mostrarAno(tc.ano))
			}
			if tc.qualidade != nil && !reflect.DeepEqual(got.QualityTags, tc.qualidade) {
				t.Errorf("qualidade = %v, esperava %v", got.QualityTags, tc.qualidade)
			}
			if tc.idioma != nil && !reflect.DeepEqual(got.LanguageTags, tc.idioma) {
				t.Errorf("idioma = %v, esperava %v", got.LanguageTags, tc.idioma)
			}
		})
	}
}

// O título bruto nunca é perdido: é o que o administrador vê quando pergunta o que a
// fonte realmente mandou.
func TestCleanTitleNuncaDevolveDisplayVazio(t *testing.T) {
	dict := dicionario(t)
	for _, entrada := range []string{"1080p", "(2014)", "[DUAL]", "..."} {
		got := CleanTitle(entrada, dict, agora())
		if strings.TrimSpace(got.Display) == "" {
			t.Errorf("CleanTitle(%q).Display ficou vazio", entrada)
		}
	}
}

func TestCleanTitleEDeterministica(t *testing.T) {
	dict := dicionario(t)
	const entrada = "A Origem 2010 1080p DUAL LEG BluRay x264"
	primeira := CleanTitle(entrada, dict, agora())
	for i := 0; i < 20; i++ {
		outra := CleanTitle(entrada, dict, agora())
		if !reflect.DeepEqual(primeira, outra) {
			t.Fatalf("CleanTitle não é determinística:\n%+v\n%+v", primeira, outra)
		}
	}
}

func TestNormalizeNameConvergeFormasEquivalentes(t *testing.T) {
	pares := [][2]string{
		{"FILMES | AÇÃO", "filmes acao"},
		{"Filmes - Ação", "filmes acao"},
		{"  filmes   ação  ", "filmes acao"},
	}
	for _, p := range pares {
		if got := NormalizeName(p[0]); got != p[1] {
			t.Errorf("NormalizeName(%q) = %q, esperava %q", p[0], got, p[1])
		}
	}
}

func TestParseTagDictionaryPermiteSubstituicao(t *testing.T) {
	custom, err := ParseTagDictionary([]byte(`{"quality":["cam"],"language":["pirata"]}`))
	if err != nil {
		t.Fatalf("ParseTagDictionary: %v", err)
	}
	got := CleanTitle("Filme Teste CAM PIRATA 1080p", custom, agora())
	if got.Normalized != "filme teste 1080p" {
		t.Errorf("normalizado = %q — 1080p não está no dicionário custom, deveria permanecer", got.Normalized)
	}
	if !reflect.DeepEqual(got.QualityTags, []string{"cam"}) {
		t.Errorf("qualidade = %v", got.QualityTags)
	}
	if !reflect.DeepEqual(got.LanguageTags, []string{"pirata"}) {
		t.Errorf("idioma = %v", got.LanguageTags)
	}
}

func TestParseTagDictionaryRejeitaJSONInvalido(t *testing.T) {
	if _, err := ParseTagDictionary([]byte(`{quebrado`)); err == nil {
		t.Error("esperava erro em JSON inválido")
	}
}

func ptr(n int) *int { return &n }

func mesmoAno(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func mostrarAno(p *int) any {
	if p == nil {
		return "nil"
	}
	return *p
}
