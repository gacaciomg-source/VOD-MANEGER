package store

import (
	"context"
	"encoding/json"
	"time"
)

// Event é uma entrada do log estruturado de negócio.
type Event struct {
	ID       int64           `json:"id"`
	TS       time.Time       `json:"ts"`
	NodeID   string          `json:"node_id"`
	Level    string          `json:"level"`
	Category string          `json:"category"`
	Message  string          `json:"message"`
	Actor    *string         `json:"actor"`
	SourceID *int64          `json:"source_id"`
	Data     json.RawMessage `json:"data"`
}

// NewEvent são os campos aceitos ao registrar um evento.
type NewEvent struct {
	NodeID   string
	Level    string
	Category string
	Message  string
	Actor    string
	SourceID *int64
	Data     map[string]any
}

// EventFilter filtra a listagem de eventos.
type EventFilter struct {
	Category string
	Level    string
	SourceID *int64
	Since    *time.Time
	Limit    int
}

// InsertEvent grava um evento.
func (s *Store) InsertEvent(ctx context.Context, e NewEvent) error {
	data := []byte("{}")
	if len(e.Data) > 0 {
		encoded, err := json.Marshal(e.Data)
		if err != nil {
			return wrapErr("serializando dados do evento", err)
		}
		data = encoded
	}
	var actor *string
	if e.Actor != "" {
		actor = &e.Actor
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO events (node_id, level, category, message, actor, source_id, data)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.NodeID, e.Level, e.Category, e.Message, actor, e.SourceID, data)
	return wrapErr("gravando evento", err)
}

// ListEvents devolve os eventos mais recentes primeiro.
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, ts, node_id, level, category, message, actor, source_id, data
		FROM events
		WHERE ($1::text IS NULL OR category = $1)
		  AND ($2::text IS NULL OR level = $2)
		  AND ($3::bigint IS NULL OR source_id = $3)
		  AND ($4::timestamptz IS NULL OR ts >= $4)
		ORDER BY ts DESC, id DESC
		LIMIT $5`,
		nullIfEmpty(f.Category), nullIfEmpty(f.Level), f.SourceID, f.Since, limit)
	if err != nil {
		return nil, wrapErr("listando eventos", err)
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TS, &e.NodeID, &e.Level, &e.Category,
			&e.Message, &e.Actor, &e.SourceID, &e.Data); err != nil {
			return nil, wrapErr("listando eventos", err)
		}
		out = append(out, e)
	}
	return out, wrapErr("listando eventos", rows.Err())
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
