package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Contas de nuvem: onde o acervo pode morar, além do disco desta máquina.
//
// Várias por instalação, cadastradas e removidas pelo painel. Espaço em nuvem se compra por
// conta, não por terabyte — quem cresce, cresce somando contas. E conta de nuvem é o
// recurso mais frágil desta lista: ela é suspensa, ela enche, ela perde o token. Com
// várias, isso vira "esta parou, as outras seguem".

// Provedores de nuvem suportados.
const ProvedorGDrive = "gdrive"

// Nuvem é uma conta de armazenamento.
//
// As credenciais NÃO estão aqui. Elas saem do banco só quando alguém vai de fato falar com
// o provedor, por um método próprio — assim nenhuma listagem de tela pode vazá-las por
// esquecimento de uma anotação `json:"-"`.
type Nuvem struct {
	ID             int64      `json:"id"`
	Nome           string     `json:"nome"`
	Provedor       string     `json:"provedor"`
	PastaRaiz      string     `json:"pasta_raiz"`
	Ativa          bool       `json:"ativa"`
	SomenteLeitura bool       `json:"somente_leitura"`
	Ordem          int        `json:"ordem"`
	BytesUsados    *int64     `json:"bytes_usados"`
	BytesTotais    *int64     `json:"bytes_totais"`
	MedidaEm       *time.Time `json:"medida_em"`
	UltimoErro     string     `json:"ultimo_erro"`
	UltimoErroEm   *time.Time `json:"ultimo_erro_em"`
	CriadoEm       time.Time  `json:"criado_em"`
	// Arquivos e BytesGuardados vêm do acervo, não da conta. Respondem à pergunta de quem
	// está prestes a remover uma conta: o que exatamente eu perco?
	Arquivos       int64 `json:"arquivos"`
	BytesGuardados int64 `json:"bytes_guardados"`
}

// PodeReceber informa se esta conta aceita gravações agora.
//
// Uma conta cheia continua servindo o que já tem: `somente_leitura` para de gravar sem
// parar de entregar. Desativá-la seria a única alternativa, e derrubaria de uma vez todo o
// acervo que está lá dentro.
func (n *Nuvem) PodeReceber() bool { return n.Ativa && !n.SomenteLeitura }

// camposNuvem é a ordem em que as colunas são lidas. Uma lista só, para que a leitura e as
// consultas não possam divergir.
var camposNuvem = []string{
	"id", "nome", "provedor", "pasta_raiz", "ativa", "somente_leitura",
	"ordem", "bytes_usados", "bytes_totais", "medida_em", "ultimo_erro", "ultimo_erro_em",
	"criado_em",
}

// colunasNuvem serve ao RETURNING de INSERT e UPDATE, que NÃO têm apelido de tabela.
//
// Esta separação existe por um defeito real: a lista era uma constante única já qualificada
// com `n.`, o que funciona no SELECT com junção e explode no RETURNING — "missing FROM-clause
// entry for table n". O cadastro de conta de nuvem falhava DEPOIS de o Google já ter
// autorizado, que é o pior momento possível: o consentimento se gasta e é preciso refazer
// tudo.
var colunasNuvem = strings.Join(camposNuvem, ", ")

// colunasNuvemDe qualifica as colunas com o apelido, para as consultas com junção.
func colunasNuvemDe(apelido string) string {
	qualificadas := make([]string, len(camposNuvem))
	for i, campo := range camposNuvem {
		qualificadas[i] = apelido + "." + campo
	}
	return strings.Join(qualificadas, ", ")
}

