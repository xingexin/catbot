package repository

import (
	"context"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/infra/store"
)

func (r *Repository) GetRun(ctx context.Context, id string, v *conversation.Run) error {
	return r.store.Get(ctx, "run", id, v)
}
func (r *Repository) SaveRun(ctx context.Context, v conversation.Run) error {
	return r.store.Put(ctx, "run", v.ID, v)
}
func (r *Repository) Runs(ctx context.Context) ([]conversation.Run, error) {
	return store.All[conversation.Run](ctx, r.store, "run")
}
func (r *Repository) GetSession(ctx context.Context, id string, v *conversation.Session) error {
	return r.store.Get(ctx, "session", id, v)
}
func (r *Repository) SaveSession(ctx context.Context, v conversation.Session) error {
	return r.store.Put(ctx, "session", v.ID, v)
}
func (r *Repository) Sessions(ctx context.Context) ([]conversation.Session, error) {
	return store.All[conversation.Session](ctx, r.store, "session")
}
