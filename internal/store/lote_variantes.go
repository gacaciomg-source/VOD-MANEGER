package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Escrita em LOTE das variantes durante a sincronização.
//
// # Por que existe
//
// Cada item novo custava duas idas ao banco, uma esperando a outra: inserir a variante e
// gravar a decisão de matching. Numa carga inicial de 290 mil variantes isso são quase 600
// mil viagens de ida e volta, sequenciais. Medido no acervo real, o resultado eram ~720
// itens por segundo e cerca de sete minutos — com CPU e banco praticamente ociosos, porque
// o tempo era quase todo ESPERA.
//
// O lote junta centenas de comandos numa viagem só. O trabalho do banco é o mesmo; o que
// desaparece é a espera entre um comando e o próximo.
//
// # O que NÃO muda
//
// O resultado. As mesmas linhas, com os mesmos valores e a mesma ordem de identificadores.
// Há teste comparando o catálogo produzido em lote com o produzido item a item.

// tamanhoLotePadrao é quantos itens acumulamos antes de escrever.
//
// Quinhentos é onde o ganho por viagem já saturou: dobrar para mil melhora pouco e dobra a
// memória segurada, além de aumentar o que se perde caso a sincronização seja cancelada
// no meio de um lote.
const tamanhoLotePadrao = 500

// VarianteParaCriar é um item novo esperando para ser gravado.
type VarianteParaCriar struct {
	Variante NewVariant
	// Chave é como o índice em memória vai encontrar esta variante depois.
	Chave string
	// Decisao é gravada junto, na mesma viagem.
	Decisao DecisaoDeMatch
}

// DecisaoDeMatch é o registro de como o alvo foi escolhido.
type DecisaoDeMatch struct {
	Actor      string
	Decision   string
	Confidence int
	Signals    any
	Locked     bool
	Note       string
}

// VarianteCriada devolve o que o chamador precisa para atualizar o índice.
type VarianteCriada struct {
	ID     int64
	Chave  string
	Alvo   string
	AlvoID int64
	Digest string
}

