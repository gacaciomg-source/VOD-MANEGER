package ingest

import "strings"

// Marcações de ESTADO que algumas fontes anexam ao título.
//
// "Um Grande Despertar" e "Um Grande Despertar Lançamento" são o mesmo filme: mesmo ano,
// mesmo cartaz, mesma duração. A palavra diz onde o item está na vitrine da fonte, não
// que conteúdo ele é.
//
// # Por que isto NÃO entra no matching automático
//
// Um dia aparece um filme cujo título contém "Lançamento" de verdade, e agrupá-lo por essa
// regra seria um erro silencioso — o tipo que só é descoberto quando alguém abre e vê outro
// filme. Então esta lista alimenta uma SUGESTÃO, revisada por quem conhece o acervo.
//
// # Por que idioma não está aqui
//
// "Legendado" e "Dublado" mudam o que a pessoa assiste, então distinguem conteúdos de
// verdade. Removê-los juntaria versões que precisam continuar separadas — foi uma decisão
// tomada quando o acervo real mostrou o problema.
// A lista é curta DE PROPÓSITO.
//
// Cada palavra aqui é uma chance de agrupar dois filmes que não são o mesmo. "Lançamento"
// entrou porque há evidência no acervo real: "Um Grande Despertar" e "Um Grande Despertar
// Lançamento", mesmo ano, mesmo cartaz.
//
// Candidatas descartadas por ora, sem evidência equivalente: "estreia", "novo", "em alta",
// "destaque". Elas voltam quando aparecer um caso concreto que as justifique — regra sem
// caso é palpite, e palpite aqui custa um conteúdo agrupado errado.
var marcacoesDeEstado = []string{
	"lancamento", "lancamentos",
}

// ChaveDeDuplicata devolve o título sem as marcações de estado, normalizado.
//
// Serve só para APROXIMAR candidatos a duplicata. Não substitui o título de nenhum
// conteúdo, e não é usada no agrupamento automático.
func ChaveDeDuplicata(titulo string) string {
	return canonicalize(semMarcacaoDeEstado(titulo))
}

// semMarcacaoDeEstado tira as palavras das pontas do título CRU.
//
// A limpeza precisa acontecer antes da normalização canônica: aquela reordena palavras e
// move artigos para o fim, então "Um Grande Despertar Lançamento" deixaria de ter
// "lançamento" no fim e a remoção nunca encontraria o que remover.
func semMarcacaoDeEstado(titulo string) string {
	base := strings.ToLower(removerAcentos(strings.TrimSpace(titulo)))
	base = strings.Join(strings.Fields(base), " ")
	if base == "" {
		return ""
	}

	// Repete até parar de encontrar: "Filme Lançamento Novo" precisa perder as duas.
	for mudou := true; mudou; {
		mudou = false
		for _, marca := range marcacoesDeEstado {
			// Só no início ou no fim. No meio, a palavra provavelmente faz parte do
			// título — "O Novo Mundo" não pode virar "O Mundo".
			switch {
			case strings.HasSuffix(base, " "+marca):
				base = strings.TrimSpace(strings.TrimSuffix(base, " "+marca))
				mudou = true
			case strings.HasPrefix(base, marca+" "):
				base = strings.TrimSpace(strings.TrimPrefix(base, marca+" "))
				mudou = true
			}
		}
	}
	return base
}

// TemMarcacaoDeEstado informa se o título carrega alguma dessas palavras nas pontas.
//
// É o que permite ao painel mostrar QUAL dos dois itens tem a marcação, para o
// administrador julgar sem precisar comparar os nomes caractere a caractere.
func TemMarcacaoDeEstado(titulo string) bool {
	limpo := strings.ToLower(removerAcentos(strings.TrimSpace(titulo)))
	limpo = strings.Join(strings.Fields(limpo), " ")
	return semMarcacaoDeEstado(titulo) != limpo
}
