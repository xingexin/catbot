package temporal

import (
	"context"
	"errors"
	"strings"
	"testing"

	taskbiz "github.com/xingexin/catbot/internal/biz/task"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"

	"go.temporal.io/sdk/testsuite"
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
func TestWorkflowPersistsStepsAndNeverRetriesAmbiguousAgent(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "interruption"}[failure], func(t *testing.T) {
			s := store.NewMemory()
			h := &fakeHost{fail: failure}
			e := &taskbiz.ExecutionHost{Store: s, Host: h}
			task := taskentity.Task{ID: "task", Kind: "manual", Status: "active", Revision: 1, ConfigID: "config", PersonaID: "persona", Notify: true, Steps: []taskentity.Step{{ID: "first", Kind: "tool"}, {ID: "second", Kind: "agent"}}}
			for kind, value := range map[string]any{"task": task, "config": agent.Config{ID: "config"}, "persona": persona.Persona{ID: "persona"}} {
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
			Register(env, e)
			env.ExecuteWorkflow(TaskWorkflow, taskentity.Input{TaskID: task.ID, Revision: 1, Manual: true})
			err := env.GetWorkflowError()
			if failure && err == nil || !failure && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(h.steps, ",") != "first,second" {
				t.Fatalf("steps replayed: %v", h.steps)
			}
			xs, err := store.All[taskentity.TaskExecution](t.Context(), s, "execution")
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
