package store

import (
	"context"
	"fmt"
	"strconv"
)

// Acervo próprio: o conteúdo que o administrador coloca no catálogo.
//
// # A decisão que governa este arquivo
//
// Conteúdo enviado pelo painel entra no catálogo pela MESMA porta que o conteúdo das
// fontes: uma linha em `contents` e uma variante em `source_variants`.
//
// A alternativa seria um caminho paralelo — "conteúdo sem fonte" — e ela custaria caro sem
// entregar nada. A exportação M3U monta os links a partir de variantes. A API Xtream, idem.
// A reprodução escolhe entre variantes. A prioridade ordena variantes. Um conteúdo que não
// tivesse variante precisaria de um caso especial em cada um desses quatro lugares, cada um
// com o seu jeito próprio de estar errado.
//
// Entrando pela porta comum, um filme que você enviou é indistinguível de um filme da fonte
// para todo o resto do sistema — e é exatamente isso que se quer.

// NomeDaFonteDoAcervo é como a fonte interna aparece na tela de Fontes.
//
// Ela aparece, e é de propósito: esconder produziria a pergunta "de onde veio esta
// variante?" sem resposta possível no painel.
const NomeDaFonteDoAcervo = "Acervo próprio"

// FonteDoAcervo devolve a fonte interna, criando-a na primeira vez.
//
// Desabilitada e com intervalo longo: ela nunca é sincronizada — não há URL para consultar
// nem catálogo para ler —, e o agendador já a ignora pelo tipo. As duas coisas juntas são
// cinto e suspensório, porque uma execução de sincronização contra ela só poderia falhar.
func (s *Store) FonteDoAcervo(ctx context.Context) (*Source, error) {
	row := s.pool.QueryRow(ctx, `
		WITH nova AS (
			INSERT INTO sources (name, description, kind, base_url, priority, enabled)
			VALUES ($1,
			        'Fonte interna: os arquivos que você enviou pelo painel. Não é sincronizada.',
			        'proprio', 'interno://acervo', 1, false)
			ON CONFLICT (name) DO NOTHING
			RETURNING *
		)
		SELECT `+sourceColumns+` FROM nova s
		UNION ALL
		SELECT `+sourceColumns+` FROM sources s WHERE s.name = $1
		LIMIT 1`, NomeDaFonteDoAcervo)

	src, err := scanSource(row)
	return src, wrapErr("obtendo a fonte do acervo próprio", err)
}

// ConteudoProprio descreve o que será criado a partir de um arquivo enviado.
type ConteudoProprio struct {
	Titulo string
	// TituloNormalizado vem pronto de quem chama.
	//
	// A normalizacao vive em internal/ingest, junto com o resto das regras de titulo. Faze-la
	// aqui obrigaria a camada de dados a conhecer regras de catalogo — e a duplicar, mais
	// cedo ou mais tarde, uma logica que precisa ser identica a da sincronizacao.
	TituloNormalizado string
	Ano               *int
	// Tipo é ContentMovie ou ContentSeries. Séries entram como conteúdo próprio só quando
	// o envio é do episódio; aqui tratamos o alvo já resolvido.
	Tipo string
	// CategoriaID é opcional: sem categoria, o conteúdo existe e fica fora das pastas.
	CategoriaID  *int64
	ContainerExt string
	// Backend e NuvemID dizem onde o arquivo vai ficar.
	Backend string
	NuvemID *int64
	// Bytes e Localizador vêm preenchidos quando o arquivo JÁ está guardado — o caso do
	// upload, em que a gravação termina antes do registro.
	Bytes       int64
	Localizador string
}

// ResultadoDoEnvio reúne o que foi criado.
type ResultadoDoEnvio struct {
	ContentID int64 `json:"content_id"`
	VariantID int64 `json:"variant_id"`
	ArquivoID int64 `json:"arquivo_id"`
}

// CriarConteudoProprio registra o conteúdo, a variante e o arquivo, numa transação só.
//
// Numa transação porque os três são um fato único: um conteúdo sem variante é invisível,
// uma variante sem arquivo é um link quebrado, e um arquivo sem conteúdo é espaço ocupado
// que ninguém encontra. Meio caminho aqui é pior que nenhum.
func (s *Store) CriarConteudoProprio(ctx context.Context, fonteID int64, c ConteudoProprio) (*ResultadoDoEnvio, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrapErr("registrando conteúdo próprio", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tipo := c.Tipo
	if tipo != ContentMovie && tipo != ContentSeries {
		tipo = ContentMovie
	}

	var res ResultadoDoEnvio
	if err := tx.QueryRow(ctx, `
		INSERT INTO contents (type, title, normalized_title, year, category_id, status)
		VALUES ($1, $2, $3, $4, $5, 'active')
		RETURNING id`,
		tipo, c.Titulo, c.TituloNormalizado, c.Ano, c.CategoriaID).Scan(&res.ContentID); err != nil {
		return nil, wrapErr("registrando conteúdo próprio", err)
	}

	// A identidade da variante é o próprio conteúdo: não há id externo nem URL de origem
	// de onde derivar um. `stream_ref` marca a procedência para quem for depurar mais
	// tarde — e satisfaz a exigência de a variante ter alguma forma de mídia.
	if err := tx.QueryRow(ctx, `
		INSERT INTO source_variants
			(source_id, target_kind, target_id, external_id, stream_ref,
			 container_ext, declared_title, available)
		VALUES ($1, 'content', $2, $3, $4::jsonb, $5, $6, true)
		RETURNING id`,
		fonteID, res.ContentID, "acervo:"+strconv.FormatInt(res.ContentID, 10),
		`{"acervo":true}`, c.ContainerExt, c.Titulo).Scan(&res.VariantID); err != nil {
		return nil, wrapErr("registrando a variante do conteúdo próprio", err)
	}

	if err := tx.QueryRow(ctx, `
		INSERT INTO arquivos_guardados
			(variant_id, target_kind, target_id, backend, nuvem_id, localizador,
			 bytes, bytes_baixados, container_ext, estado, origem, concluido_em)
		VALUES ($1, 'content', $2, $3, $4, $5, $6, $6, $7, 'pronto', 'proprio', now())
		RETURNING id`,
		res.VariantID, res.ContentID, c.Backend, c.NuvemID, c.Localizador,
		c.Bytes, c.ContainerExt).Scan(&res.ArquivoID); err != nil {
		return nil, wrapErr("registrando o arquivo do conteúdo próprio", err)
	}

	// A variante recém-criada vira a primária: sem isso o conteúdo existiria com uma
	// origem que a reprodução não escolheria.
	if _, err := tx.Exec(ctx,
		`UPDATE contents SET primary_variant_id = $2 WHERE id = $1`,
		res.ContentID, res.VariantID); err != nil {
		return nil, wrapErr("apontando a variante primária", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, wrapErr("registrando conteúdo próprio", err)
	}
	return &res, nil
}

// DesfazerConteudoProprio remove o que CriarConteudoProprio criou.
//
// Existe para o caminho de erro do envio: se a gravação do arquivo falhar depois de o
// registro ter sido feito, o catálogo ficaria com um filme que não abre. Apagar é melhor
// que deixar um link quebrado à espera de alguém apertar o play.
func (s *Store) DesfazerConteudoProprio(ctx context.Context, contentID int64) error {
	// As variantes e o arquivo caem por ON DELETE CASCADE.
	_, err := s.pool.Exec(ctx, `DELETE FROM contents WHERE id = $1`, contentID)
	return wrapErr(fmt.Sprintf("desfazendo o conteúdo próprio %d", contentID), err)
}
