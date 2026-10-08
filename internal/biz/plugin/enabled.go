package plugin

import (
	"context"
	plugindomain "github.com/xingexin/catbot/internal/domain/plugin"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

func (a *Manager) SetPluginEnabled(ctx context.Context, id string, enabled bool) error {
	unlock, err := a.Store.Lock(ctx, "plugin-config:"+id)
	if err != nil {
		return err
	}
	defer unlock()
	var p plugindomain.Plugin
	if err := a.Store.Get(ctx, "plugin", id, &p); err != nil {
		return err
	}
	if enabled {
		if err := a.Health(ctx, id); err != nil {
			return err
		}
	}
	p.Enabled = enabled
	if err := a.Store.Put(ctx, "plugin", id, p); err != nil {
		return err
	}
	if !enabled {
		tasks, err := store.All[taskentity.Task](ctx, a.Store, "task")
		if err != nil {
			return err
		}
		for _, t := range tasks {
			if err := a.PauseDependent(ctx, id, t.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
