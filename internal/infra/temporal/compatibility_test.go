package temporal

import (
	"context"
	"encoding/json"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"testing"
	"time"
)

type capturedStart struct {
	client.Client
	options  client.StartWorkflowOptions
	workflow any
	args     []any
}

func (c *capturedStart) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error) {
	c.options, c.workflow, c.args = options, workflow, args
	return nil, nil
}
func TestScheduledAndManualStartsRetainPersistedContracts(t *testing.T) {
	t.Parallel()
	c := &capturedStart{}
	scheduler := New(c, store.NewMemory())
	if scheduler.TaskQueue != "secretary-tasks" {
		t.Fatalf("worker queue is empty or incompatible: %q", scheduler.TaskQueue)
	}
	at := time.Now().Add(time.Minute)
	task := taskentity.Task{ID: "legacy", Kind: "once", Revision: 7, RunAt: &at}
	if err := scheduler.Apply(t.Context(), task, nil); err != nil {
		t.Fatal(err)
	}
	if c.options.ID != "task-legacy-r7" || c.options.TaskQueue != "secretary-tasks" || c.workflow != "TaskWorkflow" || c.options.StartDelay <= 0 || c.options.WorkflowIDReusePolicy != enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE {
		t.Fatalf("one-time workflow contract changed: %+v %v", c.options, c.workflow)
	}
	payload, err := json.Marshal(c.args[0])
	if err != nil || string(payload) != `{"TaskID":"legacy","Revision":7,"Manual":false}` {
		t.Fatalf("existing history input changed: %s %v", payload, err)
	}
	id, err := scheduler.Trigger(t.Context(), task, "op-legacy")
	if err != nil || id != "manual-legacy-4f65e6d52fac8ac9a2878697" || c.options.ID != id || c.options.TaskQueue != "secretary-tasks" || c.workflow != "TaskWorkflow" {
		t.Fatalf("manual operation identity changed: %s %v", id, err)
	}
	payload, err = json.Marshal(c.args[0])
	if err != nil || string(payload) != `{"TaskID":"legacy","Revision":7,"Manual":true}` {
		t.Fatalf("manual history input changed: %s %v", payload, err)
	}
	scheduler.TaskQueue = "isolated-tests"
	if _, err := scheduler.Trigger(t.Context(), task, "different-operation"); err != nil || c.options.TaskQueue != "isolated-tests" {
		t.Fatalf("queue override lost: %s %v", c.options.TaskQueue, err)
	}
}
