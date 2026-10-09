package task

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	taskrepository "github.com/xingexin/catbot/internal/domain/task/repository"
	taskservice "github.com/xingexin/catbot/internal/domain/task/service"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
)

type Scheduler interface {
	Apply(context.Context, taskentity.Task, *taskentity.Task) error
	Trigger(context.Context, taskentity.Task, string) (string, error)
	Cancel(context.Context, taskentity.Task) error
	Ping(context.Context) error
}
type Plugins interface {
	Snapshots(context.Context) (map[string]string, error)
	Tools(context.Context, map[string]string, []string) ([]agent.Tool, error)
}
type Commands struct {
	Store     store.Store
	Scheduler Scheduler
	Plugins   Plugins
}

func (a *Commands) Connected() bool { return a.Scheduler != nil }
func (a *Commands) SaveTask(ctx context.Context, t taskentity.Task) (taskentity.Task, error) {
	if a.Scheduler == nil {
		return t, errors.New("Temporal is not connected")
	}
	if t.ID == "" {
		t.ID = idgen.New()
	}
	unlock, err := taskrepository.New(a.Store).LockTask(ctx, t.ID)
	if err != nil {
		return t, err
	}
	defer unlock()
	return a.SaveTaskLocked(ctx, t)
}

func (a *Commands) SaveTaskLocked(ctx context.Context, t taskentity.Task) (taskentity.Task, error) {
	referenceUnlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return t, err
	}
	defer referenceUnlock()

	if err := requireTaskActive(ctx, a.Store, t.ID); err != nil {
		return t, err
	}
	for resource, id := range map[lifecycle.Resource]string{lifecycle.ResourceConfig: t.ConfigID, lifecycle.ResourcePersona: t.PersonaID, lifecycle.ResourceSession: t.SessionID} {
		if err := lifecycleRepository.RequireActive(ctx, a.Store, resource, id); err != nil {
			return t, err
		}
	}

	var previous *taskentity.Task
	old, err := taskrepository.New(a.Store).Task(ctx, t.ID)
	if err == nil {
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
	if err := taskservice.ValidateDefinition(&t, time.Now()); err != nil {
		return t, err
	}
	var config agent.Config
	if err := a.Store.Get(ctx, "config", t.ConfigID, &config); err != nil {
		return t, fmt.Errorf("task configuration: %w", err)
	}
	var persona persona.Persona
	if err := a.Store.Get(ctx, "persona", t.PersonaID, &persona); err != nil {
		return t, fmt.Errorf("task persona: %w", err)
	}
	if t.Notify && t.SessionID == "" {
		return t, errors.New("开启通知时必须选择结果通知会话")
	}
	if t.SessionID != "" {
		var s conversation.Session
		if err := a.Store.Get(ctx, "session", t.SessionID, &s); err != nil {
			return t, err
		}
		if t.Notify && s.Channel == "task" {
			return t, errors.New("请选择用户的 QQ 或 Web 会话接收通知，不能选择后台任务会话")
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
	if err := taskservice.ValidateSteps(&t, versions, tools); err != nil {
		return t, err
	}

	t.Status = "provisioning"
	t.Error = ""
	if err := a.Store.Put(ctx, "schedule-intent", t.ID, scheduleIntent{Task: t, Previous: previous}); err != nil {
		return t, err
	}
	if err := taskrepository.New(a.Store).SaveTask(ctx, t); err != nil {
		return t, err
	}
	if err := a.Scheduler.Apply(ctx, t, previous); err != nil {
		t.Status = "error"
		t.Error = err.Error()
		_ = taskrepository.New(a.Store).SaveTask(ctx, t)
		return t, err
	}
	t.Status = "active"
	if t.Paused {
		t.Status = "paused"
	}
	if err := taskrepository.New(a.Store).SaveTask(ctx, t); err != nil {
		return t, err
	}
	return t, a.Store.Delete(ctx, "schedule-intent", t.ID)
}

func (a *Commands) ControlTask(ctx context.Context, id, action string) (any, error) {
	return a.ControlTaskWithOperation(ctx, id, action, idgen.New())
}

func (a *Commands) ControlTaskWithOperation(ctx context.Context, id, action, operationID string) (any, error) {
	return a.ControlTaskForSession(ctx, id, action, operationID, "")
}

func (a *Commands) ControlTaskForSession(ctx context.Context, id, action, operationID, expectedSessionID string) (any, error) {
	if a.Scheduler == nil {
		return nil, errors.New("Temporal is not connected")
	}
	var t taskentity.Task
	unlock, err := taskrepository.New(a.Store).LockTask(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := requireTaskActive(ctx, a.Store, id); err != nil {
		return nil, err
	}
	t, err = taskrepository.New(a.Store).Task(ctx, id)
	if err != nil {
		return nil, err
	}
	if expectedSessionID != "" && t.SessionID != expectedSessionID {
		return nil, errors.New("task not authorized for this group conversation")
	}
	control := taskentity.ParseControlAction(action)
	if control == taskentity.ControlTrigger {
		if taskentity.ParseState(t.Status) == taskentity.StateCancelled {
			return nil, errors.New("task is cancelled")
		}
		executionID, err := a.Scheduler.Trigger(ctx, t, operationID)
		return map[string]any{"executionId": executionID}, err
	}
	switch control {
	case taskentity.ControlPause:
		t.Paused = true
	case taskentity.ControlResume:
		t.Paused = false
	case taskentity.ControlCancel:
		t.Status = "cancelled"
		t.Paused = true
		if err := a.Store.Put(ctx, "schedule-intent", id, scheduleIntent{Task: t, Cancel: true}); err != nil {
			return nil, err
		}
		if err := taskrepository.New(a.Store).SaveTask(ctx, t); err != nil {
			return nil, err
		}
		if err := a.Scheduler.Cancel(ctx, t); err != nil {
			t.Error = err.Error()
			_ = taskrepository.New(a.Store).SaveTask(ctx, t)
			return t, err
		}
		return t, a.Store.Delete(ctx, "schedule-intent", id)
	default:
		return nil, errors.New("unknown task action")
	}
	return a.SaveTaskLocked(ctx, t)
}

func (a *Commands) PauseDependent(ctx context.Context, id, taskID string) error {
	unlock, err := taskrepository.New(a.Store).LockTask(ctx, taskID)
	if err != nil {
		return err
	}
	defer unlock()
	t, err := taskrepository.New(a.Store).Task(ctx, taskID)
	if err != nil {
		return err
	}
	if _, uses := t.Versions[id]; !uses || taskentity.ParseState(t.Status).Terminal() {
		return nil
	}
	archived, err := lifecycleRepository.Archived(ctx, a.Store, lifecycle.ResourceTask, taskID)
	if err != nil || archived {
		return err
	}
	var pending scheduleIntent
	pendingErr := a.Store.Get(ctx, "schedule-intent", t.ID, &pending)
	if pendingErr != nil && !errors.Is(pendingErr, store.ErrNotFound) {
		return pendingErr
	}
	if t.Paused && errors.Is(pendingErr, store.ErrNotFound) {
		return nil
	}
	if a.Scheduler == nil {
		return errors.New("Temporal is not connected; dependency schedule could not be paused")
	}
	if pendingErr == nil && pending.Archive {
		return a.completeArchive(ctx, pending)
	}
	old := t
	t.Paused, t.Status, t.Error = true, taskentity.StatePaused.WireName(), "dependency disabled: "+id
	if err := a.Store.Put(ctx, "schedule-intent", t.ID, scheduleIntent{Task: t, Previous: &old}); err != nil {
		return err
	}
	if err := taskrepository.New(a.Store).SaveTask(ctx, t); err != nil {
		return err
	}
	if err := a.Scheduler.Apply(ctx, t, &old); err != nil {
		return err
	}
	return a.Store.Delete(ctx, "schedule-intent", t.ID)
}

type scheduleIntent struct {
	Task     taskentity.Task  `json:"task"`
	Previous *taskentity.Task `json:"previous,omitempty"`
	Cancel   bool             `json:"cancel"`
	Archive  bool             `json:"archive,omitempty"`
}

func (a *Commands) Reconcile(ctx context.Context) {
	if a.Scheduler == nil {
		return
	}
	intents, err := store.All[scheduleIntent](ctx, a.Store, "schedule-intent")
	if err != nil {
		slog.Error("read schedule intents", "error", err)
		return
	}
	for _, in := range intents {
		if err := a.ReconcileOne(ctx, in); err != nil {
			slog.Warn("schedule reconciliation pending", "taskId", in.Task.ID, "error", err)
		}
	}
}

func (a *Commands) ReconcileOne(ctx context.Context, in scheduleIntent) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	unlock, err := taskrepository.New(a.Store).LockTask(ctx, in.Task.ID)
	if err != nil {
		return err
	}
	defer unlock()
	purged, err := store.Purged(ctx, a.Store, "task", in.Task.ID)
	if err != nil {
		return err
	}
	if purged {
		return a.Store.Delete(ctx, "schedule-intent", in.Task.ID)
	}
	// Re-read under the lock; a new user edit may have replaced this intent.
	if err := a.Store.Get(ctx, "schedule-intent", in.Task.ID, &in); err != nil {
		return err
	}
	current, err := taskrepository.New(a.Store).Task(ctx, in.Task.ID)
	if err != nil {
		return err
	}
	if current.Revision != in.Task.Revision {
		return a.Store.Delete(ctx, "schedule-intent", in.Task.ID)
	}
	if in.Archive {
		return a.completeArchive(ctx, in)
	}
	archived, err := lifecycleRepository.Archived(ctx, a.Store, lifecycle.ResourceTask, in.Task.ID)
	if err != nil {
		return err
	}
	if archived {
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
	if !in.Cancel && taskentity.ParseState(current.Status) != taskentity.StateCompleted && taskentity.ParseState(current.Status) != taskentity.StateFailed {
		current.Status = "active"
		if current.Paused {
			current.Status = "paused"
		}
		current.Error = ""
		if err := taskrepository.New(a.Store).SaveTask(ctx, current); err != nil {
			return err
		}
	}
	return a.Store.Delete(ctx, "schedule-intent", in.Task.ID)
}
