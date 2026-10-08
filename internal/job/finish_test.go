package job

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xingexin/catbot/internal/domain"
	"github.com/xingexin/catbot/internal/store"
)

type finishHost struct {
	fakeHost
	notifyCalls int
	notifyError error
	onNotify    func()
}

func (h *finishHost) Notify(context.Context, Snapshot, string, string) error {
	h.notifyCalls++
	if h.onNotify != nil {
		h.onNotify()
	}
	return h.notifyError
}

func seedFinish(t *testing.T, s store.Store, task domain.Task) {
	t.Helper()
	if err := s.Put(t.Context(), "task", task.ID, task); err != nil {
		t.Fatal(err)
	}
	x := domain.TaskExecution{ID: "execution", TaskID: task.ID, Status: "running"}
	if err := s.Put(t.Context(), "execution", x.ID, x); err != nil {
		t.Fatal(err)
	}
}

func TestFinishReflectsNotificationOutcomeInOnceTask(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		kind        string
		when        string
		text        string
		notifyError error
		wantStatus  string
		wantError   string
		wantCalls   int
	}{
		{name: "sent", kind: "once", text: "${steps.reminder.text}", wantStatus: "completed", wantCalls: 1},
		{name: "condition false", kind: "once", when: "${steps.reminder.changed}", text: "${steps.reminder.text}", wantStatus: "completed"},
		{name: "render failed", kind: "once", text: "${steps.reminder.missing}", wantStatus: "notification_failed", wantError: "step reference field not found: missing"},
		{name: "send failed", kind: "once", text: "${steps.reminder.text}", notifyError: errors.New("QQ delivery uncertain"), wantStatus: "notification_failed", wantError: "QQ delivery uncertain", wantCalls: 1},
		{name: "recurring render failed", kind: "recurring", text: "${steps.reminder.missing}", wantStatus: "notification_failed", wantError: "step reference field not found: missing"},
		{name: "recurring send failed", kind: "recurring", text: "${steps.reminder.text}", notifyError: errors.New("QQ unavailable"), wantStatus: "notification_failed", wantError: "QQ unavailable", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := store.NewMemory()
			task := domain.Task{ID: "task", Kind: tc.kind, Revision: 1, Status: "active", Notify: true, NotifyWhen: tc.when, NotifyText: tc.text}
			seedFinish(t, s, task)
			h := &finishHost{notifyError: tc.notifyError}
			e := New(nil, s, h)
			result := map[string]any{"reminder": map[string]any{"text": "QQ 群聊定时验收完成", "changed": false}}
			if err := e.Finish(t.Context(), Snapshot{Task: task}, "execution", "completed", "", result); err != nil {
				t.Fatal(err)
			}
			var x domain.TaskExecution
			if err := s.Get(t.Context(), "execution", "execution", &x); err != nil {
				t.Fatal(err)
			}
			if x.Status != tc.wantStatus || x.Error != tc.wantError || x.FinishedAt == nil || x.Results["reminder"] == nil {
				t.Fatalf("execution outcome = %+v", x)
			}
			var current domain.Task
			if err := s.Get(t.Context(), "task", task.ID, &current); err != nil {
				t.Fatal(err)
			}
			wantTaskStatus, wantTaskError := tc.wantStatus, tc.wantError
			if tc.kind == "recurring" {
				wantTaskStatus, wantTaskError = "active", ""
			}
			if current.Status != wantTaskStatus || current.Error != wantTaskError {
				t.Fatalf("task outcome = %+v; want status %q error %q", current, wantTaskStatus, wantTaskError)
			}
			if h.notifyCalls != tc.wantCalls {
				t.Fatalf("Notify calls = %d; want %d", h.notifyCalls, tc.wantCalls)
			}
		})
	}
}

func TestFinishDoesNotOverwriteRevisedOrCancelledTask(t *testing.T) {
	t.Parallel()
	for _, changed := range []string{"revised", "cancelled"} {
		for _, outcome := range []string{"sent", "failed"} {
			t.Run(changed+"/"+outcome, func(t *testing.T) {
				t.Parallel()
				s := store.NewMemory()
				task := domain.Task{ID: "task", Kind: "once", Revision: 1, Status: "active", Notify: true, NotifyText: "reminder"}
				seedFinish(t, s, task)
				current := task
				current.Error = "current configuration"
				if changed == "revised" {
					current.Revision++
				} else {
					current.Status = "cancelled"
				}
				h := &finishHost{onNotify: func() {
					if err := s.Put(t.Context(), "task", task.ID, current); err != nil {
						t.Fatal(err)
					}
				}}
				if outcome == "failed" {
					h.notifyError = errors.New("QQ delivery uncertain")
				}
				if err := New(nil, s, h).Finish(t.Context(), Snapshot{Task: task}, "execution", "completed", "", nil); err != nil {
					t.Fatal(err)
				}
				var got domain.Task
				if err := s.Get(t.Context(), "task", task.ID, &got); err != nil {
					t.Fatal(err)
				}
				if got.Status != current.Status || got.Error != current.Error || got.Revision != current.Revision {
					t.Fatalf("late Finish overwrote current task: %+v", got)
				}
			})
		}
	}
}

