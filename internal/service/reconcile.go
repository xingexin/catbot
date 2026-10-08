package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/xingexin/catbot/internal/domain"
	"github.com/xingexin/catbot/internal/store"
)

type scheduleIntent struct {
	Task     domain.Task  `json:"task"`
	Previous *domain.Task `json:"previous,omitempty"`
	Cancel   bool         `json:"cancel"`
}

func (a *App) reconcile(ctx context.Context) {
	if a.Scheduler == nil {
		return
	}
	intents, err := store.All[scheduleIntent](ctx, a.Store, "schedule-intent")
	if err != nil {
		slog.Error("read schedule intents", "error", err)
		return
	}
	for _, in := range intents {
		if err := a.reconcileOne(ctx, in); err != nil {
			slog.Warn("schedule reconciliation pending", "taskId", in.Task.ID, "error", err)
		}
	}
}
func (a *App) reconcileOne(ctx context.Context, in scheduleIntent) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	unlock, err := a.Store.Lock(ctx, "task:"+in.Task.ID)
	if err != nil {
		return err
	}
	defer unlock()
	// Re-read under the lock; a new user edit may have replaced this intent.
	if err := a.Store.Get(ctx, "schedule-intent", in.Task.ID, &in); err != nil {
		return err
	}
	var current domain.Task
	if err := a.Store.Get(ctx, "task", in.Task.ID, &current); err != nil {
		return err
	}
	if current.Revision != in.Task.Revision {
		return a.Store.Delete(ctx, "schedule-intent", in.Task.ID)
	}
	if in.Cancel {
		err = a.Scheduler.Cancel(ctx, current)
	} else {
		err = a.Scheduler.Apply(ctx, in.Task, in.Previous)
	}
	if err != nil {
		return err
	}
	if !in.Cancel && current.Status != "completed" && current.Status != "failed" {
		current.Status = "active"
		if current.Paused {
			current.Status = "paused"
		}
		current.Error = ""
		if err := a.Store.Put(ctx, "task", current.ID, current); err != nil {
			return err
		}
	}
	return a.Store.Delete(ctx, "schedule-intent", in.Task.ID)
}
