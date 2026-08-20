package sync

import (
	"context"

	"vodmanager/internal/ingest"
	"vodmanager/internal/store"
)

// escritorEmLote acumula as escritas de variantes e as descarrega de uma vez.
//
// # O problema que resolve
//
// Cada item novo custava duas idas ao banco, uma esperando a outra. Numa carga inicial de
// 290 mil variantes, isso são quase 600 mil viagens sequenciais — e o tempo era quase todo
// espera, com CPU e banco ociosos.
//
// # A armadilha que ele precisa evitar
//
// O índice em memória é o que impede a mesma linha da lista de virar duas variantes. Ele
// era atualizado item a item, logo após a inserção. Com escrita adiada, os itens entre o
// acúmulo e a descarga ficariam invisíveis ao índice — e uma lista com a mesma entrada
// repetida criaria duplicatas.
//
// Por isso o escritor mantém um registro dos itens PENDENTES e o consulta junto do índice.
// Uma entrada repetida dentro do mesmo lote é reconhecida e descartada, exatamente como
// seria se a escrita fosse imediata.
type escritorEmLote struct {
	store *store.Store
	idx   *indices
	// tamanho é quantos itens acumulamos antes de descarregar.
	tamanho int

	novas       []store.VarianteParaCriar
	atualizadas []store.VarianteParaAtualizar
	// pendentes marca as chaves já reservadas por este lote, mas ainda não gravadas.
	pendentes map[string]bool
}

func novoEscritorEmLote(st *store.Store, idx *indices, tamanho int) *escritorEmLote {
	if tamanho <= 0 {
		tamanho = 500
	}
	return &escritorEmLote{
		store:       st,
		idx:         idx,
		tamanho:     tamanho,
		novas:       make([]store.VarianteParaCriar, 0, tamanho),
		atualizadas: make([]store.VarianteParaAtualizar, 0, tamanho),
		pendentes:   make(map[string]bool, tamanho),
	}
}

// Reservada informa se esta chave já foi acumulada neste lote e ainda não foi gravada.
//
// É a proteção contra a mesma entrada aparecer duas vezes na lista da fonte: sem ela, a
// segunda ocorrência não encontraria nada no índice e criaria uma variante duplicada.
func (e *escritorEmLote) Reservada(chave string) bool {
	return e.pendentes[chave]
}

// Criar acumula uma variante nova.
func (e *escritorEmLote) Criar(ctx context.Context, item store.VarianteParaCriar) error {
	e.novas = append(e.novas, item)
	e.pendentes[item.Chave] = true
	if len(e.novas) >= e.tamanho {
		return e.DescarregarCriacoes(ctx)
	}
	return nil
}

// Atualizar acumula uma variante existente que mudou.
func (e *escritorEmLote) Atualizar(ctx context.Context, item store.VarianteParaAtualizar) error {
	e.atualizadas = append(e.atualizadas, item)
	if len(e.atualizadas) >= e.tamanho {
		return e.DescarregarAtualizacoes(ctx)
	}
	return nil
}

// DescarregarCriacoes grava as variantes novas e alimenta o índice com os ids reais.
func (e *escritorEmLote) DescarregarCriacoes(ctx context.Context) error {
	if len(e.novas) == 0 {
		return nil
	}
	criadas, err := e.store.CriarVariantesEmLote(ctx, e.novas)
	if err != nil {
		return err
	}
	// Só agora o índice passa a conhecer os ids. Até aqui, quem protegia contra
	// duplicatas era o registro de pendentes.
	for _, c := range criadas {
		e.idx.variantes[c.Chave] = store.VariantRef{
			ID: c.ID, TargetKind: c.Alvo, TargetID: c.AlvoID, Digest: c.Digest,
		}
	}
	e.novas = e.novas[:0]
	clear(e.pendentes)
	return nil
}

// DescarregarAtualizacoes grava as variantes alteradas.
func (e *escritorEmLote) DescarregarAtualizacoes(ctx context.Context) error {
	if len(e.atualizadas) == 0 {
		return nil
	}
	if err := e.store.AtualizarVariantesEmLote(ctx, e.atualizadas); err != nil {
		return err
	}
	e.atualizadas = e.atualizadas[:0]
	return nil
}

// Descarregar grava tudo o que restou. Precisa ser chamado ao fim da sincronização, e
// antes de qualquer leitura que dependa do que foi acumulado.
func (e *escritorEmLote) Descarregar(ctx context.Context) error {
	if err := e.DescarregarCriacoes(ctx); err != nil {
		return err
	}
	return e.DescarregarAtualizacoes(ctx)
}

// Acumulados informa quantos itens estão esperando escrita.
func (e *escritorEmLote) Acumulados() int {
	return len(e.novas) + len(e.atualizadas)
}

// decisaoParaBanco traduz o veredito do matching para o que o schema aceita.
//
// Existe porque dois casos produziam valores que o banco recusa, e a recusa era engolida:
// o erro só era registrado no log, então ninguém percebeu que a decisão não estava sendo
// gravada. Com a escrita em lote isso deixou de ser aceitável — um item inválido derruba
// os outros quinhentos do mesmo lote.
//
//   - Episódio: o alvo vem da ESTRUTURA da série (temporada e número), não de um cálculo
//     de semelhança. O agrupamento aconteceu de fato; ele só não passou pelo score. Vai
//     como 'grouped' com confiança máxima, que é a descrição honesta do que ocorreu.
//   - 'locked': existe como constante no código e não está entre os valores do schema.
//     Um agrupamento travado continua sendo um agrupamento — vai como 'grouped', com a
//     trava registrada no campo próprio.
func decisaoParaBanco(r ingest.MatchResult) store.DecisaoDeMatch {
	d := store.DecisaoDeMatch{
		Actor:      "auto",
		Decision:   string(r.Decision),
		Confidence: r.Score,
		Signals:    r.Signals,
	}
	switch r.Decision {
	case ingest.DecisionGrouped, ingest.DecisionPendingReview, ingest.DecisionRejected:
		// Valores que o schema conhece.
	case ingest.DecisionLocked:
		d.Decision = string(ingest.DecisionGrouped)
		d.Locked = true
	default:
		// Inclui o vazio, que é o caso do episódio.
		d.Decision = string(ingest.DecisionGrouped)
		if d.Confidence == 0 {
			d.Confidence = 100
		}
		d.Note = "agrupado pela estrutura da série"
	}
	return d
}