type finishFaultStore struct {
	store.Store
	operation string
	kind      string
	fault     error
	skip      int
	remaining int
}

func (s *finishFaultStore) fail(operation, kind string) error {
	if operation == s.operation && kind == s.kind && s.remaining > 0 {
		if s.skip > 0 {
			s.skip--
			return nil
		}
		s.remaining--
		return s.fault
	}
	return nil
}

func TestFinishReportsExecutionPersistenceErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		operation string
		skip      int
		wantCalls int
	}{
		{name: "read execution", operation: "get"},
		{name: "save step outcome", operation: "put"},
		{name: "save notification failure", operation: "put", skip: 1, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			memory := store.NewMemory()
			task := domain.Task{ID: "task", Kind: "once", Revision: 1, Status: "active", Notify: true, NotifyText: "reminder"}
			seedFinish(t, memory, task)
			fault := errors.New("execution storage unavailable")
			s := &finishFaultStore{Store: memory, operation: tc.operation, kind: "execution", fault: fault, skip: tc.skip, remaining: 1}
			h := &finishHost{notifyError: errors.New("QQ delivery uncertain")}
			if err := New(nil, s, h).Finish(t.Context(), Snapshot{Task: task}, "execution", "completed", "", nil); !errors.Is(err, fault) {
				t.Fatalf("Finish error = %v; want storage failure", err)
			}
			if h.notifyCalls != tc.wantCalls {
				t.Fatalf("Notify calls = %d; want %d", h.notifyCalls, tc.wantCalls)
			}
			var current domain.Task
			if err := memory.Get(t.Context(), "task", task.ID, &current); err != nil {
				t.Fatal(err)
			}
			if current.Status != "active" {
				t.Fatalf("task finalized despite execution persistence failure: %+v", current)
			}
		})
	}
}

func (s *finishFaultStore) Get(ctx context.Context, kind, id string, value any) error {
	if err := s.fail("get", kind); err != nil {
		return err
	}
	return s.Store.Get(ctx, kind, id, value)
}

func (s *finishFaultStore) Put(ctx context.Context, kind, id string, value any) error {
	if err := s.fail("put", kind); err != nil {
		return err
	}
	return s.Store.Put(ctx, kind, id, value)
}

func (s *finishFaultStore) Lock(ctx context.Context, key string) (func(), error) {
	if err := s.fail("lock", strings.SplitN(key, ":", 2)[0]); err != nil {
		return nil, err
	}
	return s.Store.Lock(ctx, key)
}

func TestFinishReportsTaskPersistenceErrorsAndRepairsWithoutResending(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"get", "put", "lock"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			memory := store.NewMemory()
			task := domain.Task{ID: "task", Kind: "once", Revision: 1, Status: "active", Notify: true, NotifyText: "reminder"}
			seedFinish(t, memory, task)
			fault := errors.New("task storage unavailable")
			s := &finishFaultStore{Store: memory, operation: operation, kind: "task", fault: fault, remaining: 1}
			h := &finishHost{notifyError: errors.New("QQ delivery uncertain")}
			e := New(nil, s, h)
			snapshot := Snapshot{Task: task}
			if err := e.Finish(t.Context(), snapshot, "execution", "completed", "", nil); !errors.Is(err, fault) {
				t.Fatalf("Finish error = %v; want storage failure", err)
			}
			if err := e.Finish(t.Context(), snapshot, "execution", "completed", "", nil); err != nil {
				t.Fatal(err)
			}
			var current domain.Task
			if err := s.Get(t.Context(), "task", task.ID, &current); err != nil {
				t.Fatal(err)
			}
			if current.Status != "notification_failed" || current.Error != h.notifyError.Error() {
				t.Fatalf("task status was not repaired: %+v", current)
			}
			if h.notifyCalls != 1 {
				t.Fatalf("persistence retry repeated uncertain notification: %d calls", h.notifyCalls)
			}
		})
	}
}
