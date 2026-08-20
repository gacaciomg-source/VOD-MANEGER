package ingest

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

//go:embed dictionaries/tags.json
var dictionariesFS embed.FS

// TagDictionary é o vocabulário de tags de qualidade e idioma removidas do título.
type TagDictionary struct {
	Quality  []string `json:"quality"`
	Language []string `json:"language"`

	quality  map[string]bool
	language map[string]bool
	// multiWord guarda as tags compostas ("dolby vision"), tratadas antes da tokenização.
	multiWord []string
}

// DefaultTagDictionary carrega o dicionário embutido no binário.
func DefaultTagDictionary() (*TagDictionary, error) {
	raw, err := dictionariesFS.ReadFile("dictionaries/tags.json")
	if err != nil {
		return nil, fmt.Errorf("ingest: lendo dicionário embutido: %w", err)
	}
	return ParseTagDictionary(raw)
}

// ParseTagDictionary lê um dicionário de tags a partir de JSON.
func ParseTagDictionary(raw []byte) (*TagDictionary, error) {
	var d TagDictionary
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("ingest: dicionário de tags inválido: %w", err)
	}
	d.index()
	return &d, nil
}

func (d *TagDictionary) index() {
	d.quality = make(map[string]bool, len(d.Quality))
	d.language = make(map[string]bool, len(d.Language))
	d.multiWord = nil
	for _, t := range d.Quality {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		d.quality[t] = true
		if strings.Contains(t, " ") {
			d.multiWord = append(d.multiWord, t)
		}
	}
	for _, t := range d.Language {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		d.language[t] = true
		if strings.Contains(t, " ") {
			d.multiWord = append(d.multiWord, t)
		}
	}
	// Tags compostas mais longas primeiro, para "dual audio" ganhar de "dual".
	sort.Slice(d.multiWord, func(i, j int) bool { return len(d.multiWord[i]) > len(d.multiWord[j]) })
}

// TitleResult é o produto da limpeza de um título.
type TitleResult struct {
	Display       string
	Normalized    string
	Year          *int
	YearFromTitle bool
	QualityTags   []string
	LanguageTags  []string
}

var (
	// Blocos entre colchetes, parênteses ou chaves.
	reBracketed = regexp.MustCompile(`[\[\({][^\]\)}]*[\]\)}]`)
	// Ano entre delimitadores ou isolado.
	reYearDelimited = regexp.MustCompile(`[\(\[\.\s_-](\d{4})[\)\]\.\s_-]?`)
	reNonAlnum      = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	reSpaces        = regexp.MustCompile(`\s+`)
)

// anoMinimo é o primeiro ano plausível para uma obra audiovisual.
const anoMinimo = 1888

// romanos mapeia numerais romanos em posição final de título ("Rocky II" → "rocky 2").
//
// Os numerais de UMA letra ("I", "V", "X") ficam deliberadamente de fora: "Filme X" e
// "Missão V" são muito mais frequentes como nome do que como numeral, e converter erraria
// a identidade do conteúdo. Ambiguidade encontrada por teste; ver TestCleanTitle.
var romanos = map[string]string{
	"ii": "2", "iii": "3", "iv": "4", "vi": "6",
	"vii": "7", "viii": "8", "ix": "9",
	"xi": "11", "xii": "12", "xiii": "13",
}

// artigosIniciais são artigos movidos para o fim na forma canônica.
var artigosIniciais = map[string]bool{
	"o": true, "a": true, "os": true, "as": true,
	"um": true, "uma": true, "the": true, "el": true, "la": true, "los": true, "las": true,
}

