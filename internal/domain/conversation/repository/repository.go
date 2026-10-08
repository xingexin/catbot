package repository

import (
	"context"
	"encoding/json"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/infra/store"
)

type Repository struct{ store store.Store }

func New(s store.Store) *Repository { return &Repository{store: s} }
func (r *Repository) AppendEvent(ctx context.Context, e conversation.Event) error {
	return r.store.Append(ctx, store.EventRecord{Sequence: e.Sequence, RunID: e.RunID, Type: e.Type, Data: e.Data, Time: e.Time})
}
func (r *Repository) Events(ctx context.Context, id string, after int64) ([]conversation.Event, error) {
	rows, err := r.store.Events(ctx, id, after)
	if err != nil {
		return nil, err
	}
	out := make([]conversation.Event, 0, len(rows))
	for _, e := range rows {
		out = append(out, conversation.Event{Sequence: e.Sequence, RunID: e.RunID, Type: e.Type, Data: e.Data, Time: e.Time})
	}
	return out, nil
}
func (r *Repository) QueuedRuns(ctx context.Context, busy []string, limit int) ([]conversation.Run, error) {
	rows, err := store.QueuedRuns(ctx, r.store, busy, limit)
	return decodeRuns(rows, err)
}
func (r *Repository) PendingReplies(ctx context.Context, limit int) ([]conversation.Run, error) {
	rows, err := store.PendingReplies(ctx, r.store, limit)
	return decodeRuns(rows, err)
}
func decodeRuns(rows []json.RawMessage, err error) ([]conversation.Run, error) {
	if err != nil {
		return nil, err
	}
	out := make([]conversation.Run, 0, len(rows))
	for _, raw := range rows {
		var r conversation.Run
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