func lerNuvem(linha pgx.Row, comAcervo bool) (*Nuvem, error) {
	var n Nuvem
	alvos := []any{&n.ID, &n.Nome, &n.Provedor, &n.PastaRaiz, &n.Ativa, &n.SomenteLeitura,
		&n.Ordem, &n.BytesUsados, &n.BytesTotais, &n.MedidaEm, &n.UltimoErro,
		&n.UltimoErroEm, &n.CriadoEm}
	if comAcervo {
		alvos = append(alvos, &n.Arquivos, &n.BytesGuardados)
	}
	err := linha.Scan(alvos...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &n, err
}

// ListarNuvens devolve as contas com o que cada uma guarda.
func (s *Store) ListarNuvens(ctx context.Context) ([]Nuvem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+colunasNuvemDe("n")+`,
		       coalesce(a.arquivos, 0), coalesce(a.bytes, 0)
		FROM nuvens n
		LEFT JOIN (
			SELECT nuvem_id, count(*) AS arquivos, sum(bytes) AS bytes
			FROM arquivos_guardados
			WHERE nuvem_id IS NOT NULL AND estado = 'pronto'
			GROUP BY nuvem_id
		) a ON a.nuvem_id = n.id
		ORDER BY n.ordem, n.id`)
	if err != nil {
		return nil, wrapErr("listando contas de nuvem", err)
	}
	defer rows.Close()

	out := []Nuvem{}
	for rows.Next() {
		n, err := lerNuvem(rows, true)
		if err != nil {
			return nil, wrapErr("listando contas de nuvem", err)
		}
		out = append(out, *n)
	}
	return out, wrapErr("listando contas de nuvem", rows.Err())
}

// NuvemPorID devolve uma conta.
func (s *Store) NuvemPorID(ctx context.Context, id int64) (*Nuvem, error) {
	n, err := lerNuvem(s.pool.QueryRow(ctx,
		`SELECT `+colunasNuvemDe("n")+` FROM nuvens n WHERE n.id = $1`, id), false)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, wrapErr("buscando conta de nuvem", err)
	}
	return n, nil
}

// NuvemParaGravar escolhe a conta que recebe o próximo arquivo.
//
// A primeira ativa, gravável e com espaço, na ordem definida pelo administrador.
//
// Previsível de propósito. "A que tem mais espaço" espalharia o acervo por todas as contas,
// e aí perder uma conta significaria perder um pedaço de tudo — em vez de perder as coisas
// mais antigas, que é um estrago que dá para entender e recuperar.
//
// Contas sem cota medida entram: não saber quanto cabe não é o mesmo que estar cheia, e
// recusar por falta de medição deixaria uma conta nova inútil até a primeira medição.
func (s *Store) NuvemParaGravar(ctx context.Context, bytesNecessarios int64) (*Nuvem, error) {
	n, err := lerNuvem(s.pool.QueryRow(ctx, `
		SELECT `+colunasNuvemDe("n")+`
		FROM nuvens n
		WHERE n.ativa AND NOT n.somente_leitura
		  AND (n.bytes_totais IS NULL OR n.bytes_usados IS NULL
		       OR n.bytes_totais - n.bytes_usados >= $1)
		ORDER BY n.ordem, n.id
		LIMIT 1`, bytesNecessarios), false)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, wrapErr("escolhendo conta de nuvem", err)
	}
	return n, nil
}

// NovaNuvem é o cadastro de uma conta.
type NovaNuvem struct {
	Nome        string
	Provedor    string
	PastaRaiz   string
	Ordem       int
	Credenciais []byte // já cifradas
}

// CriarNuvem cadastra uma conta.
func (s *Store) CriarNuvem(ctx context.Context, nova NovaNuvem) (*Nuvem, error) {
	if nova.Ordem == 0 {
		nova.Ordem = 100
	}
	n, err := lerNuvem(s.pool.QueryRow(ctx, `
		INSERT INTO nuvens (nome, provedor, pasta_raiz, ordem, credenciais_enc)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING `+colunasNuvem, nova.Nome, nova.Provedor, nova.PastaRaiz,
		nova.Ordem, nova.Credenciais), false)
	if err != nil {
		return nil, wrapErr("cadastrando conta de nuvem", err)
	}
	return n, nil
}

// CredenciaisDaNuvem devolve o blob cifrado.
//
// Método separado, e nunca parte de Nuvem: uma listagem de tela não pode vazar credencial
// por esquecimento de uma anotação. Quem precisa delas pede, e fica evidente no código
// quem pediu.
func (s *Store) CredenciaisDaNuvem(ctx context.Context, id int64) ([]byte, error) {
	var blob []byte
	err := s.pool.QueryRow(ctx,
		`SELECT credenciais_enc FROM nuvens WHERE id = $1`, id).Scan(&blob)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return blob, wrapErr("lendo credenciais da conta de nuvem", err)
}

