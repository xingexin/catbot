package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xingexin/catbot/internal/biz/plugin"
	plugindomain "github.com/xingexin/catbot/internal/domain/plugin"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	taskrepository "github.com/xingexin/catbot/internal/domain/task/repository"
	taskservice "github.com/xingexin/catbot/internal/domain/task/service"
	"github.com/xingexin/catbot/internal/infra/store"
)

type Host interface {
	Step(context.Context, taskentity.StepInput) (any, error)
	Notify(context.Context, taskentity.Snapshot, string, string) error
}
type ExecutionHost struct {
	Store           store.Store
	Host            Host
	ExecutionClosed func(context.Context, string) (bool, error)
}

func (e *ExecutionHost) Begin(ctx context.Context, in taskentity.Input, id string) (taskentity.Snapshot, error) {
	unlock, err := taskrepository.New(e.Store).LockTask(ctx, in.TaskID)
	if err != nil {
		return taskentity.Snapshot{}, err
	}
	defer unlock()
	var prior taskentity.Snapshot
	if err := e.Store.Get(ctx, "execution-snapshot", id, &prior); err == nil {
		return prior, e.ensureExecution(ctx, prior, in, id)
	} else if !errors.Is(err, store.ErrNotFound) {
		return prior, err
	}
	t, err := taskrepository.New(e.Store).Task(ctx, in.TaskID)
	if err != nil {
		return taskentity.Snapshot{}, err
	}
	if t.Status == "provisioning" {
		return taskentity.Snapshot{}, errors.New("task is still provisioning")
	}
	if t.Revision != in.Revision || t.Status == "cancelled" || (!in.Manual && t.Paused) {
		_ = taskrepository.New(e.Store).SaveExecution(ctx, taskentity.TaskExecution{ID: id, TaskID: t.ID, Status: "skipped", Error: "task superseded, paused or cancelled", StartedAt: time.Now().UTC()})
		return taskentity.Snapshot{}, taskentity.NewExecutionError("task superseded, paused or cancelled", "InactiveTask", nil)
	}
	if err := e.checkOverlap(ctx, t.ID, id); err != nil {
		return taskentity.Snapshot{}, err
	}
	for pluginID := range t.Versions {
		var p plugindomain.Plugin
		if err := e.Store.Get(ctx, "plugin", pluginID, &p); err != nil || !p.Enabled {
			_ = taskrepository.New(e.Store).SaveExecution(ctx, taskentity.TaskExecution{ID: id, TaskID: t.ID, Status: "skipped", Error: "plugin dependency unavailable: " + pluginID, StartedAt: time.Now().UTC()})
			return taskentity.Snapshot{}, taskentity.NewExecutionError("plugin dependency unavailable: "+pluginID, "PluginDisabled", err)
		}
		key, err := plugin.Pin(ctx, e.Store, p)
		if err != nil {
			return taskentity.Snapshot{}, err
		}
		t.Versions[pluginID] = key
	}
	snap := taskentity.Snapshot{Task: t}
	if err := e.Store.Get(ctx, "config", t.ConfigID, &snap.Config); err != nil {
		return snap, err
	}
	if err := e.Store.Get(ctx, "persona", t.PersonaID, &snap.Persona); err != nil {
		return snap, err
	}
	// A snapshot must exist before a running occupancy record. If this first
	// write fails through all Activity retries, later polls remain runnable.
	// Once saved, a retry uses that same snapshot and repairs the second write.
	if err := e.Store.Put(ctx, "execution-snapshot", id, snap); err != nil {
		return snap, err
	}
	return snap, e.ensureExecution(ctx, snap, in, id)
}

