package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xingexin/catbot/internal/domain"
)

type queuedRunReader interface {
	QueuedRuns(context.Context, []string, int) ([]domain.Run, error)
}

func queueLimit(limit int) int {
	if limit <= 0 {
		return 64
	}
	return min(limit, 128)
}

// QueuedRuns returns a bounded batch of foreground work, oldest first, excluding
// busy sessions before applying the limit. It uses a narrow optional query so
// existing Store decorators remain compatible. Execution still acquires the
// service's run/session locks; this query does not claim the returned work.
func QueuedRuns(ctx context.Context, s Store, excludedSessionIDs []string, limit int) ([]domain.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit = queueLimit(limit)
	if reader, ok := s.(queuedRunReader); ok {
		return reader.QueuedRuns(ctx, excludedSessionIDs, limit)
	}
	runs, err := All[domain.Run](ctx, s, "run")
	if err != nil {
		return nil, err
	}
	return filterQueued(ctx, runs, excludedSessionIDs, limit)
}

func (s *Postgres) QueuedRuns(ctx context.Context, excludedSessionIDs []string, limit int) ([]domain.Run, error) {
	// A nil slice encodes as SQL NULL, which would make NOT (... = ANY(...))
	// unknown and accidentally filter out every row.
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
	runs := []domain.Run{}
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scan queued run: %w", err)
		}
		var run domain.Run
		if err := json.Unmarshal(encoded, &run); err != nil {
			return nil, fmt.Errorf("decode queued run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read queued runs: %w", err)
	}
	return runs, nil
}

func (s *Memory) QueuedRuns(ctx context.Context, excludedSessionIDs []string, limit int) ([]domain.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runs, err := All[domain.Run](ctx, s, "run")
	if err != nil {
		return nil, err
	}
	return filterQueued(ctx, runs, excludedSessionIDs, queueLimit(limit))
}

func filterQueued(ctx context.Context, runs []domain.Run, excludedSessionIDs []string, limit int) ([]domain.Run, error) {
	excluded := make(map[string]bool, len(excludedSessionIDs))
	for _, id := range excludedSessionIDs {
		excluded[id] = true
	}
	queued := []domain.Run{}
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if run.Status == "queued" && !strings.HasPrefix(run.ID, "background-") && !excluded[run.SessionID] {
			queued = append(queued, run)
		}
	}
	sort.Slice(queued, func(i, j int) bool {
		if queued[i].CreatedAt.Equal(queued[j].CreatedAt) {
			return queued[i].ID < queued[j].ID
		}
		return queued[i].CreatedAt.Before(queued[j].CreatedAt)
	})
	return queued[:min(len(queued), queueLimit(limit))], nil
}
