package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Arquivos guardados: o acervo que fica nesta operação.
//
// Duas origens que não podem se misturar, e é a distinção que governa tudo neste arquivo:
//
//   OrigemFonte   — cache. A fonte ainda tem o original; apagar custa uma releitura.
//   OrigemProprio — acervo do administrador. Não existe em lugar nenhum além daqui.
//
// Nenhuma consulta de limpeza deste arquivo alcança OrigemProprio. Não por convenção: as
// cláusulas WHERE dizem isso explicitamente, e o índice do banco também.

// Origens de um arquivo guardado.
const (
	OrigemFonte   = "fonte"
	OrigemProprio = "proprio"
)

// Estados de um arquivo guardado.
const (
	ArquivoPendente  = "pendente"
	ArquivoBaixando  = "baixando"
	ArquivoPronto    = "pronto"
	ArquivoErro      = "erro"
	ArquivoRemovendo = "removendo"
)

// Backends de armazenamento.
//
// São dois, e não um por provedor de nuvem: qual nuvem é dito por NuvemID, apontando para
// uma conta cadastrada. Assim acrescentar um provedor é código novo e uma linha na tabela
// de contas — nunca uma migração que altera um CHECK sobre milhões de linhas.
const (
	BackendLocal = "local"
	BackendNuvem = "nuvem"
)

// ArquivoGuardado é uma cópia de mídia sob nossa guarda.
type ArquivoGuardado struct {
	ID            int64      `json:"id"`
	VariantID     *int64     `json:"variant_id"`
	TargetKind    string     `json:"target_kind"`
	TargetID      int64      `json:"target_id"`
	Backend       string     `json:"backend"`
	NuvemID       *int64     `json:"nuvem_id"`
	Localizador   string     `json:"-"` // nunca vai para a API: é caminho de disco ou id da nuvem
	Bytes         int64      `json:"bytes"`
	BytesBaixados int64      `json:"bytes_baixados"`
	BytesTotais   *int64     `json:"bytes_totais"`
	ContainerExt  string     `json:"container_ext"`
	Estado        string     `json:"estado"`
	Erro          string     `json:"erro"`
	Origem        string     `json:"origem"`
	Protegido     bool       `json:"protegido"`
	Acessos       int64      `json:"acessos"`
	UltimoAcesso  *time.Time `json:"ultimo_acesso_em"`
	CriadoEm      time.Time  `json:"criado_em"`
	ConcluidoEm   *time.Time `json:"concluido_em"`
}

// Descartavel informa se a limpeza automática pode apagar este arquivo.
//
// Existe como método para que a regra tenha UM lugar. Espalhada por chamadores, ela
// acabaria implementada em cinco lugares e esquecida no sexto — e o sexto seria o que
// apagou o acervo de alguém.
func (a *ArquivoGuardado) Descartavel() bool {
	return a.Origem == OrigemFonte && !a.Protegido && a.Estado == ArquivoPronto
}

// camposArquivo é a ordem em que as colunas são lidas. Uma lista só, para que a leitura e
// as consultas não possam divergir.
var camposArquivo = []string{
	"id", "variant_id", "target_kind", "target_id", "backend", "nuvem_id", "localizador",
	"bytes", "bytes_baixados", "bytes_totais", "container_ext", "estado", "erro", "origem",
	"protegido", "acessos", "ultimo_acesso_em", "criado_em", "concluido_em",
}

// colunasArquivo serve às consultas de uma tabela só.
var colunasArquivo = strings.Join(camposArquivo, ", ")

// colunasArquivoDe qualifica as colunas com o apelido da tabela.
//
// Obrigatório em toda consulta com junção, e a razão é concreta: `id` e `criado_em` existem
// tanto em arquivos_guardados quanto em nuvens. Sem o prefixo o Postgres recusa a consulta
// inteira por ambiguidade — foi assim que a tela do Acervo parou de abrir, com um "erro
// interno" que não dizia nada.
//
// Derivar do mesmo lugar em vez de escrever uma segunda lista à mão: duas listas divergem, e
// a divergência aparece como uma coluna lida na posição errada — silenciosa, e muito pior
// que um erro de sintaxe.
func colunasArquivoDe(apelido string) string {
	qualificadas := make([]string, len(camposArquivo))
	for i, campo := range camposArquivo {
		qualificadas[i] = apelido + "." + campo
	}
	return strings.Join(qualificadas, ", ")
}

