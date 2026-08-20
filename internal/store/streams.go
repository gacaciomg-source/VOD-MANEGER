package store

import (
	"context"
	"encoding/json"
	"time"
)

// StreamTarget é o alvo de uma requisição de vídeo, já resolvido.
type StreamTarget struct {
	Kind      string // "content" | "episode"
	ID        int64
	ContentID int64
	EpisodeID *int64
	Title     string
	Extension string
}

// PlayableVariant é uma variante candidata a servir um stream.
//
// Traz junto tudo que a camada de transporte precisa para montar a URL de origem, para
// evitar uma segunda consulta no caminho crítico.
type PlayableVariant struct {
	ID           int64
	SourceID     int64
	SourceName   string
	SourceKind   string
	SourceBase   string
	Priority     int
	OriginURL    string
	StreamRef    json.RawMessage
	ContainerExt string
	MaxConns     int
}

// ResolveContentForStream localiza um filme e suas variantes jogáveis, em uma consulta.
//
// A ordem já vem pronta: override manual do administrador primeiro (principal,
// secundária, terciária), depois a prioridade da fonte. Variantes desabilitadas ou
// indisponíveis ficam de fora.
func (s *Store) ResolveContentForStream(ctx context.Context, contentID int64) (*StreamTarget, []PlayableVariant, error) {
	var alvo StreamTarget
	err := s.pool.QueryRow(ctx,
		`SELECT id, title FROM contents WHERE id = $1 AND status <> 'deleted'`, contentID).
		Scan(&alvo.ID, &alvo.Title)
	if err != nil {
		return nil, nil, wrapErr("buscando conteúdo para stream", err)
	}
	alvo.Kind = TargetContent
	alvo.ContentID = contentID

	variantes, err := s.playableVariants(ctx, TargetContent, contentID, contentID, 0)
	if err != nil {
		return nil, nil, err
	}
	if len(variantes) > 0 {
		alvo.Extension = variantes[0].ContainerExt
	}
	return &alvo, variantes, nil
}

// ResolveEpisodeForStream localiza um episódio e suas variantes jogáveis.
func (s *Store) ResolveEpisodeForStream(ctx context.Context, episodeID int64) (*StreamTarget, []PlayableVariant, error) {
	var alvo StreamTarget
	var serieID int64
	var serie string
	var temporada, numero int
	err := s.pool.QueryRow(ctx, `
		SELECT e.id, coalesce(nullif(e.title,''), c.title), c.id, c.title, se.season_number, e.episode_number
		FROM episodes e
		JOIN seasons se ON se.id = e.season_id
		JOIN contents c ON c.id = se.series_content_id
		WHERE e.id = $1 AND e.status <> 'deleted'`, episodeID).
		Scan(&alvo.ID, &alvo.Title, &serieID, &serie, &temporada, &numero)
	if err != nil {
		return nil, nil, wrapErr("buscando episódio para stream", err)
	}
	alvo.Kind = TargetEpisode
	alvo.ContentID = serieID
	alvo.EpisodeID = &episodeID

	variantes, err := s.playableVariants(ctx, TargetEpisode, episodeID, 0, episodeID)
	if err != nil {
		return nil, nil, err
	}
	if len(variantes) > 0 {
		alvo.Extension = variantes[0].ContainerExt
	}
	return &alvo, variantes, nil
}

