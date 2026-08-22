package store

import (
	"context"
)

// Apelidos de categoria: o nome que sempre cai numa pasta, venha de onde vier.
//
// Um vínculo (source_categories) responde "esta categoria DESTA fonte pertence àquela
// pasta". Um apelido responde "toda categoria com ESTE NOME pertence àquela pasta" — em
// qualquer fonte, inclusive nas que ainda não foram cadastradas.
//
// A diferença importa no momento em que a categoria de origem some. Unir uma pasta a outra
// apaga a pasta de origem; sem o apelido, o nome que a fonte continua declarando não bate
// com nada, e a categoria ressurge como pendência na sincronização seguinte — desfazendo,
// na prática, a decisão que acabou de ser tomada.

// ApelidoCategoria é um nome de categoria com destino fixo.
type ApelidoCategoria struct {
	ID          int64  `json:"id"`
	Declarado   string `json:"declared_name"`
	Normalizado string `json:"normalized_name"`
	ContentType string `json:"content_type"`
	CategoriaID int64  `json:"category_id"`
	// CategoriaNome é o nome da pasta de destino, para a tela não precisar cruzar listas.
	CategoriaNome string `json:"category_name"`
	Origem        string `json:"origem"`
	CriadoEm      string `json:"created_at"`
	// Fontes é em quantas fontes este nome aparece hoje. Zero significa que nenhuma fonte
	// declara mais essa categoria — o apelido virou histórico e pode ser removido sem
	// efeito nenhum.
	Fontes int64 `json:"fontes"`
}

// ApelidosCategoria devolve os apelidos indexados por nome normalizado e tipo.
//
// Carregado uma vez por sincronização, como as principais e os vínculos: a alternativa
// seria uma consulta por item do catálogo.
func (s *Store) ApelidosCategoria(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT normalized_name, content_type, category_id FROM category_aliases`)
	if err != nil {
		return nil, wrapErr("carregando apelidos de categoria", err)
	}
	defer rows.Close()

	idx := map[string]int64{}
	for rows.Next() {
		var nome, tipo string
		var id int64
		if err := rows.Scan(&nome, &tipo, &id); err != nil {
			return nil, wrapErr("carregando apelidos de categoria", err)
		}
		idx[ChaveCategoria(nome, tipo)] = id
	}
	return idx, wrapErr("carregando apelidos de categoria", rows.Err())
}

// ListarApelidos devolve os apelidos para a tela, com o destino e o alcance de cada um.
func (s *Store) ListarApelidos(ctx context.Context) ([]ApelidoCategoria, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.declared_name, a.normalized_name, a.content_type,
		       a.category_id, c.name, a.origem,
		       to_char(a.created_at, 'YYYY-MM-DD HH24:MI'),
		       (SELECT count(*) FROM source_categories sc
		         WHERE sc.normalized_name = a.normalized_name
		           AND sc.content_type   = a.content_type)
		FROM category_aliases a
		JOIN categories c ON c.id = a.category_id
		ORDER BY a.content_type, a.declared_name`)
	if err != nil {
		return nil, wrapErr("listando apelidos de categoria", err)
	}
	defer rows.Close()

	out := []ApelidoCategoria{}
	for rows.Next() {
		var a ApelidoCategoria
		if err := rows.Scan(&a.ID, &a.Declarado, &a.Normalizado, &a.ContentType,
			&a.CategoriaID, &a.CategoriaNome, &a.Origem, &a.CriadoEm, &a.Fontes); err != nil {
			return nil, wrapErr("listando apelidos de categoria", err)
		}
		out = append(out, a)
	}
	return out, wrapErr("listando apelidos de categoria", rows.Err())
}

