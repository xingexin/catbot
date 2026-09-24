package job

import (
	"context"
	"errors"
	"strings"
	"testing"

	"agentTest/internal/domain"
	"agentTest/internal/store"
	"go.temporal.io/sdk/testsuite"
)

type fakeHost struct {
	steps    []string
	fail     bool
	notified bool
}

func (f *fakeHost) Step(_ context.Context, in StepInput) (any, error) {
	f.steps = append(f.steps, in.Step.ID)
	if f.fail && in.Step.ID == "second" {
		return nil, errors.New("uncertain SDK interruption")
	}
	return map[string]any{"value": in.Step.ID}, nil
}
func (f *fakeHost) Notify(context.Context, Snapshot, string, string) error {
	f.notified = true
	return nil
}
func TestWorkflowPersistsStepsAndNeverRetriesAmbiguousAgent(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "interruption"}[failure], func(t *testing.T) {
			s := store.NewMemory()
			h := &fakeHost{fail: failure}
			e := New(nil, s, h)
			task := domain.Task{ID: "task", Kind: "manual", Status: "active", Revision: 1, ConfigID: "config", PersonaID: "persona", Notify: true, Steps: []domain.Step{{ID: "first", Kind: "tool"}, {ID: "second", Kind: "agent"}}}
			for kind, value := range map[string]any{"task": task, "config": domain.Config{ID: "config"}, "persona": domain.Persona{ID: "persona"}} {
				id := kind
				if kind == "task" {
					id = task.ID
				}
				if err := s.Put(t.Context(), kind, id, value); err != nil {
					t.Fatal(err)
				}
			}
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.RegisterWorkflow(TaskWorkflow)
			env.RegisterActivity(e.Begin)
			env.RegisterActivity(e.ExecuteStep)
			env.RegisterActivity(e.Finish)
			env.ExecuteWorkflow(TaskWorkflow, Input{TaskID: task.ID, Revision: 1, Manual: true})
			err := env.GetWorkflowError()
			if failure && err == nil || !failure && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(h.steps, ",") != "first,second" {
				t.Fatalf("steps replayed: %v", h.steps)
			}
			xs, err := store.All[domain.TaskExecution](t.Context(), s, "execution")
			if err != nil || len(xs) != 1 {
				t.Fatal(xs, err)
			}
			if xs[0].Results["first"] == nil {
				t.Fatal("completed step result lost")
			}
			if !h.notified {
				t.Fatal("no notification")
			}
		})
	}
}
func TestTypedTaskReferences(t *testing.T) {
	result, err := Resolve(map[string]any{"id": "${steps.read.id}", "literal": "hello"}, map[string]any{"read": map[string]any{"id": 42}})
	if err != nil || result.(map[string]any)["id"] != 42 {
		t.Fatal(result, err)
	}
	if _, err := Resolve("${steps.absent.id}", map[string]any{}); err == nil {
		t.Fatal("missing reference accepted")
	}
}
func TestBeginSkipsOverlapAndCancelledTasks(t *testing.T) {
	s := store.NewMemory()
	e := New(nil, s, &fakeHost{})
	task := domain.Task{ID: "t", Revision: 1, Status: "active", ConfigID: "c", PersonaID: "p"}
	_ = s.Put(t.Context(), "task", "t", task)
	_ = s.Put(t.Context(), "config", "c", domain.Config{})
	_ = s.Put(t.Context(), "persona", "p", domain.Persona{})
	if _, err := e.Begin(t.Context(), Input{TaskID: "t", Revision: 1}, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Begin(t.Context(), Input{TaskID: "t", Revision: 1, Manual: true}, "two"); err == nil {
		t.Fatal("overlap allowed")
	}
	task.Status = "cancelled"
	_ = s.Put(t.Context(), "task", "t", task)
	if _, err := e.Begin(t.Context(), Input{TaskID: "t", Revision: 1}, "three"); err == nil {
		t.Fatal("cancelled task started")
	}
}
