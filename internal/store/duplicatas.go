package store

import (
	"context"
	"sort"
	"strconv"
)

// Sugestões de conteúdo duplicado.
//
// O sistema aponta pares que PARECEM o mesmo conteúdo e deixa a decisão com quem conhece o
// acervo. Nunca agrupa sozinho: um dia aparece um filme cujo nome contém "Lançamento" de
// verdade, e um agrupamento errado só é descoberto quando alguém abre e vê outro filme.

// ConteudoParaComparar é o mínimo que a detecção precisa.
type ConteudoParaComparar struct {
	ID    int64
	Tipo  string
	Chave string // título sem as marcações de estado, normalizado
	Ano   *int
}

// SugestaoDeDuplicata é um par que parece ser o mesmo conteúdo.
type SugestaoDeDuplicata struct {
	A ConteudoDuplicado `json:"a"`
	B ConteudoDuplicado `json:"b"`
}

// ConteudoDuplicado descreve um lado do par, com o que o administrador precisa para julgar.
type ConteudoDuplicado struct {
	ID        int64  `json:"id"`
	Titulo    string `json:"titulo"`
	Tipo      string `json:"tipo"`
	Ano       *int   `json:"ano"`
	PosterURL string `json:"poster_url"`
	// Variantes diz quantas fontes servem este conteúdo. Ao unir, o lado com mais
	// variantes costuma ser o que vale manter.
	Variantes int `json:"variantes"`
	// TemMarcacao aponta qual dos dois carrega o "Lançamento" — é o que permite julgar
	// sem comparar os nomes caractere a caractere.
	TemMarcacao bool `json:"tem_marcacao"`
}

// ConteudosParaComparacao carrega o catálogo ativo para a detecção em memória.
//
// A comparação é feita em Go, e não em SQL, porque a chave depende das regras de limpeza de
// título que já existem e estão testadas. Reimplementá-las em SQL criaria duas verdades
// sobre o que é o mesmo filme.
func (s *Store) ConteudosParaComparacao(ctx context.Context) ([]ConteudoParaComparar, []ConteudoDuplicado, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.type, c.title, c.year, c.poster_url,
		       CASE WHEN c.type = 'series' THEN (
		           SELECT count(*)::int FROM seasons se
		           JOIN episodes e ON e.season_id = se.id
		           JOIN source_variants v ON v.target_kind = 'episode' AND v.target_id = e.id
		           WHERE se.series_content_id = c.id
		       ) ELSE (
		           SELECT count(*)::int FROM source_variants v
		           WHERE v.target_kind = 'content' AND v.target_id = c.id
		       ) END
		FROM contents c
		WHERE c.status = 'active'
		ORDER BY c.id`)
	if err != nil {
		return nil, nil, wrapErr("carregando conteúdos para comparação", err)
	}
	defer rows.Close()

	var chaves []ConteudoParaComparar
	var detalhes []ConteudoDuplicado
	for rows.Next() {
		var d ConteudoDuplicado
		if err := rows.Scan(&d.ID, &d.Tipo, &d.Titulo, &d.Ano, &d.PosterURL, &d.Variantes); err != nil {
			return nil, nil, wrapErr("carregando conteúdos para comparação", err)
		}
		detalhes = append(detalhes, d)
		chaves = append(chaves, ConteudoParaComparar{ID: d.ID, Tipo: d.Tipo, Ano: d.Ano})
	}
	return chaves, detalhes, wrapErr("carregando conteúdos para comparação", rows.Err())
}

// ParesIgnorados devolve o que o administrador já declarou serem conteúdos diferentes.
func (s *Store) ParesIgnorados(ctx context.Context) (map[[2]int64]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT conteudo_a, conteudo_b FROM duplicatas_ignoradas`)
	if err != nil {
		return nil, wrapErr("carregando pares ignorados", err)
	}
	defer rows.Close()

	out := map[[2]int64]bool{}
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return nil, wrapErr("carregando pares ignorados", err)
		}
		out[[2]int64{a, b}] = true
	}
	return out, wrapErr("carregando pares ignorados", rows.Err())
}

