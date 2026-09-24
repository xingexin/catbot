package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentTest/internal/domain"
	"agentTest/internal/store"
)

func (a *App) SaveTask(ctx context.Context, t domain.Task) (domain.Task, error) {
	if a.Scheduler == nil {
		return t, errors.New("Temporal is not connected")
	}
	if t.ID == "" {
		t.ID = domain.ID()
	}
	unlock, err := a.Store.Lock(ctx, "task:"+t.ID)
	if err != nil {
		return t, err
	}
	defer unlock()
	return a.saveTaskLocked(ctx, t)
}
func (a *App) saveTaskLocked(ctx context.Context, t domain.Task) (domain.Task, error) {
	var previous *domain.Task
	var old domain.Task
	if err := a.Store.Get(ctx, "task", t.ID, &old); err == nil {
		if t.Revision != 0 && t.Revision != old.Revision {
			return t, errors.New("task changed; refresh before editing")
		}
		previous = &old
		t.Revision = old.Revision + 1
	} else if !errors.Is(err, store.ErrNotFound) {
		return t, err
	} else {
		t.Revision = 1
	}
	if t.Name == "" || len(t.Steps) == 0 || len(t.Steps) > 20 {
		return t, errors.New("task needs a name and 1..20 steps")
	}
	if t.TimeZone == "" {
		t.TimeZone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(t.TimeZone); err != nil {
		return t, errors.New("invalid time zone")
	}
	switch t.Kind {
	case "once":
		if t.RunAt == nil || t.RunAt.Before(time.Now().Add(-time.Minute)) {
			return t, errors.New("once task requires a future runAt")
		}
	case "recurring":
		if t.Cron == "" {
			return t, errors.New("recurring task requires cron")
		}
	case "manual":
	default:
		return t, errors.New("task kind must be once, recurring or manual")
	}
	if t.CatchupSec == 0 {
		t.CatchupSec = 3600
	}
	if t.CatchupSec < 10 {
		return t, errors.New("catchupSec must be at least 10")
	}
	var config domain.Config
	if err := a.Store.Get(ctx, "config", t.ConfigID, &config); err != nil {
		return t, fmt.Errorf("task configuration: %w", err)
	}
	var persona domain.Persona
	if err := a.Store.Get(ctx, "persona", t.PersonaID, &persona); err != nil {
		return t, fmt.Errorf("task persona: %w", err)
	}
	if t.SessionID != "" {
		var s domain.Session
		if err := a.Store.Get(ctx, "session", t.SessionID, &s); err != nil {
			return t, err
		}
	}
	versions, err := a.Plugins.Snapshots(ctx)
	if err != nil {
		return t, err
	}
	t.Versions = versions
	tools, err := a.Plugins.Tools(ctx, versions, persona.Tools)
	if err != nil {
		return t, err
	}
	allowed := map[string]bool{}
	for _, tool := range tools {
		allowed[tool.Name] = true
	}
	seen := map[string]bool{}
	dependencies := map[string]string{}
	for _, step := range t.Steps {
		if step.DelaySec < 0 || step.DelaySec > 31*86400 {
			return t, errors.New("step delay must be 0..2678400 seconds")
		}
		if step.ID == "" || seen[step.ID] {
			return t, errors.New("step IDs must be unique and nonempty")
		}
		seen[step.ID] = true
		switch step.Kind {
		case "tool":
			if !allowed[step.Tool] {
				return t, fmt.Errorf("tool not available: %s", step.Tool)
			}
			id := strings.SplitN(step.Tool, "__", 2)[0]
			dependencies[id] = versions[id]
		case "agent":
			if step.Prompt == "" {
				return t, errors.New("agent step prompt is required")
			}
			for _, tool := range tools {
				dependencies[tool.PluginID] = versions[tool.PluginID]
			}
		default:
			return t, errors.New("step kind must be tool or agent")
		}
	}
	t.Versions = dependencies
	t.Status = "provisioning"
	t.Error = ""
	if err := a.Store.Put(ctx, "schedule-intent", t.ID, scheduleIntent{Task: t, Previous: previous}); err != nil {
		return t, err
	}
	if err := a.Store.Put(ctx, "task", t.ID, t); err != nil {
		return t, err
	}
	if err := a.Scheduler.Apply(ctx, t, previous); err != nil {
		t.Status = "error"
		t.Error = err.Error()
		_ = a.Store.Put(ctx, "task", t.ID, t)
		return t, err
	}
	t.Status = "active"
	if t.Paused {
		t.Status = "paused"
	}
	if err := a.Store.Put(ctx, "task", t.ID, t); err != nil {
		return t, err
	}
	return t, a.Store.Delete(ctx, "schedule-intent", t.ID)
}
func (a *App) ControlTask(ctx context.Context, id, action string) (any, error) {
	return a.controlTask(ctx, id, action, domain.ID())
}
func (a *App) controlTask(ctx context.Context, id, action, operationID string) (any, error) {
	if a.Scheduler == nil {
		return nil, errors.New("Temporal is not connected")
	}
	var t domain.Task
	unlock, err := a.Store.Lock(ctx, "task:"+id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := a.Store.Get(ctx, "task", id, &t); err != nil {
		return nil, err
	}
	if action == "trigger" {
		if t.Status == "cancelled" {
			return nil, errors.New("task is cancelled")
		}
		executionID, err := a.Scheduler.Trigger(ctx, t, operationID)
		return map[string]any{"executionId": executionID}, err
	}
	switch action {
	case "pause":
		t.Paused = true
	case "resume":
		t.Paused = false
	case "cancel":
		t.Status = "cancelled"
		t.Paused = true
		if err := a.Store.Put(ctx, "schedule-intent", id, scheduleIntent{Task: t, Cancel: true}); err != nil {
			return nil, err
		}
		if err := a.Store.Put(ctx, "task", id, t); err != nil {
			return nil, err
		}
		if err := a.Scheduler.Cancel(ctx, t); err != nil {
			t.Error = err.Error()
			_ = a.Store.Put(ctx, "task", id, t)
			return t, err
		}
		return t, a.Store.Delete(ctx, "schedule-intent", id)
	default:
		return nil, errors.New("unknown task action")
	}
	return a.saveTaskLocked(ctx, t)
}
func (a *App) SetPluginEnabled(ctx context.Context, id string, enabled bool) error {
	unlock, err := a.Store.Lock(ctx, "plugin-config:"+id)
	if err != nil {
		return err
	}
	defer unlock()
	var p domain.Plugin
	if err := a.Store.Get(ctx, "plugin", id, &p); err != nil {
		return err
	}
	if enabled {
		if err := a.Plugins.Health(ctx, id); err != nil {
			return err
		}
	}
	p.Enabled = enabled
	if err := a.Store.Put(ctx, "plugin", id, p); err != nil {
		return err
	}
	if !enabled {
		tasks, err := store.All[domain.Task](ctx, a.Store, "task")
		if err != nil {
			return err
		}
		for _, t := range tasks {
			if err := a.pauseDependent(ctx, id, t.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
func (a *App) pauseDependent(ctx context.Context, id, taskID string) error {
	unlock, err := a.Store.Lock(ctx, "task:"+taskID)
	if err != nil {
		return err
	}
	defer unlock()
	var t domain.Task
	if err := a.Store.Get(ctx, "task", taskID, &t); err != nil {
		return err
	}
	if _, uses := t.Versions[id]; uses && !t.Paused && t.Status != "cancelled" {
		// Retain pinned snapshots for executions already in flight.
		old := t
		t.Paused = true
		t.Status = "paused"
		t.Error = "dependency disabled: " + id
		if err := a.Store.Put(ctx, "schedule-intent", t.ID, scheduleIntent{Task: t, Previous: &old}); err != nil {
			return err
		}
		if err := a.Store.Put(ctx, "task", t.ID, t); err != nil {
			return err
		}
		if a.Scheduler != nil {
			if err := a.Scheduler.Apply(ctx, t, &old); err != nil {
				return err
			}
			if err := a.Store.Delete(ctx, "schedule-intent", t.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
