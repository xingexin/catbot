package system

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/domain/conversation"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

func (a *Service) DeleteReferenced(ctx context.Context, kind, id string) error {
	if a.CheckBindings != nil {
		if err := a.CheckBindings(ctx, kind, id); err != nil {
			return err
		}
	}
	sessions, err := store.All[conversation.Session](ctx, a.Store, "session")
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if (kind == "persona" && s.PersonaID == id) || (kind == "config" && s.ConfigID == id) {
			return errors.New("record is used by a conversation")
		}
	}
	tasks, err := store.All[taskentity.Task](ctx, a.Store, "task")
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if (kind == "persona" && t.PersonaID == id) || (kind == "config" && t.ConfigID == id) {
			return errors.New("record is used by a task")
		}
	}
	if err := a.Store.Delete(ctx, kind, id); err != nil {
		return err
	}
	return nil
}