// RegistrarApelidoDePendencia transforma uma decisão em regra de nome.
//
// Quando o administrador diz "esta categoria vai para aquela pasta", ele quase nunca quer
// dizer "só nesta fonte". Guardar a decisão pelo nome faz duas coisas de uma vez: as outras
// fontes que declaram o mesmo nome são resolvidas junto, e as fontes que ainda nem foram
// cadastradas já nascem com a decisão aplicada.
//
// Devolve quantas OUTRAS categorias de fonte foram resolvidas pela mesma decisão — número
// que a tela mostra, porque com uma centena de categorias essa é a diferença entre uma
// tarde de cliques e alguns minutos.
func (s *Store) RegistrarApelidoDePendencia(ctx context.Context, sourceCategoryID, destino int64) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, wrapErr("guardando o apelido", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var declarado, normalizado, tipo string
	if err := tx.QueryRow(ctx, `
		SELECT declared_name, normalized_name, content_type
		FROM source_categories WHERE id = $1`, sourceCategoryID).
		Scan(&declarado, &normalizado, &tipo); err != nil {
		return 0, wrapErr("guardando o apelido", err)
	}
	if tipo != ContentMovie && tipo != ContentSeries {
		return 0, nil
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO category_aliases (normalized_name, content_type, category_id, declared_name, origem)
		VALUES ($1, $2, $3, $4, 'vinculo')
		ON CONFLICT (normalized_name, content_type) DO UPDATE SET
			category_id   = excluded.category_id,
			declared_name = excluded.declared_name`,
		normalizado, tipo, destino, declarado); err != nil {
		return 0, wrapErr("guardando o apelido", err)
	}

	// As outras fontes com o mesmo nome, resolvidas pela mesma decisão. Só as que ainda
	// não tinham destino: uma decisão anterior e diferente continua valendo para a fonte
	// dela, porque o vínculo por fonte é consultado antes do apelido.
	tag, err := tx.Exec(ctx, `
		UPDATE source_categories SET category_id = $1
		WHERE id <> $2 AND category_id IS NULL
		  AND normalized_name = $3 AND content_type = $4`,
		destino, sourceCategoryID, normalizado, tipo)
	if err != nil {
		return 0, wrapErr("guardando o apelido: resolvendo as demais fontes", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, wrapErr("guardando o apelido", err)
	}
	return tag.RowsAffected(), nil
}

// RemoverApelido solta o nome: ele volta a pedir decisão.
//
// As linhas de source_categories que apontavam para o destino por causa deste apelido
// voltam a ficar pendentes. O conteúdo que já foi movido continua onde está — mover de
// volta exigiria saber de qual pasta cada item veio, e essa informação some junto com a
// pasta apagada. É uma limitação real, e a tela a diz em voz alta em vez de fingir que
// desfazer devolve tudo.
//
// Devolve quantas categorias de fonte voltaram a ser pendências.
func (s *Store) RemoverApelido(ctx context.Context, id int64) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, wrapErr("removendo apelido", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var nome, tipo string
	var destino int64
	if err := tx.QueryRow(ctx,
		`SELECT normalized_name, content_type, category_id FROM category_aliases WHERE id = $1`,
		id).Scan(&nome, &tipo, &destino); err != nil {
		return 0, wrapErr("removendo apelido", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE source_categories SET category_id = NULL
		WHERE normalized_name = $1 AND content_type = $2 AND category_id = $3`,
		nome, tipo, destino)
	if err != nil {
		return 0, wrapErr("removendo apelido: soltando vínculos", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM category_aliases WHERE id = $1`, id); err != nil {
		return 0, wrapErr("removendo apelido", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, wrapErr("removendo apelido", err)
	}
	return tag.RowsAffected(), nil
}

// ReativarApelido devolve o nome à condição de pasta própria e principal.
//
// É o caminho de "mudei de ideia": a categoria que tinha sido dobrada dentro de outra volta
// a existir, marcada como principal, e o conteúdo que veio dela volta para ela.
//
// O conteúdo é reencontrado pelo grupo que a FONTE declarou em cada variante — que é a
// única marca que sobrevive à pasta ter sido apagada. Itens que a fonte parou de declarar
// nesse grupo ficam onde estão; não há de onde tirar a informação.
//
// Devolve o id da categoria recriada e quantos conteúdos voltaram.
func (s *Store) ReativarApelido(ctx context.Context, id int64) (int64, int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, wrapErr("reativando apelido", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var declarado, normalizado, tipo string
	var antigoDestino int64
	if err := tx.QueryRow(ctx, `
		SELECT declared_name, normalized_name, content_type, category_id
		FROM category_aliases WHERE id = $1`, id).
		Scan(&declarado, &normalizado, &tipo, &antigoDestino); err != nil {
		return 0, 0, wrapErr("reativando apelido", err)
	}
	if declarado == "" {
		declarado = normalizado
	}

	var nova int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO categories (name, normalized_name, content_type, principal)
		VALUES ($1, $2, $3, true)
		ON CONFLICT (normalized_name, content_type) DO UPDATE SET principal = true
		RETURNING id`, declarado, normalizado, tipo).Scan(&nova); err != nil {
		return 0, 0, wrapErr("reativando apelido: recriando a pasta", err)
	}

	// O conteúdo volta pelo grupo declarado nas variantes. Só sai do destino atual: um
	// item que o administrador moveu para outro lugar depois não é arrastado de volta.
	tag, err := tx.Exec(ctx, `
		UPDATE contents c
		SET category_id = $1, updated_at = now()
		WHERE c.category_id = $2
		  AND c.type = $3
		  AND EXISTS (
		      SELECT 1 FROM source_variants v
		      WHERE lower(v.declared_group) = lower($4)
		        AND (
		            (v.target_kind = 'content' AND v.target_id = c.id)
		         OR (c.type = 'series' AND v.target_kind = 'episode' AND v.target_id IN (
		                SELECT e.id FROM episodes e
		                JOIN seasons se ON se.id = e.season_id
		                WHERE se.series_content_id = c.id))
		        )
		  )`, nova, antigoDestino, tipo, declarado)
	if err != nil {
		return 0, 0, wrapErr("reativando apelido: trazendo o conteúdo de volta", err)
	}

	// Os vínculos das fontes passam a apontar para a pasta recriada: sem isto, a próxima
	// sincronização mandaria o conteúdo novo de volta para o destino antigo, e a pasta
	// reativada ficaria congelada no que existia hoje.
	if _, err := tx.Exec(ctx, `
		UPDATE source_categories SET category_id = $1
		WHERE normalized_name = $2 AND content_type = $3`,
		nova, normalizado, tipo); err != nil {
		return 0, 0, wrapErr("reativando apelido: religando as fontes", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM category_aliases WHERE id = $1`, id); err != nil {
		return 0, 0, wrapErr("reativando apelido", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, wrapErr("reativando apelido", err)
	}
	return nova, tag.RowsAffected(), nil
}