// playableVariants é a consulta que ordena as origens.
//
// O CASE traduz os overrides manuais em posições: quem o administrador marcou como
// principal vem primeiro, e só depois entra a prioridade da fonte. É esta ordem que a
// tentativa de failover percorre.
func (s *Store) playableVariants(ctx context.Context, kind string, targetID, contentID, episodeID int64) ([]PlayableVariant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.id, v.source_id, s.name, s.kind, s.base_url, s.priority,
		       v.origin_url, v.stream_ref, v.container_ext, s.max_connections
		FROM source_variants v
		JOIN sources s ON s.id = v.source_id
		LEFT JOIN contents c ON c.id = $3::bigint
		LEFT JOIN episodes e ON e.id = $4::bigint
		WHERE v.target_kind = $1 AND v.target_id = $2
		  AND v.enabled AND v.available AND s.enabled
		ORDER BY
			CASE
				WHEN v.id IN (c.primary_variant_id,   e.primary_variant_id)   THEN 0
				WHEN v.id IN (c.secondary_variant_id, e.secondary_variant_id) THEN 1
				WHEN v.id IN (c.tertiary_variant_id,  e.tertiary_variant_id)  THEN 2
				ELSE 3
			END,
			s.priority, v.id`,
		kind, targetID, nullIfZero(contentID), nullIfZero(episodeID))
	if err != nil {
		return nil, wrapErr("listando variantes jogáveis", err)
	}
	defer rows.Close()

	out := []PlayableVariant{}
	for rows.Next() {
		var v PlayableVariant
		if err := rows.Scan(&v.ID, &v.SourceID, &v.SourceName, &v.SourceKind, &v.SourceBase,
			&v.Priority, &v.OriginURL, &v.StreamRef, &v.ContainerExt, &v.MaxConns); err != nil {
			return nil, wrapErr("listando variantes jogáveis", err)
		}
		out = append(out, v)
	}
	return out, wrapErr("listando variantes jogáveis", rows.Err())
}

func nullIfZero(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

// --- Sessões de reprodução ---------------------------------------------------

// NewStream são os campos de abertura de uma sessão.
type NewStream struct {
	NodeID       string
	ContentID    *int64
	EpisodeID    *int64
	VariantID    *int64
	SourceID     *int64
	CredentialID *int64
	ClientIP     string
	UserAgent    string
	RangeHeader  string
}

// OpenStream registra o início de uma reprodução.
func (s *Store) OpenStream(ctx context.Context, in NewStream) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO streams (node_id, content_id, episode_id, variant_id, source_id,
			credential_id, client_ip, user_agent, range_header)
		VALUES ($1,$2,$3,$4,$5,$6,coalesce($7,''),coalesce($8,''),coalesce($9,''))
		RETURNING id`,
		in.NodeID, in.ContentID, in.EpisodeID, in.VariantID, in.SourceID,
		in.CredentialID, in.ClientIP, in.UserAgent, in.RangeHeader).Scan(&id)
	return id, wrapErr("abrindo sessão de stream", err)
}

// CloseStream registra o fim de uma reprodução.
func (s *Store) CloseStream(ctx context.Context, id int64, bytes int64, ttfbMs int, status int, estado, errCode string, tentativas int, variantID *int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE streams SET
			state = $2, bytes_sent = $3, ttfb_ms = $4, status_code = $5,
			error_code = coalesce($6,''), attempts = $7,
			variant_id = coalesce($8, variant_id), ended_at = now()
		WHERE id = $1`,
		id, estado, bytes, nullIfZeroInt(ttfbMs), nullIfZeroInt(status), errCode, tentativas, variantID)
	return wrapErr("fechando sessão de stream", err)
}

func nullIfZeroInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

// ActiveStream é uma reprodução em andamento, para o monitoramento.
type ActiveStream struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	SourceName  string    `json:"source_name"`
	ClientIP    string    `json:"client_ip"`
	CacheResult string    `json:"cache_result"`
	BytesSent   int64     `json:"bytes_sent"`
	TTFBMs      *int      `json:"ttfb_ms"`
	StartedAt   time.Time `json:"started_at"`
}

// ListActiveStreams devolve as reproduções em andamento.
func (s *Store) ListActiveStreams(ctx context.Context, limit int) ([]ActiveStream, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT st.id,
		       coalesce(c.title, ct.title, '(desconhecido)'),
		       coalesce(s.name, ''), st.client_ip, st.cache_result,
		       st.bytes_sent, st.ttfb_ms, st.started_at
		FROM streams st
		LEFT JOIN contents c  ON c.id = st.content_id
		LEFT JOIN episodes e  ON e.id = st.episode_id
		LEFT JOIN seasons se  ON se.id = e.season_id
		LEFT JOIN contents ct ON ct.id = se.series_content_id
		LEFT JOIN sources s   ON s.id = st.source_id
		WHERE st.state = 'active'
		ORDER BY st.started_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, wrapErr("listando streams ativos", err)
	}
	defer rows.Close()

	out := []ActiveStream{}
	for rows.Next() {
		var a ActiveStream
		if err := rows.Scan(&a.ID, &a.Title, &a.SourceName, &a.ClientIP, &a.CacheResult,
			&a.BytesSent, &a.TTFBMs, &a.StartedAt); err != nil {
			return nil, wrapErr("listando streams ativos", err)
		}
		out = append(out, a)
	}
	return out, wrapErr("listando streams ativos", rows.Err())
}

// StreamStats resume a atividade de streaming.
type StreamStats struct {
	Active      int64 `json:"active"`
	Last24h     int64 `json:"last_24h"`
	Errors24h   int64 `json:"errors_24h"`
	BytesServed int64 `json:"bytes_served_24h"`
	AvgTTFBMs   *int  `json:"avg_ttfb_ms"`
}

// GetStreamStats devolve os números de streaming do painel.
func (s *Store) GetStreamStats(ctx context.Context) (*StreamStats, error) {
	var st StreamStats
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM streams WHERE state = 'active'),
			(SELECT count(*) FROM streams WHERE started_at > now() - interval '24 hours'),
			(SELECT count(*) FROM streams WHERE state = 'error' AND started_at > now() - interval '24 hours'),
			(SELECT coalesce(sum(bytes_sent), 0) FROM streams WHERE started_at > now() - interval '24 hours'),
			(SELECT round(avg(ttfb_ms))::int FROM streams
			 WHERE ttfb_ms IS NOT NULL AND started_at > now() - interval '24 hours')`).
		Scan(&st.Active, &st.Last24h, &st.Errors24h, &st.BytesServed, &st.AvgTTFBMs)
	if err != nil {
		return nil, wrapErr("consultando estatísticas de streaming", err)
	}
	return &st, nil
}

