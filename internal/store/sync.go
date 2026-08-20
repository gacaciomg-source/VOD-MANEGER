package store

import (
	"context"
	"encoding/json"
	"time"
)

// SyncRun é uma execução de sincronização.
type SyncRun struct {
	ID             int64           `json:"id"`
	SourceID       int64           `json:"source_id"`
	SourceName     string          `json:"source_name,omitempty"`
	NodeID         string          `json:"node_id"`
	Trigger        string          `json:"trigger"`
	State          string          `json:"state"`
	StartedAt      time.Time       `json:"started_at"`
	FinishedAt     *time.Time      `json:"finished_at"`
	ItemsSeen      int             `json:"items_seen"`
	ItemsNew       int             `json:"items_new"`
	ItemsUpdated   int             `json:"items_updated"`
	ItemsUnchanged int             `json:"items_unchanged"`
	ItemsMissing   int             `json:"items_missing"`
	ItemsRejected  int             `json:"items_rejected"`
	RequestsMade   int             `json:"requests_made"`
	RequestBudget  int             `json:"request_budget"`
	ErrorMessage   string          `json:"error_message"`
	Stats          json.RawMessage `json:"stats"`
}

const syncRunColumns = `id, source_id, node_id, trigger, state, started_at, finished_at,
	items_seen, items_new, items_updated, items_unchanged, items_missing, items_rejected,
	requests_made, request_budget, error_message, stats`

func scanSyncRun(row rowScanner) (*SyncRun, error) {
	var r SyncRun
	if err := row.Scan(&r.ID, &r.SourceID, &r.NodeID, &r.Trigger, &r.State, &r.StartedAt, &r.FinishedAt,
		&r.ItemsSeen, &r.ItemsNew, &r.ItemsUpdated, &r.ItemsUnchanged, &r.ItemsMissing, &r.ItemsRejected,
		&r.RequestsMade, &r.RequestBudget, &r.ErrorMessage, &r.Stats); err != nil {
		return nil, err
	}
	return &r, nil
}

// StartSyncRun abre uma execução.
//
// O índice único parcial do schema garante uma única run ativa por fonte: duas
// simultâneas dobrariam as conexões à fonte e produziriam diffs conflitantes. Uma
// segunda tentativa recebe ErrConflict.
func (s *Store) StartSyncRun(ctx context.Context, sourceID int64, nodeID, trigger string, budget int) (*SyncRun, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO sync_runs (source_id, node_id, trigger, request_budget)
		VALUES ($1, $2, $3, $4)
		RETURNING `+syncRunColumns,
		sourceID, nodeID, trigger, budget)
	r, err := scanSyncRun(row)
	return r, wrapErr("abrindo execução de sincronização", err)
}

// SyncCounters são os contadores finais de uma execução.
type SyncCounters struct {
	Seen      int
	New       int
	Updated   int
	Unchanged int
	Missing   int
	Rejected  int
	Requests  int
	Stats     map[string]any
}

// FinishSyncRun fecha uma execução com o estado final.
func (s *Store) FinishSyncRun(ctx context.Context, runID int64, state string, counters SyncCounters, errMsg string) error {
	stats, err := json.Marshal(counters.Stats)
	if err != nil || counters.Stats == nil {
		stats = []byte("{}")
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE sync_runs SET
			state = $2, finished_at = now(),
			items_seen = $3, items_new = $4, items_updated = $5, items_unchanged = $6,
			items_missing = $7, items_rejected = $8, requests_made = $9,
			error_message = coalesce($10,''), stats = $11
		WHERE id = $1`,
		runID, state, counters.Seen, counters.New, counters.Updated, counters.Unchanged,
		counters.Missing, counters.Rejected, counters.Requests, errMsg, stats)
	return wrapErr("fechando execução de sincronização", err)
}

