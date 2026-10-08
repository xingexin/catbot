package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type pendingReplyReader interface {
	PendingReplies(context.Context, int) ([]json.RawMessage, error)
}

type pendingNotificationReader interface {
	PendingNotifications(context.Context, int) ([]json.RawMessage, error)
}

func outboxLimit(limit int) int {
	if limit <= 0 {
		return 32
	}
	return min(limit, 128)
}

// PendingReplies selects a bounded batch of finished foreground runs whose
// replies still need reconciliation. Selection does not claim the messages;
// callers must retain their delivery locks and stable operation IDs.
func PendingReplies(ctx context.Context, s Store, limit int) ([]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit = outboxLimit(limit)
	if reader, ok := s.(pendingReplyReader); ok {
		return reader.PendingReplies(ctx, limit)
	}
	rows, err := s.List(ctx, "run")
	if err != nil {
		return nil, err
	}
	pending := []runRecord{}
	for _, payload := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var run runRecord
		if err := json.Unmarshal(payload, &run); err != nil {
			return nil, fmt.Errorf("decode pending reply: %w", err)
		}
		if run.ReplyPending && run.Status != "queued" && run.Status != "running" && !strings.HasPrefix(run.ID, "background-") {
			run.payload = payload
			pending = append(pending, run)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sortedRunPayloads(pending, limit), nil
}

// PendingNotifications preserves each complete notification payload while
// selecting only pending deliveries, oldest first. Optional narrow readers
// let PostgreSQL filter and limit before transferring data to the worker.
func PendingNotifications(ctx context.Context, s Store, limit int) ([]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit = outboxLimit(limit)
	if reader, ok := s.(pendingNotificationReader); ok {
		return reader.PendingNotifications(ctx, limit)
	}
	raw, err := s.List(ctx, "notification")
	if err != nil {
		return nil, err
	}
	type candidate struct {
		ID        string    `json:"id"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"createdAt"`
		payload   json.RawMessage
	}
	pending := []candidate{}
	for _, payload := range raw {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var record candidate
		if err := json.Unmarshal(payload, &record); err != nil {
			return nil, fmt.Errorf("decode pending notification: %w", err)
		}
		if record.Status == "pending" {
			record.payload = payload
			pending = append(pending, record)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].CreatedAt.Equal(pending[j].CreatedAt) {
			return pending[i].ID < pending[j].ID
		}
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]json.RawMessage, 0, min(len(pending), limit))
	for _, record := range pending[:min(len(pending), limit)] {
		result = append(result, record.payload)
	}
	return result, nil
}

func (s *Postgres) PendingReplies(ctx context.Context, limit int) ([]json.RawMessage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT data FROM records
		WHERE kind='run' AND data @> '{"replyPending":true}'::jsonb
		  AND COALESCE(data->>'status', '') NOT IN ('queued', 'running')
		  AND id NOT LIKE 'background-%'
		ORDER BY (data->>'createdAt')::timestamptz NULLS FIRST, id
		LIMIT $1`, outboxLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("query pending replies: %w", err)
	}
	defer rows.Close()
	result := []json.RawMessage{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan pending reply: %w", err)
		}
		result = append(result, json.RawMessage(payload))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pending replies: %w", err)
	}
	return result, nil
}

func (s *Postgres) PendingNotifications(ctx context.Context, limit int) ([]json.RawMessage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT data FROM records
		WHERE kind='notification' AND data @> '{"status":"pending"}'::jsonb
		ORDER BY (data->>'createdAt')::timestamptz NULLS FIRST, id
		LIMIT $1`, outboxLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("query pending notifications: %w", err)
	}
	defer rows.Close()
	result := []json.RawMessage{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan pending notification: %w", err)
		}
		result = append(result, json.RawMessage(payload))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pending notifications: %w", err)
	}
	return result, nil
}
