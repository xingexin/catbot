package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

func TestLegacyTaskAndExecutionRecordsRemainReadableAndWritable(t *testing.T) {
	t.Parallel()
	records := store.NewMemory()
	raw := json.RawMessage(`{"id":"legacy","name":"提醒","kind":"recurring","status":"active","revision":7,"configId":"model","personaId":"secretary","sessionId":"qq-existing","steps":[{"id":"remind","kind":"tool","tool":"example__echo"}],"versions":{"example":"example@1"},"notify":true}`)
	if err := records.Put(t.Context(), "task", "legacy", raw); err != nil {
		t.Fatal(err)
	}
	repo := New(records)
	task, err := repo.Task(t.Context(), "legacy")
	if err != nil || task.Revision != 7 || task.PersonaID != "secretary" || task.Versions["example"] != "example@1" {
		t.Fatalf("legacy task lost: %+v %v", task, err)
	}
	task.Paused = true
	if err := repo.SaveTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	var persisted taskentity.Task
	if err := records.Get(t.Context(), "task", "legacy", &persisted); err != nil || !persisted.Paused {
		t.Fatalf("task saved under another key: %+v %v", persisted, err)
	}
	execution := json.RawMessage(`{"id":"task-legacy-r7","taskId":"legacy","status":"running","results":{"remind":{"text":"吃饭"}},"startedAt":"2026-10-07T00:00:00Z"}`)
	if err := records.Put(t.Context(), "execution", "task-legacy-r7", execution); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveExecution(t.Context(), taskentity.TaskExecution{ID: "other", TaskID: "another-task", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	selected, err := repo.Executions(t.Context(), "legacy")
	if err != nil || len(selected) != 1 || selected[0].ID != "task-legacy-r7" {
		t.Fatalf("wrong task occupancy: %+v %v", selected, err)
	}
	x, err := repo.Execution(t.Context(), selected[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	x.Status = "completed"
	if err := repo.SaveExecution(t.Context(), x); err != nil {
		t.Fatal(err)
	}
	if err := records.Get(t.Context(), "execution", x.ID, &x); err != nil || x.Status != "completed" || x.Results["remind"] == nil {
		t.Fatalf("execution key or result changed: %+v %v", x, err)
	}
	if _, err := repo.Task(t.Context(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("not-found identity lost: %v", err)
	}
}

func TestTaskRepositoryUsesTheExistingLockKeyAndCancellation(t *testing.T) {
	t.Parallel()
	records := store.NewMemory()
	unlock, err := records.Lock(t.Context(), "task:legacy")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	released, err := New(records).LockTask(ctx, "legacy")
	if released != nil {
		released()
		t.Fatal("typed repository bypassed existing task lock")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lock ignored cancellation: %v", err)
	}
}