func lerArquivo(linha pgx.Row) (*ArquivoGuardado, error) {
	var a ArquivoGuardado
	err := linha.Scan(&a.ID, &a.VariantID, &a.TargetKind, &a.TargetID, &a.Backend, &a.NuvemID,
		&a.Localizador, &a.Bytes, &a.BytesBaixados, &a.BytesTotais, &a.ContainerExt,
		&a.Estado, &a.Erro, &a.Origem, &a.Protegido, &a.Acessos, &a.UltimoAcesso,
		&a.CriadoEm, &a.ConcluidoEm)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// ArquivoProntoDaVariante devolve a cópia utilizável de uma variante, se houver.
//
// É a consulta do caminho quente: roda a cada pedido de reprodução, antes de decidir entre
// servir do disco e puxar da fonte. Por isso ela é exatamente isto — um índice parcial e
// uma linha — e não uma junção com o catálogo.
//
// Ausência não é erro. É a resposta "não temos, vá à fonte", que é o caminho normal
// enquanto o cache estiver desligado.
func (s *Store) ArquivoProntoDaVariante(ctx context.Context, variantID int64) (*ArquivoGuardado, error) {
	a, err := lerArquivo(s.pool.QueryRow(ctx,
		`SELECT `+colunasArquivo+` FROM arquivos_guardados
		 WHERE variant_id = $1 AND estado = 'pronto'`, variantID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, wrapErr("buscando arquivo guardado da variante", err)
	}
	return a, nil
}

// NovoArquivo é o pedido para guardar alguma coisa.
type NovoArquivo struct {
	VariantID  *int64
	TargetKind string
	TargetID   int64
	Backend    string
	// NuvemID é obrigatório quando Backend é BackendNuvem, e proibido quando é local. O
	// banco recusa as duas combinações erradas.
	NuvemID      *int64
	Origem       string
	ContainerExt string
	BytesTotais  *int64
	// Localizador vem preenchido quando o arquivo JÁ está guardado — o caso do upload,
	// em que a gravação acontece antes do registro. Vazio enfileira para baixar.
	Localizador string
	Bytes       int64
	Protegido   bool
}

// EnfileirarArquivo registra a intenção de guardar, sem guardar nada ainda.
//
// Devolve o registro existente quando a variante já tem cópia: pedir duas vezes é o caso
// normal (dois espectadores pedem o mesmo filme no mesmo minuto), e não pode virar dois
// downloads do mesmo vídeo.
func (s *Store) EnfileirarArquivo(ctx context.Context, novo NovoArquivo) (*ArquivoGuardado, error) {
	estado := ArquivoPendente
	var concluido *time.Time
	if novo.Localizador != "" {
		estado = ArquivoPronto
		agora := time.Now()
		concluido = &agora
	}

	// ON CONFLICT sobre o índice parcial da variante. DO UPDATE com um campo inócuo em vez
	// de DO NOTHING porque este precisa devolver a linha existente — DO NOTHING não
	// devolve nada, e o chamador ficaria sem saber o id do download que já estava em curso.
	if novo.VariantID != nil {
		a, err := lerArquivo(s.pool.QueryRow(ctx, `
			INSERT INTO arquivos_guardados
				(variant_id, target_kind, target_id, backend, nuvem_id, localizador, bytes,
				 bytes_totais, container_ext, estado, origem, protegido, concluido_em)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT (variant_id) WHERE variant_id IS NOT NULL
			DO UPDATE SET container_ext = excluded.container_ext
			RETURNING `+colunasArquivo,
			novo.VariantID, novo.TargetKind, novo.TargetID, novo.Backend, novo.NuvemID,
			novo.Localizador, novo.Bytes, novo.BytesTotais, novo.ContainerExt, estado,
			novo.Origem, novo.Protegido, concluido))
		if err != nil {
			return nil, wrapErr("enfileirando arquivo", err)
		}
		return a, nil
	}

	// Acervo próprio: sem variante, e sem conflito a resolver — dois envios do mesmo filme
	// são dois arquivos, e é o administrador quem decide se algum sobra.
	a, err := lerArquivo(s.pool.QueryRow(ctx, `
		INSERT INTO arquivos_guardados
			(target_kind, target_id, backend, nuvem_id, localizador, bytes, bytes_totais,
			 container_ext, estado, origem, protegido, concluido_em)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING `+colunasArquivo,
		novo.TargetKind, novo.TargetID, novo.Backend, novo.NuvemID, novo.Localizador,
		novo.Bytes, novo.BytesTotais, novo.ContainerExt, estado, novo.Origem,
		novo.Protegido, concluido))
	if err != nil {
		return nil, wrapErr("enfileirando arquivo", err)
	}
	return a, nil
}

// TomarDaFila reserva o próximo arquivo a baixar, para um trabalhador só.
//
// FOR UPDATE SKIP LOCKED é o que permite mais de um trabalhador sem coordenação externa:
// cada um pega uma linha diferente, e nenhum espera pelo outro. Sem o SKIP LOCKED, dois
// trabalhadores fariam fila para o mesmo vídeo — e o segundo baixaria de novo o que o
// primeiro acabou de guardar.
func (s *Store) TomarDaFila(ctx context.Context) (*ArquivoGuardado, error) {
	a, err := lerArquivo(s.pool.QueryRow(ctx, `
		UPDATE arquivos_guardados SET estado = 'baixando'
		WHERE id = (
			SELECT id FROM arquivos_guardados
			WHERE estado = 'pendente'
			ORDER BY criado_em
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+colunasArquivo))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, wrapErr("tomando arquivo da fila", err)
	}
	return a, nil
}

// AnotarProgresso registra quanto já chegou.
//
// Chamada de tempos em tempos durante o download, e não a cada bloco: um vídeo de 2 GB
// tem milhares de blocos, e uma escrita no banco por bloco custaria mais que a cópia.
func (s *Store) AnotarProgresso(ctx context.Context, id, baixados int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE arquivos_guardados SET bytes_baixados = $2 WHERE id = $1 AND estado = 'baixando'`,
		id, baixados)
	return wrapErr("anotando progresso do download", err)
}

// ConcluirArquivo marca a cópia como utilizável.
func (s *Store) ConcluirArquivo(ctx context.Context, id int64, localizador string, bytes int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE arquivos_guardados
		SET estado = 'pronto', localizador = $2, bytes = $3, bytes_baixados = $3,
		    erro = '', concluido_em = now()
		WHERE id = $1`, id, localizador, bytes)
	if err != nil {
		return wrapErr("concluindo arquivo", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("concluindo arquivo", ErrNotFound)
	}
	return nil
}

// FalharArquivo registra o motivo de não ter dado certo.
//
// O registro fica, em vez de a linha ser apagada: sem ele, um download que falha sempre
// seria tentado para sempre, e a tela não teria o que mostrar além da ausência.
func (s *Store) FalharArquivo(ctx context.Context, id int64, motivo string) error {
	const limite = 500
	if len(motivo) > limite {
		motivo = motivo[:limite]
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE arquivos_guardados SET estado = 'erro', erro = $2 WHERE id = $1`, id, motivo)
	return wrapErr("registrando falha do arquivo", err)
}

// RegistrarAcessoAoArquivo conta mais um uso.
//
// É o que alimenta a limpeza: sem esta contagem, "o que é pouco usado" não teria resposta e
// a escolha do que apagar viraria sorteio. Uma escrita por reprodução iniciada — não por
// byte entregue.
func (s *Store) RegistrarAcessoAoArquivo(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE arquivos_guardados SET acessos = acessos + 1, ultimo_acesso_em = now()
		 WHERE id = $1`, id)
	return wrapErr("registrando acesso ao arquivo", err)
}

// ProtegerArquivo liga ou desliga a proteção contra a limpeza automática.
func (s *Store) ProtegerArquivo(ctx context.Context, id int64, protegido bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE arquivos_guardados SET protegido = $2 WHERE id = $1`, id, protegido)
	if err != nil {
		return wrapErr("protegendo arquivo", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("protegendo arquivo", ErrNotFound)
	}
	return nil
}

// MarcarParaRemocao põe o arquivo na fila de remoção.
//
// Dois passos, e não um DELETE: o arquivo existe no banco E no armazenamento, e apagar a
// linha primeiro deixaria o arquivo órfão lá, ocupando espaço que ninguém mais sabe que
// está ocupado. Marcar, apagar no backend, e só então remover a linha.
func (s *Store) MarcarParaRemocao(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE arquivos_guardados SET estado = 'removendo' WHERE id = $1`, id)
	if err != nil {
		return wrapErr("marcando arquivo para remoção", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("marcando arquivo para remoção", ErrNotFound)
	}
	return nil
}

// EsquecerArquivo apaga a linha. Só depois de o backend ter apagado o arquivo.
func (s *Store) EsquecerArquivo(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM arquivos_guardados WHERE id = $1`, id)
	return wrapErr("esquecendo arquivo", err)
}

// CandidatosParaLimpeza devolve o cache descartável, do menos usado para o mais usado.
//
// O WHERE diz a regra inteira em voz alta, e é de propósito: acervo próprio nunca entra,
// protegido nunca entra, e o que ainda está sendo baixado também não. Uma condição a menos
// aqui é acervo perdido em produção.
//
// `idadeMinima` evita o vaivém: um arquivo guardado há dez minutos ser apagado para caber
// outro, que na hora seguinte é apagado para caber o primeiro de novo. Gasta banda dos dois
// lados e não melhora nada.
func (s *Store) CandidatosParaLimpeza(ctx context.Context, idadeMinima time.Duration, limite int) ([]ArquivoGuardado, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+colunasArquivo+` FROM arquivos_guardados
		WHERE estado = 'pronto'
		  AND origem = 'fonte'
		  AND NOT protegido
		  AND concluido_em < now() - $1::interval
		ORDER BY ultimo_acesso_em NULLS FIRST, acessos, concluido_em
		LIMIT $2`, idadeMinima.String(), limite)
	if err != nil {
		return nil, wrapErr("listando candidatos à limpeza", err)
	}
	defer rows.Close()

	out := []ArquivoGuardado{}
	for rows.Next() {
		a, err := lerArquivo(rows)
		if err != nil {
			return nil, wrapErr("listando candidatos à limpeza", err)
		}
		out = append(out, *a)
	}
	return out, wrapErr("listando candidatos à limpeza", rows.Err())
}

