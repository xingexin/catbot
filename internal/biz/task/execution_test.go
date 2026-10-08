package task

import (
	"context"
	"errors"
	"testing"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

type fakeHost struct {
	steps    []string
	fail     bool
	notified bool
}

func (f *fakeHost) Step(_ context.Context, in taskentity.StepInput) (any, error) {
	f.steps = append(f.steps, in.Step.ID)
	if f.fail && in.Step.ID == "second" {
		return nil, errors.New("uncertain SDK interruption")
	}
	return map[string]any{"value": in.Step.ID}, nil
}
func (f *fakeHost) Notify(context.Context, taskentity.Snapshot, string, string) error {
	f.notified = true
	return nil
}
func newTestExecution(s store.Store, h Host) *ExecutionHost {
	return &ExecutionHost{Store: s, Host: h}
}
func TestBeginSkipsOverlapAndCancelledTasks(t *testing.T) {
	s := store.NewMemory()
	e := newTestExecution(s, &fakeHost{})
	task := taskentity.Task{ID: "t", Revision: 1, Status: "active", ConfigID: "c", PersonaID: "p"}
	_ = s.Put(t.Context(), "task", "t", task)
	_ = s.Put(t.Context(), "config", "c", agent.Config{})
	_ = s.Put(t.Context(), "persona", "p", persona.Persona{})
	if _, err := e.Begin(t.Context(), taskentity.Input{TaskID: "t", Revision: 1}, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Begin(t.Context(), taskentity.Input{TaskID: "t", Revision: 1, Manual: true}, "two"); err == nil {
		t.Fatal("overlap allowed")
	}
	task.Status = "cancelled"
	_ = s.Put(t.Context(), "task", "t", task)
	if _, err := e.Begin(t.Context(), taskentity.Input{TaskID: "t", Revision: 1}, "three"); err == nil {
		t.Fatal("cancelled task started")
	}
}