func (e *ExecutionHost) ensureExecution(ctx context.Context, snap taskentity.Snapshot, in taskentity.Input, id string) error {
	current, err := taskrepository.New(e.Store).Execution(ctx, id)
	if err == nil {
		if current.Status == "skipped" || current.Status == "interrupted" {
			return taskentity.NewExecutionError("execution is no longer active", "InactiveTask", nil)
		}
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	task, err := taskrepository.New(e.Store).Task(ctx, snap.Task.ID)
	if err != nil {
		return err
	}
	if task.Revision != in.Revision || task.Status == "cancelled" || (!in.Manual && task.Paused) {
		if err := taskrepository.New(e.Store).SaveExecution(ctx, taskentity.TaskExecution{ID: id, TaskID: task.ID, Status: "skipped", Error: "task superseded, paused or cancelled before execution began", StartedAt: time.Now().UTC()}); err != nil {
			return err
		}
		return taskentity.NewExecutionError("task superseded, paused or cancelled", "InactiveTask", nil)
	}
	if err := e.checkOverlap(ctx, snap.Task.ID, id); err != nil {
		return err
	}
	x := taskentity.TaskExecution{ID: id, TaskID: snap.Task.ID, Status: "running", Results: map[string]any{}, StartedAt: time.Now().UTC(), Config: &snap.Config, Persona: &snap.Persona, Versions: snap.Task.Versions}
	return taskrepository.New(e.Store).SaveExecution(ctx, x)
}

func (e *ExecutionHost) checkOverlap(ctx context.Context, taskID, id string) error {
	existing, err := taskrepository.New(e.Store).Executions(ctx, taskID)
	if err != nil {
		return err
	}
	for _, x := range existing {
		if x.TaskID != taskID || x.ID == id || x.Status != "running" {
			continue
		}
		closed := false
		if e.ExecutionClosed != nil {
			// Release occupancy only after the scheduler confirms closure.
			// Never infer closure from elapsed time or replay prior actions.
			closed, err = e.ExecutionClosed(ctx, x.ID)
			if err != nil {
				return fmt.Errorf("verify active task execution: %w", err)
			}
		}
		if closed {
			now := time.Now().UTC()
			x.Status, x.Error, x.FinishedAt = "interrupted", "workflow is no longer active; inspect saved results and delivery state before retrying", &now
			if err := taskrepository.New(e.Store).SaveExecution(ctx, x); err != nil {
				return err
			}
			continue
		}
		if err := taskrepository.New(e.Store).SaveExecution(ctx, taskentity.TaskExecution{ID: id, TaskID: taskID, Status: "skipped", Error: "another execution is active", StartedAt: time.Now().UTC()}); err != nil {
			return err
		}
		return taskentity.NewExecutionError("another execution is active", "OverlapSkipped", nil)
	}
	return nil
}

func (e *ExecutionHost) ExecuteStep(ctx context.Context, in taskentity.StepInput) (any, error) {
	resultID := in.ExecutionID + ":" + in.Step.ID
	var prior any
	if err := e.Store.Get(ctx, "step-result", resultID, &prior); err == nil {
		return map[string]any{"_resultRef": resultID}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	hydrated, err := e.hydrate(ctx, in.Results)
	if err != nil {
		return nil, err
	}
	in.Results = hydrated
	value, err := e.Host.Step(ctx, in)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		if in.Step.Kind == "tool" {
			parts := strings.SplitN(in.Step.Tool, "__", 2)
			if len(parts) == 2 {
				var p plugindomain.Plugin
				if e.Store.Get(ctx, "plugin-version", in.Snapshot.Task.Versions[parts[0]], &p) == nil {
					for _, spec := range p.Manifest.Tools {
						if spec.Name == parts[1] && spec.RetrySafe {
							return nil, err
						}
					}
				}
			}
		}
		return nil, taskentity.NewExecutionError(err.Error(), "StepFailed", err)
	}
	if err := e.Store.Put(ctx, "step-result", resultID, value); err != nil {
		return nil, err
	}
	x, err := taskrepository.New(e.Store).Execution(ctx, in.ExecutionID)
	if err != nil {
		return nil, err
	}
	if x.Results == nil {
		x.Results = map[string]any{}
	}
	x.Results[in.Step.ID] = value
	if err := taskrepository.New(e.Store).SaveExecution(ctx, x); err != nil {
		return nil, err
	}
	return map[string]any{"_resultRef": resultID}, nil
}

func (e *ExecutionHost) hydrate(ctx context.Context, results map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for key, value := range results {
		if obj, ok := value.(map[string]any); ok {
			if ref, ok := obj["_resultRef"].(string); ok {
				var full any
				if err := e.Store.Get(ctx, "step-result", ref, &full); err != nil {
					return nil, err
				}
				out[key] = full
				continue
			}
		}
		out[key] = value
	}
	return out, nil
}

func (e *ExecutionHost) Finish(ctx context.Context, snapshot taskentity.Snapshot, id, status, message string, result map[string]any) error {
	x, err := taskrepository.New(e.Store).Execution(ctx, id)
	if err != nil {
		return err
	}
	// A retry may only need to repair the task status after the execution was
	// saved. Do not reset a recorded notification failure or attempt it again.
	if x.Status == "notification_failed" {
		return e.finishOnceTask(ctx, snapshot.Task, x)
	}
	full, err := e.hydrate(ctx, result)
	if err != nil {
		return err
	}
	result = full
	x.Status = status
	x.Error = message
	x.Results = result
	now := time.Now().UTC()
	x.FinishedAt = &now
	if err := taskrepository.New(e.Store).SaveExecution(ctx, x); err != nil {
		return err
	}
	if snapshot.Task.Notify {
		text, send, notificationErr := taskservice.Notification(snapshot.Task, status, message, result)
		if notificationErr == nil {
			allowed, policyErr := e.allowNotification(ctx, snapshot.Task, id, status)
			if policyErr != nil {
				return policyErr
			}
			if send && allowed {
				notificationErr = e.Host.Notify(ctx, snapshot, text, "task-notify:"+id)
			}
		}
		if notificationErr != nil {
			x.Status = "notification_failed"
			x.Error = notificationErr.Error()
			if err := taskrepository.New(e.Store).SaveExecution(ctx, x); err != nil {
				return err
			}
		}
	}
	return e.finishOnceTask(ctx, snapshot.Task, x)
}

func (e *ExecutionHost) finishOnceTask(ctx context.Context, snapshot taskentity.Task, x taskentity.TaskExecution) error {
	if snapshot.Kind != "once" {
		return nil
	}
	unlock, err := taskrepository.New(e.Store).LockTask(ctx, snapshot.ID)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := taskrepository.New(e.Store).Task(ctx, snapshot.ID)
	if err != nil {
		return err
	}
	if current.Revision != snapshot.Revision || current.Status == "cancelled" {
		return nil
	}
	current.Status = x.Status
	current.Error = x.Error
	return taskrepository.New(e.Store).SaveTask(ctx, current)
}

type notificationIncident struct {
	ExecutionID string    `json:"executionId"`
	AttemptedAt time.Time `json:"attemptedAt"`
}

func (e *ExecutionHost) allowNotification(ctx context.Context, t taskentity.Task, id, status string) (bool, error) {
	if t.Kind != "recurring" {
		return true, nil
	}
	unlock, err := e.Store.Lock(ctx, "notification-incident:"+t.ID)
	if err != nil {
		return false, err
	}
	defer unlock()
	if status == "completed" {
		return true, e.Store.Delete(ctx, "notification-incident", t.ID)
	}
	var incident notificationIncident
	err = e.Store.Get(ctx, "notification-incident", t.ID, &incident)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if err == nil && incident.ExecutionID != id && time.Since(incident.AttemptedAt) < time.Hour {
		return false, nil
	}
	if incident.ExecutionID != id {
		incident = notificationIncident{ExecutionID: id, AttemptedAt: time.Now().UTC()}
		if err := e.Store.Put(ctx, "notification-incident", t.ID, incident); err != nil {
			return false, err
		}
	}
	return true, nil
}
