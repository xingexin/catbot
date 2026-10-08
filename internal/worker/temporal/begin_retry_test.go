package temporal

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	taskbiz "github.com/xingexin/catbot/internal/biz/task"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"

	"go.temporal.io/sdk/testsuite"
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

func TestBeginSnapshotFailureDoesNotPermanentlyBlockRecurringTask(t *testing.T) {
	s := &beginFaultStore{Store: store.NewMemory(), kind: "execution-snapshot"}
	s.remaining.Store(3)
	task := seedBegin(t, s)
	host := &fakeHost{}
	engine := &taskbiz.ExecutionHost{Store: s, Host: host}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	Register(env, engine)
	env.ExecuteWorkflow(TaskWorkflow, taskentity.Input{TaskID: task.ID, Revision: task.Revision})
	if env.GetWorkflowError() == nil {
		t.Fatal("injected Begin failure was hidden")
	}
	if len(host.steps) != 0 {
		t.Fatal("work executed despite a failed Begin")
	}
	if _, err := engine.Begin(t.Context(), taskentity.Input{TaskID: task.ID, Revision: task.Revision}, "next-poll"); err != nil {
		t.Fatalf("recovered storage left a permanent overlap blocker: %v", err)
	}
	executions, err := store.All[taskentity.TaskExecution](t.Context(), s, "execution")
	if err != nil {
		t.Fatal(err)
	}
	for _, execution := range executions {
		if execution.ID != "next-poll" && execution.Status == "running" {
			t.Fatalf("failed Begin left a running placeholder: %+v", execution)
		}
	}
}
