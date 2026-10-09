package task

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

type lifecycleScheduler struct {
	cancelled atomic.Int32
	triggered atomic.Int32
	applied   atomic.Int32
	cancelErr error
}

func (s *lifecycleScheduler) Apply(context.Context, taskentity.Task, *taskentity.Task) error {
	s.applied.Add(1)
	return nil
}
func (s *lifecycleScheduler) Trigger(context.Context, taskentity.Task, string) (string, error) {
	s.triggered.Add(1)
	return "trigger", nil
}
func (s *lifecycleScheduler) Cancel(context.Context, taskentity.Task) error {
	s.cancelled.Add(1)
	return s.cancelErr
}
func (s *lifecycleScheduler) Ping(context.Context) error { return nil }

func lifecycleTask(t *testing.T, s store.Store) taskentity.Task {
	t.Helper()
	task := taskentity.Task{ID: "task", Name: "提醒", Kind: "recurring", Status: "active", Revision: 2, ConfigID: "config", PersonaID: "persona"}
	for _, record := range []struct {
		kind, id string
		value    any
	}{{"task", task.ID, task}, {"config", "config", agent.Config{ID: "config"}}, {"persona", "persona", persona.Persona{ID: "persona"}}} {
		if err := s.Put(t.Context(), record.kind, record.id, record.value); err != nil {
			t.Fatal(err)
		}
	}
	return task
}