// ArquivoPorID busca um arquivo do acervo.
func (s *Store) ArquivoPorID(ctx context.Context, id int64) (*ArquivoGuardado, error) {
	a, err := lerArquivo(s.pool.QueryRow(ctx,
		`SELECT `+colunasArquivo+` FROM arquivos_guardados WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, wrapErr("buscando arquivo do acervo", err)
	}
	return a, nil
}

// FiltroDeArquivos são os cortes que a tela do acervo oferece.
type FiltroDeArquivos struct {
	Origem string
	Estado string
	Limite int
}

// ArquivoNaTela é um arquivo guardado com o que o painel precisa mostrar junto.
//
// O título vem do catálogo, e não do arquivo: uma listagem de acervo sem os títulos é uma
// lista de números, e ninguém decide o que apagar olhando ids.
type ArquivoNaTela struct {
	ArquivoGuardado
	Titulo    string  `json:"titulo"`
	NuvemNome *string `json:"nuvem_nome"`
	FonteNome *string `json:"fonte_nome"`
}

// ListarArquivos devolve o acervo para a tela.
func (s *Store) ListarArquivos(ctx context.Context, f FiltroDeArquivos) ([]ArquivoNaTela, error) {
	if f.Limite <= 0 || f.Limite > 1000 {
		f.Limite = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+colunasArquivoDe("a")+`,
		       coalesce(
		           CASE a.target_kind
		               WHEN 'content' THEN (SELECT c.title FROM contents c WHERE c.id = a.target_id)
		               -- Episódio sem a série a que pertence é uma linha inútil: "Episódio 3"
		               -- não diz de que série, e um acervo com centenas deles vira uma lista
		               -- de nomes repetidos que ninguém consegue usar para decidir nada.
		               --
		               -- O formato fica "Série · S01E03 · Título", com o título só quando
		               -- ele existe — muitas fontes não o trazem, e um separador solto no fim
		               -- seria pior que a ausência.
		               WHEN 'episode' THEN (
		                   SELECT serie.title
		                          || ' · S' || lpad(se.season_number::text, 2, '0')
		                          || 'E' || lpad(e.episode_number::text, 2, '0')
		                          || CASE WHEN btrim(e.title) <> '' THEN ' · ' || e.title ELSE '' END
		                   FROM episodes e
		                   JOIN seasons se ON se.id = e.season_id
		                   JOIN contents serie ON serie.id = se.series_content_id
		                   WHERE e.id = a.target_id)
		           END, '') AS titulo,
		       n.nome,
		       (SELECT src.name FROM source_variants v
		         JOIN sources src ON src.id = v.source_id
		        WHERE v.id = a.variant_id)
		FROM arquivos_guardados a
		LEFT JOIN nuvens n ON n.id = a.nuvem_id
		WHERE ($1 = '' OR a.origem = $1)
		  AND ($2 = '' OR a.estado = $2)
		ORDER BY a.criado_em DESC
		LIMIT $3`, f.Origem, f.Estado, f.Limite)
	if err != nil {
		return nil, wrapErr("listando o acervo", err)
	}
	defer rows.Close()

	out := []ArquivoNaTela{}
	for rows.Next() {
		var t ArquivoNaTela
		a := &t.ArquivoGuardado
		if err := rows.Scan(&a.ID, &a.VariantID, &a.TargetKind, &a.TargetID, &a.Backend,
			&a.NuvemID, &a.Localizador, &a.Bytes, &a.BytesBaixados, &a.BytesTotais,
			&a.ContainerExt, &a.Estado, &a.Erro, &a.Origem, &a.Protegido, &a.Acessos,
			&a.UltimoAcesso, &a.CriadoEm, &a.ConcluidoEm,
			&t.Titulo, &t.NuvemNome, &t.FonteNome); err != nil {
			return nil, wrapErr("listando o acervo", err)
		}
		out = append(out, t)
	}
	return out, wrapErr("listando o acervo", rows.Err())
}

// UsoDoAcervo resume o que está guardado, por backend e por origem.
//
// Separado por origem porque as duas respondem a perguntas diferentes: o cache responde
// "quanto estou economizando de banda", o acervo próprio responde "quanto do meu disco é
// insubstituível". Somados, os dois viram um número que não ajuda a decidir nada.
type UsoDoAcervo struct {
	Backend  string `json:"backend"`
	Origem   string `json:"origem"`
	Arquivos int64  `json:"arquivos"`
	Bytes    int64  `json:"bytes"`
}

// ResumoDoAcervo agrupa o que está guardado.
func (s *Store) ResumoDoAcervo(ctx context.Context) ([]UsoDoAcervo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT backend, origem, count(*), coalesce(sum(bytes), 0)
		FROM arquivos_guardados
		WHERE estado = 'pronto'
		GROUP BY backend, origem
		ORDER BY backend, origem`)
	if err != nil {
		return nil, wrapErr("resumindo o acervo", err)
	}
	defer rows.Close()

	out := []UsoDoAcervo{}
	for rows.Next() {
		var u UsoDoAcervo
		if err := rows.Scan(&u.Backend, &u.Origem, &u.Arquivos, &u.Bytes); err != nil {
			return nil, wrapErr("resumindo o acervo", err)
		}
		out = append(out, u)
	}
	return out, wrapErr("resumindo o acervo", rows.Err())
}

// DevolverAFila põe uma cópia interrompida de volta como pendente.
//
// Serve ao desligamento do serviço: a cópia em curso é abortada, e marcá-la como ERRO seria
// mentir — não houve falha, houve um `systemctl restart`. Como pendente, ela recomeça
// sozinha na próxima subida.
func (s *Store) DevolverAFila(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE arquivos_guardados SET estado = 'pendente', bytes_baixados = 0
		 WHERE id = $1 AND estado = 'baixando'`, id)
	return wrapErr("devolvendo a cópia à fila", err)
}

// AnotarTamanhoTotal registra o tamanho que a fonte anunciou.
//
// Gravado antes de a cópia começar: é dele que a tela tira a porcentagem, e sem ele o
// progresso seria um número subindo sem fim à vista.
func (s *Store) AnotarTamanhoTotal(ctx context.Context, id, total int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE arquivos_guardados SET bytes_totais = $2 WHERE id = $1`, id, total)
	return wrapErr("anotando o tamanho total", err)
}