// ReleaseActiveStreams fecha sessões que ficaram abertas por queda do processo.
func (s *Store) ReleaseActiveStreams(ctx context.Context, nodeID string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE streams SET state = 'closed', ended_at = now(), error_code = 'processo_encerrado'
		WHERE state = 'active' AND node_id = $1`, nodeID)
	if err != nil {
		return 0, wrapErr("liberando streams ativos", err)
	}
	return tag.RowsAffected(), nil
}

// FalhaDeReproducao é uma tentativa que não entregou o vídeo.
//
// Traz o conteúdo e a FONTE juntos porque a pergunta do administrador nunca é "quantas
// falhas houve": é "o que não abriu, e de onde ele tentou puxar". Sem a fonte, uma falha
// não sugere ação nenhuma.
type FalhaDeReproducao struct {
	ID         int64      `json:"id"`
	ContentID  *int64     `json:"content_id"`
	EpisodeID  *int64     `json:"episode_id"`
	Titulo     string     `json:"titulo"`
	Tipo       string     `json:"tipo"`
	SourceID   *int64     `json:"source_id"`
	SourceName string     `json:"source_name"`
	VariantID  *int64     `json:"variant_id"`
	Credencial string     `json:"credencial"`
	ClientIP   string     `json:"client_ip"`
	ErrorCode  string     `json:"error_code"`
	StatusCode *int       `json:"status_code"`
	Tentativas int        `json:"tentativas"`
	BytesSent  int64      `json:"bytes_sent"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at"`
}

