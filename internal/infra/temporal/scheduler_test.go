package temporal

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	taskbiz "github.com/xingexin/catbot/internal/biz/task"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

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

type beginDescribeClient struct {
	client.Client
	status enumspb.WorkflowExecutionStatus
	err    error
}

func (c beginDescribeClient) DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Status: c.status}}, nil
}

func TestBeginReconcilesOnlyConfirmedClosedWorkflowOccupancy(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		status  enumspb.WorkflowExecutionStatus
		err     error
		release bool
	}{
		{"failed", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, nil, true},
		{"completed", enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, nil, true},
		{"cancelled", enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, nil, true},
		{"terminated", enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, nil, true},
		{"timed out", enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT, nil, true},
		{"retention expired", enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED, serviceerror.NewNotFound("execution not found"), true},
		{"still running", enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, nil, false},
		{"continued", enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW, nil, false},
		{"unknown status", enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED, nil, false},
		{"unreachable Temporal", enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED, errors.New("Temporal unavailable"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := store.NewMemory()
			task := seedBegin(t, s)
			previous := taskentity.TaskExecution{ID: "old-execution", TaskID: task.ID, Status: "running", StartedAt: time.Now().Add(-time.Hour)}
			if err := s.Put(t.Context(), "execution", previous.ID, previous); err != nil {
				t.Fatal(err)
			}
			host := &integrationHost{}
			scheduler := New(beginDescribeClient{status: tt.status, err: tt.err}, s)
			engine := &taskbiz.ExecutionHost{Store: s, Host: host, ExecutionClosed: scheduler.ExecutionClosed}
			_, err := engine.Begin(t.Context(), taskentity.Input{TaskID: task.ID, Revision: task.Revision}, "new-execution")
			if (err == nil) != tt.release {
				t.Fatalf("release=%v error=%v", tt.release, err)
			}
			if err := s.Get(t.Context(), "execution", previous.ID, &previous); err != nil {
				t.Fatal(err)
			}
			if tt.release && (previous.Status != "interrupted" || previous.FinishedAt == nil || previous.Error == "") {
				t.Fatalf("orphaned execution was not made inspectable: %+v", previous)
			}
			if !tt.release && previous.Status != "running" {
				t.Fatal("unconfirmed workflow was modified")
			}
			if host.calls.Load() != 0 {
				t.Fatal("reconciliation replayed earlier tool actions")
			}
		})
	}
}

type integrationHost struct{ calls atomic.Int32 }

func (h *integrationHost) Step(ctx context.Context, in taskentity.StepInput) (any, error) {
	h.calls.Add(1)
	return map[string]any{"done": in.Step.ID}, nil
}
func (h *integrationHost) Notify(context.Context, taskentity.Snapshot, string, string) error {
	return nil
}