// BytesEmCache soma o que o CACHE ocupa num destino.
//
// Só `origem = 'fonte'`, e é essa a razão de a função existir em vez de reaproveitar o
// resumo: o acervo próprio não é descartável, então um limite que o incluísse ficaria
// estourado para sempre sem que a limpeza tivesse o que apagar — e o cache pararia de
// funcionar por causa de arquivos que ninguém jamais vai remover.
//
// Conta o que está `baixando` junto com o que está `pronto`. Ignorar as cópias em curso
// deixaria o limite ser furado por tudo que estivesse em voo no momento da conferência.
func (s *Store) BytesEmCache(ctx context.Context, backend string) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx, `
		SELECT coalesce(sum(CASE WHEN estado = 'pronto' THEN bytes ELSE bytes_baixados END), 0)
		FROM arquivos_guardados
		WHERE origem = 'fonte' AND backend = $1 AND estado IN ('pronto', 'baixando')`,
		backend).Scan(&total)
	return total, wrapErr("medindo o cache", err)
}

// ReivindicarParaCaptura tenta tomar uma cópia pendente para gravar durante a reprodução.
//
// Devolve false quando outro já a tem. É a trava que impede dois espectadores do mesmo
// filme, no mesmo minuto, de gravarem por cima um do outro — a troca de estado é condicional
// e atômica, então exatamente um ganha e os demais só entregam.
func (s *Store) ReivindicarParaCaptura(ctx context.Context, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE arquivos_guardados SET estado = 'baixando', bytes_baixados = 0, erro = ''
		 WHERE id = $1 AND estado = 'pendente'`, id)
	if err != nil {
		return false, wrapErr("reivindicando cópia para captura", err)
	}
	return tag.RowsAffected() == 1, nil
}
