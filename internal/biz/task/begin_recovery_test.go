package task

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

type beginFaultStore struct {
	store.Store
	kind      string
	remaining atomic.Int32
}

func (s *beginFaultStore) Put(ctx context.Context, kind, id string, value any) error {
	if kind == s.kind && s.remaining.Add(-1) >= 0 {
		return errors.New("injected begin persistence failure")
	}
	return s.Store.Put(ctx, kind, id, value)
}

func seedBegin(t *testing.T, s store.Store) taskentity.Task {
	t.Helper()
	task := taskentity.Task{ID: "recover-task", Kind: "recurring", Status: "active", Revision: 1, ConfigID: "config", PersonaID: "persona", Steps: []taskentity.Step{{ID: "work", Kind: "tool"}}}
	for _, entry := range []struct {
		kind, id string
		value    any
	}{
		{"task", task.ID, task},
		{"config", "config", agent.Config{ID: "config", Model: "original"}},
		{"persona", "persona", persona.Persona{ID: "persona", Name: "original"}},
	} {
		if err := s.Put(t.Context(), entry.kind, entry.id, entry.value); err != nil {
			t.Fatal(err)
		}
	}
	return task
}

func TestBeginRetryKeepsSnapshotAndRepairsMissingExecution(t *testing.T) {
	s := &beginFaultStore{Store: store.NewMemory(), kind: "execution"}
	s.remaining.Store(1)
	task := seedBegin(t, s)
	engine := newTestExecution(s, &fakeHost{})
	in := taskentity.Input{TaskID: task.ID, Revision: task.Revision}
	if _, err := engine.Begin(t.Context(), in, "same-execution"); err == nil {
		t.Fatal("injected execution persistence failure was hidden")
	}
	if err := s.Put(t.Context(), "persona", "persona", persona.Persona{ID: "persona", Name: "updated-after-start"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.Begin(t.Context(), in, "same-execution")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Persona.Name != "original" {
		t.Fatal("retry switched to a new persona instead of the saved snapshot")
	}
	var execution taskentity.TaskExecution
	if err := s.Get(t.Context(), "execution", "same-execution", &execution); err != nil || execution.Status != "running" || execution.Persona == nil || execution.Persona.Name != "original" {
		t.Fatalf("execution was not repaired from its snapshot: %+v %v", execution, err)
	}
}

func TestBeginSnapshotRetryStillHonorsNewOccupancyAndCancellation(t *testing.T) {
	for _, tc := range []string{"another execution started", "task cancelled", "task paused", "task superseded"} {
		t.Run(tc, func(t *testing.T) {
			s := &beginFaultStore{Store: store.NewMemory(), kind: "execution"}
			s.remaining.Store(1)
			task := seedBegin(t, s)
			engine := newTestExecution(s, &fakeHost{})
			in := taskentity.Input{TaskID: task.ID, Revision: task.Revision}
			if _, err := engine.Begin(t.Context(), in, "incomplete-begin"); err == nil {
				t.Fatal("injected execution write failure was hidden")
			}
			switch tc {
			case "another execution started":
				if _, err := engine.Begin(t.Context(), in, "newer-execution"); err != nil {
					t.Fatal(err)
				}
			case "task cancelled":
				task.Status = "cancelled"
			case "task paused":
				task.Paused = true
			case "task superseded":
				task.Revision++
			}
			if err := s.Put(t.Context(), "task", task.ID, task); err != nil {
				t.Fatal(err)
			}
			if _, err := engine.Begin(t.Context(), in, "incomplete-begin"); err == nil {
				t.Fatal("saved snapshot bypassed the current task state")
			}
			var abandoned taskentity.TaskExecution
			if err := s.Get(t.Context(), "execution", "incomplete-begin", &abandoned); err != nil || abandoned.Status != "skipped" {
				t.Fatalf("abandoned begin was not visible: %+v %v", abandoned, err)
			}
		})
	}
}
