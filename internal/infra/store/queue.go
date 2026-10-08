package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type queuedRunReader interface {
	QueuedRuns(context.Context, []string, int) ([]json.RawMessage, error)
}

// runRecord projects only indexed selection fields and retains the original JSON.
// The persistence layer does not own the conversation entity or its snapshots.
type runRecord struct {
	ID           string    `json:"id"`
	SessionID    string    `json:"sessionId"`
	Status       string    `json:"status"`
	ReplyPending bool      `json:"replyPending,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	payload      json.RawMessage
}

func queueLimit(limit int) int {
	if limit <= 0 {
		return 64
	}
	return min(limit, 128)
}

// QueuedRuns selects a bounded batch, excluding busy sessions before the limit.
// The returned JSON is unchanged; the conversation repository owns decoding.
func QueuedRuns(ctx context.Context, s Store, excludedSessionIDs []string, limit int) ([]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit = queueLimit(limit)
	if reader, ok := s.(queuedRunReader); ok {
		return reader.QueuedRuns(ctx, excludedSessionIDs, limit)
	}
	rows, err := s.List(ctx, "run")
	if err != nil {
		return nil, err
	}
	return filterQueued(ctx, rows, excludedSessionIDs, limit)
}

func (s *Postgres) QueuedRuns(ctx context.Context, excludedSessionIDs []string, limit int) ([]json.RawMessage, error) {
	// A nil slice would encode SQL NULL and filter every row.
	excluded := append([]string{}, excludedSessionIDs...)
	rows, err := s.pool.Query(ctx, `
  SELECT data FROM records
  WHERE kind='run' AND data->>'status'='queued'
    AND id NOT LIKE 'background-%'
    AND NOT (COALESCE(data->>'sessionId', '') = ANY($1::text[]))
  ORDER BY (data->>'createdAt')::timestamptz, id
  LIMIT $2`, excluded, queueLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("query queued runs: %w", err)
	}
	defer rows.Close()
	result := []json.RawMessage{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan queued run: %w", err)
		}
		result = append(result, json.RawMessage(payload))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read queued runs: %w", err)
	}
	return result, nil
}

func (s *Memory) QueuedRuns(ctx context.Context, excludedSessionIDs []string, limit int) ([]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.List(ctx, "run")
	if err != nil {
		return nil, err
	}
	return filterQueued(ctx, rows, excludedSessionIDs, queueLimit(limit))
}

func filterQueued(ctx context.Context, rows []json.RawMessage, excludedSessionIDs []string, limit int) ([]json.RawMessage, error) {
	excluded := make(map[string]bool, len(excludedSessionIDs))
	for _, id := range excludedSessionIDs {
		excluded[id] = true
	}
	selected := []runRecord{}
	for _, payload := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var run runRecord
		if err := json.Unmarshal(payload, &run); err != nil {
			return nil, fmt.Errorf("decode queued run: %w", err)
		}
		if run.Status == "queued" && !strings.HasPrefix(run.ID, "background-") && !excluded[run.SessionID] {
			run.payload = payload
			selected = append(selected, run)
		}
	}
	return sortedRunPayloads(selected, queueLimit(limit)), nil
}

func sortedRunPayloads(records []runRecord, limit int) []json.RawMessage {
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	result := make([]json.RawMessage, 0, min(len(records), limit))
	for _, record := range records[:min(len(records), limit)] {
		result = append(result, record.payload)
	}
	return result
}