// UpdateSyncProgress atualiza os contadores de uma execução AINDA EM ANDAMENTO.
//
// É o que permite ao painel mostrar progresso ao vivo. A escrita é deliberadamente
// esparsa (a cada lote, não a cada item): atualizar por item transformaria uma
// sincronização de 50 mil itens em 50 mil UPDATEs.
func (s *Store) UpdateSyncProgress(ctx context.Context, runID int64, c SyncCounters) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sync_runs SET
			items_seen = $2, items_new = $3, items_updated = $4,
			items_unchanged = $5, items_rejected = $6, requests_made = $7
		WHERE id = $1 AND state = 'running'`,
		runID, c.Seen, c.New, c.Updated, c.Unchanged, c.Rejected, c.Requests)
	return wrapErr("atualizando progresso", err)
}

// ListSyncRuns devolve as execuções mais recentes.
func (s *Store) ListSyncRuns(ctx context.Context, sourceID *int64, limit int) ([]SyncRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.source_id, r.node_id, r.trigger, r.state, r.started_at, r.finished_at,
		       r.items_seen, r.items_new, r.items_updated, r.items_unchanged, r.items_missing,
		       r.items_rejected, r.requests_made, r.request_budget, r.error_message, r.stats,
		       s.name
		FROM sync_runs r
		JOIN sources s ON s.id = r.source_id
		WHERE ($1::bigint IS NULL OR r.source_id = $1)
		ORDER BY r.started_at DESC
		LIMIT $2`, sourceID, limit)
	if err != nil {
		return nil, wrapErr("listando execuções", err)
	}
	defer rows.Close()

	out := []SyncRun{}
	for rows.Next() {
		var r SyncRun
		if err := rows.Scan(&r.ID, &r.SourceID, &r.NodeID, &r.Trigger, &r.State, &r.StartedAt, &r.FinishedAt,
			&r.ItemsSeen, &r.ItemsNew, &r.ItemsUpdated, &r.ItemsUnchanged, &r.ItemsMissing, &r.ItemsRejected,
			&r.RequestsMade, &r.RequestBudget, &r.ErrorMessage, &r.Stats, &r.SourceName); err != nil {
			return nil, wrapErr("listando execuções", err)
		}
		out = append(out, r)
	}
	return out, wrapErr("listando execuções", rows.Err())
}

// GetRunningSyncRun devolve a execução em andamento de uma fonte, se houver.
func (s *Store) GetRunningSyncRun(ctx context.Context, sourceID int64) (*SyncRun, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+syncRunColumns+` FROM sync_runs WHERE source_id = $1 AND state = 'running'`,
		sourceID)
	r, err := scanSyncRun(row)
	return r, wrapErr("buscando execução em andamento", err)
}

// ListRunningSyncRuns devolve todas as execuções em andamento.
//
// Usada pelo painel para mostrar quais fontes estão sincronizando agora — sem isso, o
// administrador recebe "aguarde" sem saber esperar o quê.
func (s *Store) ListRunningSyncRuns(ctx context.Context) ([]SyncRun, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+syncRunColumns+`, s.name
		FROM sync_runs r
		JOIN sources s ON s.id = r.source_id
		WHERE r.state = 'running'
		ORDER BY r.started_at`)
	if err != nil {
		return nil, wrapErr("listando execuções em andamento", err)
	}
	defer rows.Close()

	out := []SyncRun{}
	for rows.Next() {
		var r SyncRun
		if err := rows.Scan(&r.ID, &r.SourceID, &r.NodeID, &r.Trigger, &r.State, &r.StartedAt,
			&r.FinishedAt, &r.ItemsSeen, &r.ItemsNew, &r.ItemsUpdated, &r.ItemsUnchanged,
			&r.ItemsMissing, &r.ItemsRejected, &r.RequestsMade, &r.RequestBudget,
			&r.ErrorMessage, &r.Stats, &r.SourceName); err != nil {
			return nil, wrapErr("listando execuções em andamento", err)
		}
		out = append(out, r)
	}
	return out, wrapErr("listando execuções em andamento", rows.Err())
}

// GetSyncRun busca uma execução por id.
func (s *Store) GetSyncRun(ctx context.Context, id int64) (*SyncRun, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+syncRunColumns+` FROM sync_runs WHERE id = $1`, id)
	r, err := scanSyncRun(row)
	return r, wrapErr("buscando execução", err)
}

