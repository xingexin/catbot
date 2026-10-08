package repository

import (
	"context"
	"github.com/xingexin/catbot/internal/domain/persona"
	"github.com/xingexin/catbot/internal/infra/store"
)

type Repository struct{ store store.Store }

func New(s store.Store) *Repository { return &Repository{store: s} }
func (r *Repository) Get(ctx context.Context, id string, v *persona.Persona) error {
	return r.store.Get(ctx, "persona", id, v)
}
func (r *Repository) Save(ctx context.Context, v persona.Persona) error {
	return r.store.Put(ctx, "persona", v.ID, v)
}
func (r *Repository) List(ctx context.Context) ([]persona.Persona, error) {
	return store.All[persona.Persona](ctx, r.store, "persona")
}