// CriarVariantesEmLote insere as variantes e as decisões numa única viagem ao banco.
//
// Devolve os identificadores NA ORDEM da entrada — é o que permite ao chamador manter o
// índice em memória coerente sem uma segunda consulta.
func (s *Store) CriarVariantesEmLote(ctx context.Context, itens []VarianteParaCriar) ([]VarianteCriada, error) {
	if len(itens) == 0 {
		return nil, nil
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, wrapErr("criando variantes em lote", err)
	}
	defer conn.Release()

	// Uma transação por lote: ou o lote inteiro entra, ou nenhum item dele entra. Meio
	// lote gravado deixaria o índice em memória mentindo sobre o que existe no banco.
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, wrapErr("criando variantes em lote", err)
	}
	defer tx.Rollback(ctx)

	lote := &pgx.Batch{}
	for i := range itens {
		in := itens[i].Variante
		if len(in.RawPayload) == 0 {
			in.RawPayload = json.RawMessage("{}")
		}
		lote.Queue(`
			INSERT INTO source_variants (source_id, target_kind, target_id, external_id, url_hash,
				origin_url, stream_ref, container_ext, declared_title, declared_group,
				quality_tags, language_tags, digest, raw_payload)
			VALUES ($1,$2,$3,coalesce($4,''),coalesce($5,''),coalesce($6,''),$7,coalesce($8,''),
			        coalesce($9,''),coalesce($10,''),coalesce($11::text[],'{}'),coalesce($12::text[],'{}'),
			        coalesce($13,''),$14)
			RETURNING id`,
			in.SourceID, in.TargetKind, in.TargetID, in.ExternalID, in.URLHash,
			in.OriginURL, in.StreamRef, in.ContainerExt, in.DeclaredTitle, in.DeclaredGroup,
			in.QualityTags, in.LanguageTags, in.Digest, in.RawPayload)
	}

	res := tx.SendBatch(ctx, lote)
	criadas := make([]VarianteCriada, 0, len(itens))
	for i := range itens {
		var id int64
		if err := res.QueryRow().Scan(&id); err != nil {
			res.Close()
			return nil, wrapErr(fmt.Sprintf("criando variante %d do lote", i), err)
		}
		criadas = append(criadas, VarianteCriada{
			ID:     id,
			Chave:  itens[i].Chave,
			Alvo:   itens[i].Variante.TargetKind,
			AlvoID: itens[i].Variante.TargetID,
			Digest: itens[i].Variante.Digest,
		})
	}
	if err := res.Close(); err != nil {
		return nil, wrapErr("criando variantes em lote", err)
	}

	// As decisões vão numa segunda viagem porque dependem dos ids que a primeira
	// devolveu. Duas viagens por lote de 500 continua sendo 250 vezes menos que antes.
	decisoes := &pgx.Batch{}
	for i := range itens {
		d := itens[i].Decisao
		payload, err := json.Marshal(d.Signals)
		if err != nil {
			payload = []byte("{}")
		}
		decisoes.Queue(`
			INSERT INTO match_decisions (variant_id, target_kind, target_id, actor, decision, confidence, signals, locked, note)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,coalesce($9,''))
			ON CONFLICT (variant_id) DO UPDATE SET
				target_kind = excluded.target_kind,
				target_id   = excluded.target_id,
				actor       = excluded.actor,
				decision    = excluded.decision,
				confidence  = excluded.confidence,
				signals     = excluded.signals,
				locked      = excluded.locked,
				note        = excluded.note`,
			criadas[i].ID, itens[i].Variante.TargetKind, itens[i].Variante.TargetID,
			d.Actor, d.Decision, d.Confidence, payload, d.Locked, d.Note)
	}
	resD := tx.SendBatch(ctx, decisoes)
	for range itens {
		if _, err := resD.Exec(); err != nil {
			resD.Close()
			return nil, wrapErr("gravando decisões de matching em lote", err)
		}
	}
	if err := resD.Close(); err != nil {
		return nil, wrapErr("gravando decisões de matching em lote", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, wrapErr("confirmando lote de variantes", err)
	}
	return criadas, nil
}

// VarianteParaAtualizar é um item já conhecido cujo conteúdo mudou.
type VarianteParaAtualizar struct {
	ID       int64
	Variante NewVariant
}

// AtualizarVariantesEmLote aplica as atualizações numa única viagem.
func (s *Store) AtualizarVariantesEmLote(ctx context.Context, itens []VarianteParaAtualizar) error {
	if len(itens) == 0 {
		return nil
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return wrapErr("atualizando variantes em lote", err)
	}
	defer conn.Release()

	lote := &pgx.Batch{}
	for i := range itens {
		in := itens[i].Variante
		if len(in.RawPayload) == 0 {
			in.RawPayload = json.RawMessage("{}")
		}
		lote.Queue(`
			UPDATE source_variants SET
				origin_url     = coalesce($2,''),
				stream_ref     = $3,
				container_ext  = coalesce($4,''),
				declared_title = coalesce($5,''),
				declared_group = coalesce($6,''),
				quality_tags   = coalesce($7::text[],'{}'),
				language_tags  = coalesce($8::text[],'{}'),
				digest         = coalesce($9,''),
				raw_payload    = $10,
				url_hash       = coalesce($11,''),
				available      = true,
				missing_since  = NULL,
				missing_count  = 0,
				last_seen_at   = now(),
				updated_at     = now()
			WHERE id = $1`,
			itens[i].ID, in.OriginURL, in.StreamRef, in.ContainerExt, in.DeclaredTitle,
			in.DeclaredGroup, in.QualityTags, in.LanguageTags, in.Digest, in.RawPayload,
			in.URLHash)
	}

	res := conn.SendBatch(ctx, lote)
	for i := range itens {
		if _, err := res.Exec(); err != nil {
			res.Close()
			return wrapErr(fmt.Sprintf("atualizando variante %d do lote", i), err)
		}
	}
	return wrapErr("atualizando variantes em lote", res.Close())
}
