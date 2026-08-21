package store

import (
	"context"
)

// Categorias PRINCIPAIS: as pastas que o administrador escolheu manter.
//
// A regra que governa tudo aqui: a sincronização nunca cria pasta. Ela só usa o que o
// administrador marcou como principal, e o que ele vinculou. Uma categoria de fonte
// desconhecida vira PENDÊNCIA — uma decisão a tomar uma vez, não um item a mesclar
// depois de já ter virado pasta.

// ChaveCategoria monta a chave de busca por nome normalizado e tipo.
func ChaveCategoria(normalizado, tipo string) string { return tipo + "|" + normalizado }

// CategoriasPrincipais devolve as principais indexadas por nome normalizado e tipo.
//
// Carregada uma vez por sincronização: sem isso, cada item do catálogo faria uma consulta
// para descobrir a pasta dele.
func (s *Store) CategoriasPrincipais(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, normalized_name, content_type FROM categories WHERE principal`)
	if err != nil {
		return nil, wrapErr("carregando categorias principais", err)
	}
	defer rows.Close()

	idx := map[string]int64{}
	for rows.Next() {
		var id int64
		var nome, tipo string
		if err := rows.Scan(&id, &nome, &tipo); err != nil {
			return nil, wrapErr("carregando categorias principais", err)
		}
		idx[ChaveCategoria(nome, tipo)] = id
	}
	return idx, wrapErr("carregando categorias principais", rows.Err())
}

// VinculosDaFonte devolve os vínculos já decididos para uma fonte.
//
// É o que faz a decisão valer para sempre: uma vez vinculada, a categoria daquela fonte
// não volta a aparecer como pendente, nem em sincronizações futuras.
func (s *Store) VinculosDaFonte(ctx context.Context, sourceID int64) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT normalized_name, content_type, category_id
		FROM source_categories
		WHERE source_id = $1 AND category_id IS NOT NULL`, sourceID)
	if err != nil {
		return nil, wrapErr("carregando vínculos da fonte", err)
	}
	defer rows.Close()

	idx := map[string]int64{}
	for rows.Next() {
		var nome, tipo string
		var id int64
		if err := rows.Scan(&nome, &tipo, &id); err != nil {
			return nil, wrapErr("carregando vínculos da fonte", err)
		}
		idx[ChaveCategoria(nome, tipo)] = id
	}
	return idx, wrapErr("carregando vínculos da fonte", rows.Err())
}