// AtualizarCredenciaisDaNuvem grava um token renovado.
//
// O token de acesso expira e é renovado sozinho durante o uso. Sem esta gravação, cada
// reinício do serviço começaria a partir de um token vencido.
func (s *Store) AtualizarCredenciaisDaNuvem(ctx context.Context, id int64, credenciais []byte) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE nuvens SET credenciais_enc = $2 WHERE id = $1`, id, credenciais)
	if err != nil {
		return wrapErr("atualizando credenciais da conta de nuvem", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("atualizando credenciais da conta de nuvem", ErrNotFound)
	}
	return nil
}

// AjusteDeNuvem são as alterações que o painel permite. Campos nulos ficam como estão.
type AjusteDeNuvem struct {
	Nome           *string
	PastaRaiz      *string
	Ativa          *bool
	SomenteLeitura *bool
	Ordem          *int
}

// AtualizarNuvem aplica as alterações do painel.
func (s *Store) AtualizarNuvem(ctx context.Context, id int64, aj AjusteDeNuvem) (*Nuvem, error) {
	n, err := lerNuvem(s.pool.QueryRow(ctx, `
		UPDATE nuvens SET
			nome            = coalesce($2, nome),
			pasta_raiz      = coalesce($3, pasta_raiz),
			ativa           = coalesce($4, ativa),
			somente_leitura = coalesce($5, somente_leitura),
			ordem           = coalesce($6, ordem)
		WHERE id = $1
		RETURNING `+colunasNuvem,
		id, aj.Nome, aj.PastaRaiz, aj.Ativa, aj.SomenteLeitura, aj.Ordem), false)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, wrapErr("atualizando conta de nuvem", err)
	}
	return n, nil
}

// AnotarCotaDaNuvem guarda a última medição de espaço.
func (s *Store) AnotarCotaDaNuvem(ctx context.Context, id, usados, totais int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE nuvens SET bytes_usados = $2, bytes_totais = $3, medida_em = now(),
		                  ultimo_erro = '', ultimo_erro_em = NULL
		WHERE id = $1`, id, usados, totais)
	return wrapErr("anotando cota da conta de nuvem", err)
}

// AnotarErroDaNuvem registra a última falha.
//
// Uma conta que perdeu o token falha em toda gravação, e sem este registro a única pista
// seria o log do serviço — que ninguém lê antes de o acervo já ter parado.
func (s *Store) AnotarErroDaNuvem(ctx context.Context, id int64, motivo string) error {
	const limite = 500
	if len(motivo) > limite {
		motivo = motivo[:limite]
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE nuvens SET ultimo_erro = $2, ultimo_erro_em = now() WHERE id = $1`, id, motivo)
	return wrapErr("anotando erro da conta de nuvem", err)
}

// RemoverNuvem apaga a conta.
//
// Recusa enquanto houver arquivo guardado nela — a chave estrangeira é RESTRICT, e este
// erro traduz a recusa do banco numa frase que diz o que fazer. Remover uma conta com
// acervo dentro deixaria linhas apontando para lugar nenhum, e o painel entregaria links de
// vídeos que não existem mais.
func (s *Store) RemoverNuvem(ctx context.Context, id int64) error {
	var quantos int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM arquivos_guardados WHERE nuvem_id = $1`, id).Scan(&quantos); err != nil {
		return wrapErr("removendo conta de nuvem", err)
	}
	if quantos > 0 {
		return wrapErr("removendo conta de nuvem", ErrConflict)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM nuvens WHERE id = $1`, id)
	if err != nil {
		return wrapErr("removendo conta de nuvem", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("removendo conta de nuvem", ErrNotFound)
	}
	return nil
}

// ArquivosDaNuvem lista o que está guardado numa conta.
//
// É o que a tela mostra antes de remover uma conta: "estes 340 arquivos vão sumir". Sem
// isso, a recusa da remoção seria um "não pode" sem resposta para "então o que eu faço".
func (s *Store) ArquivosDaNuvem(ctx context.Context, id int64, limite int) ([]ArquivoGuardado, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+colunasArquivo+` FROM arquivos_guardados
		 WHERE nuvem_id = $1 ORDER BY bytes DESC LIMIT $2`, id, limite)
	if err != nil {
		return nil, wrapErr("listando arquivos da conta de nuvem", err)
	}
	defer rows.Close()

	out := []ArquivoGuardado{}
	for rows.Next() {
		a, err := lerArquivo(rows)
		if err != nil {
			return nil, wrapErr("listando arquivos da conta de nuvem", err)
		}
		out = append(out, *a)
	}
	return out, wrapErr("listando arquivos da conta de nuvem", rows.Err())
}