func TestTaskArchiveStopsSchedulerAndRestoreRemainsPaused(t *testing.T) {
	s := store.NewMemory()
	task := lifecycleTask(t, s)
	scheduler := &lifecycleScheduler{}
	commands := &Commands{Store: s, Scheduler: scheduler}
	for range 2 {
		if err := commands.Archive(t.Context(), task.ID); err != nil {
			t.Fatal(err)
		}
	}
	if scheduler.cancelled.Load() != 1 {
		t.Fatal("archive repeated cancellation")
	}
	archived, err := lifecycleRepository.Archived(t.Context(), s, lifecycle.ResourceTask, task.ID)
	if err != nil || !archived {
		t.Fatalf("archive=%v err=%v", archived, err)
	}
	if _, err := commands.ControlTask(t.Context(), task.ID, "trigger"); !errors.Is(err, lifecycleRepository.ErrArchived) {
		t.Fatalf("archived trigger accepted: %v", err)
	}
	if _, err := commands.SaveTask(t.Context(), task); !errors.Is(err, lifecycleRepository.ErrArchived) {
		t.Fatalf("archived edit accepted: %v", err)
	}
	if err := commands.Restore(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	var restored taskentity.Task
	if err := s.Get(t.Context(), "task", task.ID, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Paused || taskentity.ParseState(restored.Status) != taskentity.StatePaused || restored.Revision <= task.Revision {
		t.Fatalf("unsafe restored task: %+v", restored)
	}
	if scheduler.applied.Load() != 0 || scheduler.triggered.Load() != 0 {
		t.Fatal("restore automatically restarted work")
	}
	if _, err := newTestExecution(s, &fakeHost{}).Begin(t.Context(), taskentity.Input{TaskID: task.ID, Revision: task.Revision, Manual: true}, "stale"); err == nil {
		t.Fatal("stale queued manual execution resumed after restore")
	}
}

func TestTaskArchiveCancellationFailureIsDurableAndBlocksNewWork(t *testing.T) {
	s := store.NewMemory()
	task := lifecycleTask(t, s)
	failure := errors.New("Temporal unavailable")
	scheduler := &lifecycleScheduler{cancelErr: failure}
	commands := &Commands{Store: s, Scheduler: scheduler}
	if err := commands.Archive(t.Context(), task.ID); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	archived, _ := lifecycleRepository.Archived(t.Context(), s, lifecycle.ResourceTask, task.ID)
	if archived {
		t.Fatal("failed cancellation presented as archived")
	}
	if _, err := commands.ControlTask(t.Context(), task.ID, "trigger"); err == nil {
		t.Fatal("pending archive allowed trigger")
	}
	if _, err := newTestExecution(s, &fakeHost{}).Begin(t.Context(), taskentity.Input{TaskID: task.ID, Revision: task.Revision, Manual: true}, "late"); err == nil {
		t.Fatal("pending archive allowed delayed manual begin")
	}
	var intent scheduleIntent
	if err := s.Get(t.Context(), "schedule-intent", task.ID, &intent); err != nil || !intent.Archive {
		t.Fatalf("archive intent missing: %+v %v", intent, err)
	}
	scheduler.cancelErr = nil
	if err := commands.ReconcileOne(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	archived, _ = lifecycleRepository.Archived(t.Context(), s, lifecycle.ResourceTask, task.ID)
	if !archived || scheduler.applied.Load() != 0 {
		t.Fatal("recovery re-created rather than stopped schedule")
	}
	if err := s.Get(t.Context(), "schedule-intent", task.ID, &intent); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("intent not consumed", err)
	}
}

func TestTaskLifecycleRejectsActiveExecutionAndPendingDelivery(t *testing.T) {
	for _, kind := range []string{"execution", "notification"} {
		t.Run(kind, func(t *testing.T) {
			s := store.NewMemory()
			task := lifecycleTask(t, s)
			scheduler := &lifecycleScheduler{}
			commands := &Commands{Store: s, Scheduler: scheduler}
			var value any = taskentity.TaskExecution{ID: "busy", TaskID: task.ID, Status: "running"}
			if kind == "notification" {
				value = messaging.Notification{ID: "busy", TaskID: task.ID, Status: "pending"}
			}
			if err := s.Put(t.Context(), kind, "busy", value); err != nil {
				t.Fatal(err)
			}
			if err := commands.Archive(t.Context(), task.ID); err == nil {
				t.Fatal("busy task archived")
			}
			if scheduler.cancelled.Load() != 0 {
				t.Fatal("busy execution cancelled by archive")
			}
		})
	}
}

func TestTaskPurgePreservesHistoryAndRejectsLateReplay(t *testing.T) {
	s := store.NewMemory()
	task := lifecycleTask(t, s)
	scheduler := &lifecycleScheduler{}
	commands := &Commands{Store: s, Scheduler: scheduler}
	finished := taskentity.TaskExecution{ID: "finished", TaskID: task.ID, Status: "completed"}
	if err := s.Put(t.Context(), "execution", finished.ID, finished); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(t.Context(), "operation", "finished:side-effect", map[string]any{"status": "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := commands.Purge(t.Context(), task.ID); err == nil {
		t.Fatal("unarchived task deleted")
	}
	if err := commands.Archive(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := commands.Purge(t.Context(), task.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := commands.SaveTask(t.Context(), task); !errors.Is(err, lifecycleRepository.ErrPurged) {
		t.Fatalf("purged task recreated: %v", err)
	}
	if err := s.Put(t.Context(), "task", task.ID, task); !errors.Is(err, store.ErrPurgedRecord) {
		t.Fatal("store accepted stale task write", err)
	}
	var history any
	for _, ref := range []store.RecordRef{{Kind: "execution", ID: finished.ID}, {Kind: "operation", ID: "finished:side-effect"}} {
		if err := s.Get(t.Context(), ref.Kind, ref.ID, &history); err != nil {
			t.Fatal("idempotency history lost", err)
		}
	}
	if _, err := newTestExecution(s, &fakeHost{}).Begin(t.Context(), taskentity.Input{TaskID: task.ID, Revision: task.Revision}, finished.ID); err == nil {
		t.Fatal("deleted task replayed")
	}
}

func TestPurgedExecutionCannotRecreateSnapshotOrRepeatStep(t *testing.T) {
	s := store.NewMemory()
	task := lifecycleTask(t, s)
	host := &fakeHost{}
	engine := newTestExecution(s, host)
	if err := store.Purge(t.Context(), s, []store.RecordRef{{Kind: "execution", ID: "gone"}, {Kind: "execution-snapshot", ID: "gone"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Begin(t.Context(), taskentity.Input{TaskID: task.ID, Revision: task.Revision}, "gone"); err == nil {
		t.Fatal("purged execution began again")
	}
	if _, err := engine.ExecuteStep(t.Context(), taskentity.StepInput{ExecutionID: "gone", Snapshot: taskentity.Snapshot{Task: task}, Step: taskentity.Step{ID: "side-effect"}}); err == nil {
		t.Fatal("purged step executed")
	}
	if len(host.steps) != 0 {
		t.Fatal("side effect repeated")
	}
}

func TestTaskArchiveWaitsUntilFinishNotificationSettles(t *testing.T) {
	s := store.NewMemory()
	task := lifecycleTask(t, s)
	task.Kind = "once"
	task.Notify = true
	task.NotifyText = "test"
	seedFinish(t, s, task)
	entered, release := make(chan struct{}), make(chan struct{})
	host := &finishHost{onNotify: func() { close(entered); <-release }}
	engine := newTestExecution(s, host)
	commands := &Commands{Store: s, Scheduler: &lifecycleScheduler{}}
	finishDone := make(chan error, 1)
	archiveDone := make(chan error, 1)
	go func() {
		finishDone <- engine.Finish(t.Context(), taskentity.Snapshot{Task: task}, "execution", "completed", "", nil)
	}()
	<-entered
	go func() { archiveDone <- commands.Archive(t.Context(), task.ID) }()
	select {
	case err := <-archiveDone:
		close(release)
		t.Fatalf("archive raced ahead of notification: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-finishDone; err != nil {
		t.Fatal(err)
	}
	if err := <-archiveDone; err != nil {
		t.Fatal(err)
	}
}

func TestActiveTaskCannotArchiveWithoutScheduler(t *testing.T) {
	s := store.NewMemory()
	task := lifecycleTask(t, s)
	commands := &Commands{Store: s}
	if err := commands.Archive(t.Context(), task.ID); err == nil {
		t.Fatal("active task archived without confirming scheduler stop")
	}
	task.Kind = "once"
	task.Status = "completed"
	if err := s.Put(t.Context(), "task", task.ID, task); err != nil {
		t.Fatal(err)
	}
	if err := commands.Archive(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := commands.Purge(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTaskArchiveCancelsBothSidesOfIncompleteScheduleEdit(t *testing.T) {
	s := store.NewMemory()
	task := lifecycleTask(t, s)
	previous := task
	previous.Revision--
	previous.Kind = "once"
	scheduler := &lifecycleScheduler{}
	commands := &Commands{Store: s, Scheduler: scheduler}
	if err := s.Put(t.Context(), "schedule-intent", task.ID, scheduleIntent{Task: task, Previous: &previous}); err != nil {
		t.Fatal(err)
	}
	if err := commands.Archive(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if scheduler.cancelled.Load() != 2 {
		t.Fatal("previous delayed workflow or current recurring schedule was left installed")
	}
	if scheduler.applied.Load() != 0 {
		t.Fatal("archive re-applied an unfinished edit")
	}
}

func TestTaskLifecycleRejectsUncertainExecutionAndNotification(t *testing.T) {
	for _, kind := range []string{"execution", "notification"} {
		t.Run(kind, func(t *testing.T) {
			s := store.NewMemory()
			task := lifecycleTask(t, s)
			commands := &Commands{Store: s, Scheduler: &lifecycleScheduler{}}
			if err := s.Put(t.Context(), kind, "uncertain", map[string]any{"id": "uncertain", "taskId": task.ID, "status": "uncertain"}); err != nil {
				t.Fatal(err)
			}
			if err := commands.Archive(t.Context(), task.ID); err == nil {
				t.Fatal("uncertain external outcome accepted as settled")
			}
			if err := lifecycleRepository.Mark(t.Context(), s, lifecycle.ArchiveRecord{Resource: lifecycle.ResourceTask, RecordID: task.ID, Name: task.Name, ArchivedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			if err := commands.Purge(t.Context(), task.ID); err == nil {
				t.Fatal("uncertain external outcome permanently deleted")
			}
		})
	}
}
