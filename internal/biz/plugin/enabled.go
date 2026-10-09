package plugin

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
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
	if err := lifecycleRepository.RequireActive(ctx, a.Store, lifecycle.ResourcePlugin, id); err != nil {
		return err
	}
	var p plugindomain.Plugin
	if err := a.Store.Get(ctx, "plugin", id, &p); err != nil {
		return err
	}
	return a.setPluginEnabledLocked(ctx, p, enabled)
}

func (a *Manager) setPluginEnabledLocked(ctx context.Context, p plugindomain.Plugin, enabled bool) error {
	id := p.ID
	if enabled {
		if err := a.Health(ctx, id); err != nil {
			return err
		}
	}
	referenceUnlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return err
	}
	// Snapshot selection and task persistence hold this same lock. Once the
	// plugin is disabled, all previously selected dependencies are discoverable.
	tasks, err := func() ([]taskentity.Task, error) {
		defer referenceUnlock()
		p.Enabled = enabled
		if err := a.Store.Put(ctx, "plugin", id, p); err != nil {
			return nil, err
		}
		if enabled {
			return nil, nil
		}
		return store.All[taskentity.Task](ctx, a.Store, "task")
	}()
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if _, uses := t.Versions[id]; !uses {
			continue
		}
		if a.PauseDependent == nil {
			return errors.New("task dependency pause service is unavailable")
		}
		if err := a.PauseDependent(ctx, id, t.ID); err != nil {
			return err
		}
	}
	return nil
}
