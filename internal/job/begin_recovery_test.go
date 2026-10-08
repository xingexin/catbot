package job

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain"
	"github.com/xingexin/catbot/internal/store"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
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

func seedBegin(t *testing.T, s store.Store) domain.Task {
	t.Helper()
	task := domain.Task{ID: "recover-task", Kind: "recurring", Status: "active", Revision: 1, ConfigID: "config", PersonaID: "persona", Steps: []domain.Step{{ID: "work", Kind: "tool"}}}
	for _, entry := range []struct {
		kind, id string
		value    any
	}{
		{"task", task.ID, task},
		{"config", "config", domain.Config{ID: "config", Model: "original"}},
		{"persona", "persona", domain.Persona{ID: "persona", Name: "original"}},
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
	engine := New(nil, s, host)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(TaskWorkflow)
	env.RegisterActivity(engine.Begin)
	env.RegisterActivity(engine.ExecuteStep)
	env.RegisterActivity(engine.Finish)
	env.ExecuteWorkflow(TaskWorkflow, Input{TaskID: task.ID, Revision: task.Revision})
	if env.GetWorkflowError() == nil {
		t.Fatal("injected Begin failure was hidden")
	}
	if len(host.steps) != 0 {
		t.Fatal("work executed despite a failed Begin")
	}
	if _, err := engine.Begin(t.Context(), Input{TaskID: task.ID, Revision: task.Revision}, "next-poll"); err != nil {
		t.Fatalf("recovered storage left a permanent overlap blocker: %v", err)
	}
	executions, err := store.All[domain.TaskExecution](t.Context(), s, "execution")
	if err != nil {
		t.Fatal(err)
	}
	for _, execution := range executions {
		if execution.ID != "next-poll" && execution.Status == "running" {
			t.Fatalf("failed Begin left a running placeholder: %+v", execution)
		}
	}
}

func TestBeginRetryKeepsSnapshotAndRepairsMissingExecution(t *testing.T) {
	s := &beginFaultStore{Store: store.NewMemory(), kind: "execution"}
	s.remaining.Store(1)
	task := seedBegin(t, s)
	engine := New(nil, s, &fakeHost{})
	in := Input{TaskID: task.ID, Revision: task.Revision}
	if _, err := engine.Begin(t.Context(), in, "same-execution"); err == nil {
		t.Fatal("injected execution persistence failure was hidden")
	}
	if err := s.Put(t.Context(), "persona", "persona", domain.Persona{ID: "persona", Name: "updated-after-start"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.Begin(t.Context(), in, "same-execution")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Persona.Name != "original" {
		t.Fatal("retry switched to a new persona instead of the saved snapshot")
	}
	var execution domain.TaskExecution
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
			engine := New(nil, s, &fakeHost{})
			in := Input{TaskID: task.ID, Revision: task.Revision}
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
			var abandoned domain.TaskExecution
			if err := s.Get(t.Context(), "execution", "incomplete-begin", &abandoned); err != nil || abandoned.Status != "skipped" {
				t.Fatalf("abandoned begin was not visible: %+v %v", abandoned, err)
			}
		})
	}
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
			previous := domain.TaskExecution{ID: "old-execution", TaskID: task.ID, Status: "running", StartedAt: time.Now().Add(-time.Hour)}
			if err := s.Put(t.Context(), "execution", previous.ID, previous); err != nil {
				t.Fatal(err)
			}
			host := &fakeHost{}
			engine := New(beginDescribeClient{status: tt.status, err: tt.err}, s, host)
			_, err := engine.Begin(t.Context(), Input{TaskID: task.ID, Revision: task.Revision}, "new-execution")
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
			if len(host.steps) != 0 {
				t.Fatal("reconciliation replayed earlier tool actions")
			}
		})
	}
}