// IgnorarPar registra que estes dois conteúdos são diferentes.
func (s *Store) IgnorarPar(ctx context.Context, a, b, decididoPor int64) error {
	menor, maior := ordenar(a, b)
	var quem *int64
	if decididoPor > 0 {
		quem = &decididoPor
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO duplicatas_ignoradas (conteudo_a, conteudo_b, decidido_por)
		VALUES ($1, $2, $3)
		ON CONFLICT (conteudo_a, conteudo_b) DO NOTHING`, menor, maior, quem)
	return wrapErr("ignorando par de duplicatas", err)
}

// UnirConteudos move tudo de `origem` para `destino` e remove a origem.
//
// O que muda de dono: as variantes, os episódios (via temporadas) e as decisões de
// matching. O conteúdo de origem deixa de existir.
//
// # A consequência que precisa estar clara
//
// O identificador da origem para de funcionar. Quem já importou aquele id — o XC_VM, por
// exemplo — vai receber "não encontrado" até reimportar. É o preço de unir dois conteúdos
// que nunca deveriam ter sido separados, e por isso a decisão é do administrador.
func (s *Store) UnirConteudos(ctx context.Context, destino, origem int64) (int64, error) {
	if destino == origem {
		return 0, wrapErr("unindo conteúdos", ErrInvalid)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, wrapErr("unindo conteúdos", err)
	}
	defer tx.Rollback(ctx)

	// Séries: as temporadas passam a pertencer ao destino. Episódios acompanham, porque
	// pendem da temporada.
	if _, err := tx.Exec(ctx,
		`UPDATE seasons SET series_content_id = $1 WHERE series_content_id = $2`,
		destino, origem); err != nil {
		return 0, wrapErr("movendo temporadas", err)
	}

	// Filmes: as variantes apontam direto para o conteúdo.
	tag, err := tx.Exec(ctx, `
		UPDATE source_variants
		SET target_id = $1, updated_at = now()
		WHERE target_kind = 'content' AND target_id = $2`, destino, origem)
	if err != nil {
		return 0, wrapErr("movendo variantes", err)
	}
	movidas := tag.RowsAffected()

	// As reproduções já registradas apontam para o conteúdo antigo. Redirecioná-las
	// preserva o histórico de audiência, que some se a linha for apagada em cascata.
	if _, err := tx.Exec(ctx,
		`UPDATE streams SET content_id = $1 WHERE content_id = $2`, destino, origem); err != nil {
		return 0, wrapErr("movendo histórico de reproduções", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM contents WHERE id = $1`, origem); err != nil {
		return 0, wrapErr("removendo o conteúdo unido", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, wrapErr("confirmando a união", err)
	}
	return movidas, nil
}

func ordenar(a, b int64) (int64, int64) {
	if a > b {
		return b, a
	}
	return a, b
}

// MontarSugestoes agrupa os candidatos e devolve os pares a revisar.
//
// Exige MESMO TIPO e MESMO ANO além da chave igual. Só o título produziria pares como
// dois filmes homônimos de décadas diferentes, que são obras distintas.
func MontarSugestoes(chaves []ConteudoParaComparar, detalhes []ConteudoDuplicado,
	ignorados map[[2]int64]bool, limite int) []SugestaoDeDuplicata {

	porID := make(map[int64]ConteudoDuplicado, len(detalhes))
	for _, d := range detalhes {
		porID[d.ID] = d
	}

	grupos := map[string][]int64{}
	for _, c := range chaves {
		if c.Chave == "" {
			continue
		}
		// Ano entra na chave para não juntar dois filmes homônimos de décadas
		// diferentes, que são obras distintas.
		ano := "sem-ano"
		if c.Ano != nil {
			ano = strconv.Itoa(*c.Ano)
		}
		chave := c.Tipo + "|" + c.Chave + "|" + ano
		grupos[chave] = append(grupos[chave], c.ID)
	}

	var out []SugestaoDeDuplicata
	for _, ids := range grupos {
		if len(ids) < 2 {
			continue
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				if ignorados[[2]int64{ids[i], ids[j]}] {
					continue
				}
				out = append(out, SugestaoDeDuplicata{A: porID[ids[i]], B: porID[ids[j]]})
				if limite > 0 && len(out) >= limite {
					return out
				}
			}
		}
	}
	return out
}