// CleanTitle aplica a regra title-cleanup-v1 (docs/07 §5), em ordem fixa.
//
// É função pura: mesma entrada, mesma saída, sem estado e sem I/O.
func CleanTitle(raw string, dict *TagDictionary, now time.Time) TitleResult {
	res := TitleResult{QualityTags: []string{}, LanguageTags: []string{}}

	work := strings.TrimSpace(raw)

	// 1. Blocos entre colchetes/parênteses: extrai ano e tags conhecidas.
	//
	// O que NÃO for ano nem tag reconhecida permanece no título. Descartar todo bloco
	// entre parênteses colapsava "Natal (Ao Vivo)" em "Natal" e o fazia colidir com
	// outro filme — bug visto no catálogo real.
	work = reBracketed.ReplaceAllStringFunc(work, func(bloco string) string {
		inner := strings.TrimSpace(bloco[1 : len(bloco)-1])
		if ano, ok := parseAno(inner, now); ok && res.Year == nil {
			res.Year = &ano
			res.YearFromTitle = true
			return " "
		}
		q, l, sobra := extrairTags(inner, dict)
		res.QualityTags = append(res.QualityTags, q...)
		res.LanguageTags = append(res.LanguageTags, l...)

		if strings.TrimSpace(sobra) == "" {
			// O bloco era só tags (ou estava vazio): pode sair.
			return " "
		}
		// Sobrou conteúdo desconhecido: ele faz parte do nome da obra e fica.
		return " " + sobra + " "
	})

	// 2. Ano solto no título ("Interestelar 2014", "Filme.2014.1080p").
	//
	// Um número de quatro dígitos NO INÍCIO do título é parte do nome, não marcação de
	// ano: "2001: Uma Odisséia no Espaço" e "1917" são títulos, não anos. Ano de
	// lançamento vem depois do nome ou entre parênteses. Sem essa exceção, o catálogo
	// real produziu ": Uma Odisséia no Espaço" com ano 2001 — bug visto em produção.
	if res.Year == nil {
		acolchoado := " " + work + " "
		for _, m := range reYearDelimited.FindAllStringSubmatchIndex(acolchoado, -1) {
			inicioNoTitulo := m[2] - 1 // desconta o espaço de preenchimento
			if inicioNoTitulo == 0 {
				continue
			}
			candidato := acolchoado[m[2]:m[3]]
			ano, ok := parseAno(candidato, now)
			if !ok {
				continue
			}
			res.Year = &ano
			res.YearFromTitle = true
			// Remove a ocorrência exata que foi reconhecida, não a primeira do texto.
			work = work[:inicioNoTitulo] + " " + work[inicioNoTitulo+len(candidato):]
			break
		}
	}

	// 3. Tags soltas fora de blocos.
	q, l, semTags := extrairTags(work, dict)
	res.QualityTags = append(res.QualityTags, q...)
	res.LanguageTags = append(res.LanguageTags, l...)
	work = semTags

	res.QualityTags = dedup(res.QualityTags)
	res.LanguageTags = dedup(res.LanguageTags)

	// 4. Título de exibição: limpo, mas ainda com acentos e maiúsculas originais.
	display := strings.Trim(reSpaces.ReplaceAllString(work, " "), " -–—_.,|")
	display = strings.TrimSpace(display)
	if display == "" {
		display = strings.TrimSpace(raw)
	}
	res.Display = display

	// 5. Forma canônica para matching.
	res.Normalized = canonicalize(display)
	return res
}

// canonicalize produz a forma usada para comparação entre fontes.
func canonicalize(s string) string {
	s = removerAcentos(strings.ToLower(s))
	s = reNonAlnum.ReplaceAllString(s, " ")
	s = strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
	if s == "" {
		return ""
	}

	tokens := strings.Fields(s)

	// Numeral romano em posição final vira arábico ("rocky ii" → "rocky 2").
	if n := len(tokens); n > 1 {
		if arabico, ok := romanos[tokens[n-1]]; ok {
			tokens[n-1] = arabico
		}
	}
	// Artigo inicial vai para o fim, para "o senhor dos aneis" e "senhor dos aneis, o"
	// convergirem para a mesma forma.
	if len(tokens) >= 2 && artigosIniciais[tokens[0]] {
		tokens = append(tokens[1:], tokens[0])
	}
	return strings.Join(tokens, " ")
}

// NormalizeName é a forma canônica de um nome qualquer (categoria, grupo).
func NormalizeName(s string) string { return canonicalize(s) }

func removerAcentos(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		return s
	}
	return out
}

func parseAno(s string, now time.Time) (int, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 4 {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	if n < anoMinimo || n > now.Year()+2 {
		return 0, false
	}
	return n, true
}

// extrairTags remove tags de qualidade/idioma e devolve o que sobrou do texto.
func extrairTags(s string, dict *TagDictionary) (quality, language []string, resto string) {
	if dict == nil {
		return nil, nil, s
	}
	trabalho := s

	// Tags compostas primeiro, sobre a forma sem acento e minúscula.
	for _, tag := range dict.multiWord {
		for {
			idx := indexTagComposta(trabalho, tag)
			if idx < 0 {
				break
			}
			if dict.quality[tag] {
				quality = append(quality, tag)
			} else {
				language = append(language, tag)
			}
			trabalho = trabalho[:idx] + " " + trabalho[idx+len(tag):]
		}
	}

	// Tokens simples.
	campos := strings.FieldsFunc(trabalho, func(r rune) bool {
		return r == ' ' || r == '.' || r == '_' || r == '|' || r == ',' || r == '/'
	})
	mantidos := make([]string, 0, len(campos))
	for _, campo := range campos {
		chave := strings.Trim(removerAcentos(strings.ToLower(campo)), "-–—")
		switch {
		case chave == "":
			continue
		case dict.quality[chave]:
			quality = append(quality, chave)
		case dict.language[chave]:
			language = append(language, chave)
		default:
			mantidos = append(mantidos, campo)
		}
	}
	return quality, language, strings.Join(mantidos, " ")
}

// indexTagComposta procura uma tag composta respeitando fronteira de palavra, na forma
// sem acento e minúscula, devolvendo o índice na string ORIGINAL.
func indexTagComposta(s, tag string) int {
	plano := removerAcentos(strings.ToLower(s))
	if len(plano) != len(s) {
		// A remoção de acentos mudou o tamanho: os índices não são mais comparáveis.
		// Nesse caso não arriscamos um recorte errado.
		return -1
	}
	from := 0
	for {
		idx := strings.Index(plano[from:], tag)
		if idx < 0 {
			return -1
		}
		abs := from + idx
		antes := abs == 0 || !ehAlfanumerico(rune(plano[abs-1]))
		fim := abs + len(tag)
		depois := fim >= len(plano) || !ehAlfanumerico(rune(plano[fim]))
		if antes && depois {
			return abs
		}
		from = abs + 1
	}
}

func ehAlfanumerico(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func dedup(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