// ReleaseStaleRuns fecha execuções que ficaram penduradas por queda do processo.
//
// Sem isso, o índice de "uma run ativa por fonte" bloquearia a fonte indefinidamente
// depois de um encerramento abrupto.
//
// Duas regras, e a primeira é a que importa na prática:
//
//  1. Execuções DESTE nó são liberadas sempre. Se estamos iniciando, nenhuma execução
//     nossa pode estar viva — ela morreu junto com o processo anterior. Esperar um prazo
//     aqui deixaria a fonte travada por horas depois de um simples reinício.
//  2. Execuções de OUTROS nós só são liberadas depois do prazo, porque elas podem estar
//     legitimamente em andamento em outra máquina.
func (s *Store) ReleaseStaleRuns(ctx context.Context, nodeID string, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sync_runs
		SET state = 'failed', finished_at = now(),
		    error_message = 'execução interrompida: o processo foi encerrado durante a sincronização'
		WHERE state = 'running'
		  AND (node_id = $1 OR started_at < now() - $2::interval)`,
		nodeID, olderThan.String())
	if err != nil {
		return 0, wrapErr("liberando execuções travadas", err)
	}
	return tag.RowsAffected(), nil
}

// --- Estado de sincronização da fonte ---------------------------------------

// GetSyncState lê o estado persistido de uma fonte.
func (s *Store) GetSyncState(ctx context.Context, sourceID int64) (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT sync_state FROM sources WHERE id = $1`, sourceID).Scan(&raw)
	return raw, wrapErr("lendo estado de sincronização", err)
}

// SaveSyncState grava o estado e atualiza os marcadores de última sincronização.
func (s *Store) SaveSyncState(ctx context.Context, sourceID int64, state json.RawMessage, sucesso bool) error {
	if len(state) == 0 {
		state = json.RawMessage("{}")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE sources SET
			sync_state      = $2,
			last_sync_at    = now(),
			last_success_at = CASE WHEN $3 THEN now() ELSE last_success_at END,
			status          = CASE WHEN $3 THEN 'ok' ELSE 'degraded' END,
			updated_at      = now()
		WHERE id = $1`, sourceID, state, sucesso)
	return wrapErr("gravando estado de sincronização", err)
}

// UpdateSourceBudget ajusta o teto de requisições por execução de uma fonte.
func (s *Store) UpdateSourceBudget(ctx context.Context, sourceID int64, budget int) (int, error) {
	var atual int
	err := s.pool.QueryRow(ctx,
		`UPDATE sources SET request_budget = $2, updated_at = now() WHERE id = $1 RETURNING request_budget`,
		sourceID, budget).Scan(&atual)
	return atual, wrapErr("ajustando teto de requisições", err)
}

// SourceDue é uma fonte que já passou da hora de sincronizar.
type SourceDue struct {
	ID       int64
	Name     string
	Interval int
}

// ListSourcesDue devolve as fontes habilitadas cujo intervalo de sincronização venceu.
func (s *Store) ListSourcesDue(ctx context.Context) ([]SourceDue, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.name, s.sync_interval_minutes
		FROM sources s
		WHERE s.enabled
		  AND (s.last_sync_at IS NULL
		       OR s.last_sync_at < now() - make_interval(mins => s.sync_interval_minutes))
		  AND NOT EXISTS (
		      SELECT 1 FROM sync_runs r WHERE r.source_id = s.id AND r.state = 'running'
		  )
		ORDER BY s.priority, s.id`)
	if err != nil {
		return nil, wrapErr("listando fontes vencidas", err)
	}
	defer rows.Close()

	out := []SourceDue{}
	for rows.Next() {
		var d SourceDue
		if err := rows.Scan(&d.ID, &d.Name, &d.Interval); err != nil {
			return nil, wrapErr("listando fontes vencidas", err)
		}
		out = append(out, d)
	}
	return out, wrapErr("listando fontes vencidas", rows.Err())
}