// RegistrarPendencia guarda uma categoria de fonte ainda sem destino.
//
// Não cria categoria canônica: é justamente o que se deixou de fazer. O conteúdo dessa
// categoria continua disponível e reproduzível — só fica numa pasta genérica até o
// administrador decidir.
func (s *Store) RegistrarPendencia(ctx context.Context, sourceID int64, externalID, declarado, normalizado, tipo string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO source_categories (source_id, external_id, declared_name, normalized_name, content_type, category_id)
		VALUES ($1, coalesce($2,''), $3, $4, $5, NULL)
		ON CONFLICT (source_id, content_type, normalized_name) DO UPDATE SET
			external_id   = excluded.external_id,
			declared_name = excluded.declared_name,
			last_seen_at  = now()`,
		sourceID, externalID, declarado, normalizado, tipo)
	return wrapErr("registrando categoria pendente", err)
}

// CategoriaPendente é uma categoria de fonte esperando decisão.
type CategoriaPendente struct {
	ID          int64  `json:"id"`
	SourceID    int64  `json:"source_id"`
	SourceName  string `json:"source_name"`
	Declarado   string `json:"declared_name"`
	Normalizado string `json:"normalized_name"`
	ContentType string `json:"content_type"`
	// Itens é quanto conteúdo está esperando esta decisão. Ordena a lista pelo que
	// realmente importa: uma pendência com três filmes não é urgente; uma com mil é.
	Itens int64 `json:"itens"`
	// SugestaoID é a principal de mesmo nome, quando existe. Sugestão por nome idêntico
	// é a única que não erra — as por semelhança geravam propostas absurdas.
	SugestaoID   *int64  `json:"sugestao_id"`
	SugestaoNome *string `json:"sugestao_nome"`
}

// ListarPendencias devolve as categorias de fonte sem vínculo.
//
// Só as pendentes: as já vinculadas não voltam a aparecer, que é a diferença entre uma
// caixa de entrada que esvazia e uma lista que nunca termina.
func (s *Store) ListarPendencias(ctx context.Context) ([]CategoriaPendente, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sc.id, sc.source_id, coalesce(src.name, ''), sc.declared_name,
		       sc.normalized_name, sc.content_type,
		       (SELECT count(*) FROM contents c
		         WHERE c.category_id IS NULL AND c.status = 'active'
		           AND c.type = sc.content_type) AS itens,
		       p.id, p.name
		FROM source_categories sc
		LEFT JOIN sources src ON src.id = sc.source_id
		LEFT JOIN categories p
		       ON p.principal
		      AND p.normalized_name = sc.normalized_name
		      AND p.content_type = sc.content_type
		WHERE sc.category_id IS NULL
		ORDER BY sc.content_type, sc.declared_name`)
	if err != nil {
		return nil, wrapErr("listando pendências de categoria", err)
	}
	defer rows.Close()

	out := []CategoriaPendente{}
	for rows.Next() {
		var p CategoriaPendente
		if err := rows.Scan(&p.ID, &p.SourceID, &p.SourceName, &p.Declarado,
			&p.Normalizado, &p.ContentType, &p.Itens, &p.SugestaoID, &p.SugestaoNome); err != nil {
			return nil, wrapErr("listando pendências de categoria", err)
		}
		out = append(out, p)
	}
	return out, wrapErr("listando pendências de categoria", rows.Err())
}

// MarcarPrincipal liga ou desliga a marcação de uma categoria.
func (s *Store) MarcarPrincipal(ctx context.Context, id int64, principal bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE categories SET principal = $2 WHERE id = $1`, id, principal)
	if err != nil {
		return wrapErr("marcando categoria principal", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("marcando categoria principal", ErrNotFound)
	}
	return nil
}

// CriarPrincipal cria uma categoria já marcada como principal.
//
// É o caminho de "esta categoria de fonte não tem onde encaixar": em vez de forçar um
// vínculo errado, o administrador promove a própria categoria a destino final.
func (s *Store) CriarPrincipal(ctx context.Context, nome, normalizado, tipo string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO categories (name, normalized_name, content_type, principal)
		VALUES ($1, $2, $3, true)
		ON CONFLICT (normalized_name, content_type) DO UPDATE SET
			principal = true,
			name = excluded.name
		RETURNING id`, nome, normalizado, tipo).Scan(&id)
	return id, wrapErr("criando categoria principal", err)
}

// AplicarVinculoRetroativo move o conteúdo que estava sem pasta para a categoria
// escolhida.
//
// Sem isto, vincular só valeria da próxima sincronização em diante — e o administrador
// veria a decisão dele não surtir efeito nenhum, que é a maneira mais rápida de perder a
// confiança na tela.
func (s *Store) AplicarVinculoRetroativo(ctx context.Context, sourceCategoryID, categoriaID int64) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE contents c
		SET category_id = $2, updated_at = now()
		FROM source_categories sc
		WHERE sc.id = $1
		  AND c.category_id IS NULL
		  AND c.type = sc.content_type
		  AND EXISTS (
		      SELECT 1 FROM source_variants v
		      WHERE v.source_id = sc.source_id
		        AND v.declared_group <> ''
		        AND (
		            (v.target_kind = 'content' AND v.target_id = c.id)
		         OR (c.type = 'series' AND v.target_kind = 'episode' AND v.target_id IN (
		                SELECT e.id FROM episodes e
		                JOIN seasons se ON se.id = e.season_id
		                WHERE se.series_content_id = c.id))
		        )
		  )`, sourceCategoryID, categoriaID)
	if err != nil {
		return 0, wrapErr("aplicando vínculo às pastas", err)
	}
	return tag.RowsAffected(), nil
}