// ListFalhasDeReproducao devolve as reproduções que terminaram em erro.
//
// Exclui 'cliente_desconectou': fechar o player no meio é o comportamento mais comum de
// quem assiste, e listá-lo como falha afogaria os problemas reais.
func (s *Store) ListFalhasDeReproducao(ctx context.Context, limite int) ([]FalhaDeReproducao, error) {
	if limite <= 0 || limite > 500 {
		limite = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT st.id, st.content_id, st.episode_id,
		       coalesce(c.title, ct.title, '(conteúdo removido)'),
		       coalesce(c.type, 'series'),
		       st.source_id, coalesce(src.name, ''),
		       st.variant_id, coalesce(cred.name, ''), st.client_ip,
		       st.error_code, st.status_code, st.attempts, st.bytes_sent,
		       st.started_at, st.ended_at
		FROM streams st
		LEFT JOIN contents c   ON c.id = st.content_id
		LEFT JOIN episodes e   ON e.id = st.episode_id
		LEFT JOIN seasons se   ON se.id = e.season_id
		LEFT JOIN contents ct  ON ct.id = se.series_content_id
		LEFT JOIN sources src  ON src.id = st.source_id
		LEFT JOIN stream_credentials cred ON cred.id = st.credential_id
		WHERE st.state = 'error'
		   OR (st.error_code <> '' AND st.error_code <> 'cliente_desconectou')
		ORDER BY st.started_at DESC
		LIMIT $1`, limite)
	if err != nil {
		return nil, wrapErr("listando falhas de reprodução", err)
	}
	defer rows.Close()

	out := []FalhaDeReproducao{}
	for rows.Next() {
		var f FalhaDeReproducao
		if err := rows.Scan(&f.ID, &f.ContentID, &f.EpisodeID, &f.Titulo, &f.Tipo,
			&f.SourceID, &f.SourceName, &f.VariantID, &f.Credencial, &f.ClientIP,
			&f.ErrorCode, &f.StatusCode, &f.Tentativas, &f.BytesSent,
			&f.StartedAt, &f.EndedAt); err != nil {
			return nil, wrapErr("listando falhas de reprodução", err)
		}
		out = append(out, f)
	}
	return out, wrapErr("listando falhas de reprodução", rows.Err())
}

// TrafegoPorPeriodo resume o volume entregue.
type TrafegoPorPeriodo struct {
	Periodo     string `json:"periodo"`
	Bytes       int64  `json:"bytes"`
	Reproducoes int64  `json:"reproducoes"`
	Falhas      int64  `json:"falhas"`
}

// Trafego devolve o volume entregue em janelas de tempo.
//
// Enquanto a entrega é direta da fonte, cada byte entregue foi um byte RECEBIDO da fonte:
// o tráfego total da máquina é o dobro do que aparece aqui. Quando o cache existir, os
// dois números passam a divergir, e aí valerá medi-los em separado.
func (s *Store) Trafego(ctx context.Context) ([]TrafegoPorPeriodo, error) {
	janelas := []struct {
		nome      string
		intervalo string
	}{
		{"última hora", "1 hour"},
		{"últimas 24 horas", "24 hours"},
		{"últimos 7 dias", "7 days"},
		{"últimos 30 dias", "30 days"},
	}

	out := make([]TrafegoPorPeriodo, 0, len(janelas))
	for _, j := range janelas {
		var t TrafegoPorPeriodo
		t.Periodo = j.nome
		err := s.pool.QueryRow(ctx, `
			SELECT coalesce(sum(bytes_sent), 0), count(*),
			       count(*) FILTER (WHERE state = 'error')
			FROM streams
			WHERE started_at > now() - $1::interval`, j.intervalo).
			Scan(&t.Bytes, &t.Reproducoes, &t.Falhas)
		if err != nil {
			return nil, wrapErr("resumindo tráfego", err)
		}
		out = append(out, t)
	}
	return out, nil
}

// ReleaseStaleStreams fecha sessões que ficaram marcadas como ativas tempo demais.
//
// É a rede de segurança do fechamento: se um caminho novo escapar sem fechar a sessão, ou
// o processo morrer no meio de uma transmissão, a linha ficaria 'ativa' para sempre — e o
// painel mostraria reproduções que já não existem, além de contar contra o limite de
// conexões da credencial.
//
// O prazo precisa ser generoso: um filme longo é uma sessão legitimamente aberta por
// horas. Só o que passa MUITO disso é fantasma.
func (s *Store) ReleaseStaleStreams(ctx context.Context, nodeID string, maisVelhoQue time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE streams
		SET state = 'error', error_code = 'sessao_abandonada', ended_at = now()
		WHERE state = 'active'
		  AND node_id = $1
		  AND started_at < now() - $2::interval`,
		nodeID, maisVelhoQue.String())
	if err != nil {
		return 0, wrapErr("liberando sessões abandonadas", err)
	}
	return tag.RowsAffected(), nil
}
