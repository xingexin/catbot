package task

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/messaging"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	taskrepository "github.com/xingexin/catbot/internal/domain/task/repository"
	"github.com/xingexin/catbot/internal/infra/store"
)

// Archive stops the actual scheduler before exposing the task as archived.
// The durable intent also prevents manual triggering while cancellation retries.
func (a *Commands) Archive(ctx context.Context, id string) error {
	unlock, err := taskrepository.New(a.Store).LockTask(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	t, err := taskrepository.New(a.Store).Task(ctx, id)
	if err != nil {
		return err
	}
	archived, err := lifecycleRepository.Archived(ctx, a.Store, lifecycle.ResourceTask, id)
	if err != nil || archived {
		return err
	}
	if taskentity.ParseState(t.Status) == taskentity.StateUnknown {
		return errors.New("未知任务状态，不能安全归档或删除")
	}
	if err := a.requireIdleTask(ctx, id); err != nil {
		return err
	}
	var existing scheduleIntent
	intentErr := a.Store.Get(ctx, "schedule-intent", id, &existing)
	if intentErr != nil && !errors.Is(intentErr, store.ErrNotFound) {
		return intentErr
	}
	if a.Scheduler == nil && (!taskentity.ParseState(t.Status).Terminal() || intentErr == nil) {
		return errors.New("Temporal 未连接，无法确认任务调度已停止")
	}
	t.Paused = true
	in := scheduleIntent{Task: t, Previous: existing.Previous, Cancel: true, Archive: true}
	if err := a.Store.Put(ctx, "schedule-intent", id, in); err != nil {
		return err
	}
	if err := taskrepository.New(a.Store).SaveTask(ctx, t); err != nil {
		return err
	}
	return a.completeArchive(ctx, in)
}

// completeArchive is shared by direct requests and durable reconciliation.
// Callers hold the task lock. Revision is retained until Restore so cancellation
// still addresses the original delayed Workflow ID after a crash.
func (a *Commands) completeArchive(ctx context.Context, in scheduleIntent) error {
	t := in.Task
	if a.Scheduler != nil {
		// A failed edit can leave either the previous or current schedule installed.
		// Retain and cancel both identities before consuming that durable intent.
		if in.Previous != nil {
			if err := a.Scheduler.Cancel(ctx, *in.Previous); err != nil {
				return err
			}
		}
		if err := a.Scheduler.Cancel(ctx, t); err != nil {
			return err
		}
	} else if !taskentity.ParseState(t.Status).Terminal() {
		return errors.New("Temporal 未连接，无法确认任务调度已停止")
	}
	t.Paused, t.Status, t.Error = true, taskentity.StatePaused.WireName(), ""
	if err := taskrepository.New(a.Store).SaveTask(ctx, t); err != nil {
		return err
	}
	if err := lifecycleRepository.Mark(ctx, a.Store, lifecycle.ArchiveRecord{Resource: lifecycle.ResourceTask, RecordID: t.ID, Name: t.Name, ArchivedAt: time.Now().UTC()}); err != nil {
		return err
	}
	return a.Store.Delete(ctx, "schedule-intent", t.ID)
}

func (a *Commands) Restore(ctx context.Context, id string) error {
	unlock, err := taskrepository.New(a.Store).LockTask(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	referenceUnlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return err
	}
	defer referenceUnlock()
	t, err := taskrepository.New(a.Store).Task(ctx, id)
	if err != nil {
		return err
	}
	archived, err := lifecycleRepository.Archived(ctx, a.Store, lifecycle.ResourceTask, id)
	if err != nil || !archived {
		return err
	}
	for resource, reference := range map[lifecycle.Resource]string{lifecycle.ResourceConfig: t.ConfigID, lifecycle.ResourcePersona: t.PersonaID, lifecycle.ResourceSession: t.SessionID} {
		if reference == "" {
			continue
		}
		if err := lifecycleRepository.RequireActive(ctx, a.Store, resource, reference); err != nil {
			return fmt.Errorf("恢复任务关联记录: %w", err)
		}
		var value any
		if err := a.Store.Get(ctx, resource.StorageKind(), reference, &value); err != nil {
			return fmt.Errorf("恢复任务关联记录: %w", err)
		}
	}
	t.Paused, t.Status, t.Error = true, taskentity.StatePaused.WireName(), ""
	t.Revision++
	if err := taskrepository.New(a.Store).SaveTask(ctx, t); err != nil {
		return err
	}
	if err := a.Store.Delete(ctx, "schedule-intent", id); err != nil {
		return err
	}
	return lifecycleRepository.Unmark(ctx, a.Store, lifecycle.ResourceTask, id)
}

func (a *Commands) Purge(ctx context.Context, id string) error {
	unlock, err := taskrepository.New(a.Store).LockTask(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	purged, err := store.Purged(ctx, a.Store, "task", id)
	if err != nil || purged {
		return err
	}
	archived, err := lifecycleRepository.Archived(ctx, a.Store, lifecycle.ResourceTask, id)
	if err != nil {
		return err
	}
	if !archived {
		return errors.New("请先归档任务，再永久删除")
	}
	t, err := taskrepository.New(a.Store).Task(ctx, id)
	if err != nil {
		return err
	}
	if taskentity.ParseState(t.Status) == taskentity.StateUnknown {
		return errors.New("未知任务状态，不能安全归档或删除")
	}
	if err := a.requireIdleTask(ctx, id); err != nil {
		return err
	}
	// Archive already confirmed cancellation. Recheck when connected; offline
	// purging is safe only when no reconciliation intent is outstanding.
	var pending scheduleIntent
	intentErr := a.Store.Get(ctx, "schedule-intent", id, &pending)
	if intentErr != nil && !errors.Is(intentErr, store.ErrNotFound) {
		return intentErr
	}
	if a.Scheduler != nil {
		if err := a.Scheduler.Cancel(ctx, t); err != nil {
			return err
		}
	} else if intentErr == nil {
		return errors.New("Temporal 未连接，任务仍有待处理的调度操作")
	}
	return store.Purge(ctx, a.Store, []store.RecordRef{
		{Kind: "task", ID: id}, {Kind: "schedule-intent", ID: id}, {Kind: "notification-incident", ID: id},
		{Kind: lifecycle.ArchiveStorageKind, ID: lifecycle.ArchiveKey(lifecycle.ResourceTask, id), Reusable: true},
	}, nil)
}

func (a *Commands) requireIdleTask(ctx context.Context, id string) error {
	executions, err := taskrepository.New(a.Store).Executions(ctx, id)
	if err != nil {
		return err
	}
	for _, execution := range executions {
		state := lifecycle.ParseActivityState(execution.Status)
		if state == lifecycle.ActivityUnknown || state == lifecycle.ActivityUncertain || state.Mutable() {
			return errors.New("任务仍在执行，请结束后再操作")
		}
	}
	notifications, err := store.All[messaging.Notification](ctx, a.Store, "notification")
	if err != nil {
		return err
	}
	for _, notification := range notifications {
		if notification.TaskID != id {
			continue
		}
		state := lifecycle.ParseActivityState(notification.Status)
		if state == lifecycle.ActivityUnknown || state == lifecycle.ActivityUncertain || state.Mutable() {
			return errors.New("任务仍有待投递通知，请处理后再操作")
		}
	}
	return nil
}

func requireTaskActive(ctx context.Context, s store.Store, id string) error {
	if err := lifecycleRepository.RequireActive(ctx, s, lifecycle.ResourceTask, id); err != nil {
		return err
	}
	var intent scheduleIntent
	err := s.Get(ctx, "schedule-intent", id, &intent)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if intent.Archive {
		return errors.New("任务正在归档，等待停止调度")
	}
	return nil
}

func requireExecutionActive(ctx context.Context, s store.Store, id string) error {
	if err := lifecycleRepository.RequireActive(ctx, s, lifecycle.ResourceExecution, id); err != nil {
		return taskentity.NewExecutionError(err.Error(), "InactiveTask", err)
	}
	return nil
}
