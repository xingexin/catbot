package temporal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

type Scheduler struct {
	TaskQueue string
	Client    client.Client
	Store     store.Store
}

func New(c client.Client, s store.Store) *Scheduler {
	return &Scheduler{Client: c, Store: s, TaskQueue: Queue}
}

const Queue = "secretary-tasks"

func (e *Scheduler) queue() string {
	if e.TaskQueue != "" {
		return e.TaskQueue
	}
	return Queue
}

func (e *Scheduler) Ping(ctx context.Context) error {
	_, err := e.Client.CheckHealth(ctx, &client.CheckHealthRequest{})
	return err
}

func onceID(t taskentity.Task) string { return fmt.Sprintf("task-%s-r%d", t.ID, t.Revision) }

func (e *Scheduler) Apply(ctx context.Context, t taskentity.Task, old *taskentity.Task) error {
	if old != nil && old.Kind == "once" {
		var active taskentity.TaskExecution
		keepActive := t.Paused && e.Store.Get(ctx, "execution", onceID(*old), &active) == nil && active.Status == "running"
		var err error
		if !keepActive {
			err = e.Client.CancelWorkflow(ctx, onceID(*old), "")
		}
		var missing *serviceerror.NotFound
		if err != nil && !errors.As(err, &missing) {
			return err
		}
	}
	if old != nil && old.Kind == "recurring" && t.Kind != "recurring" {
		err := e.Client.ScheduleClient().GetHandle(ctx, t.ID).Delete(ctx)
		var missing *serviceerror.NotFound
		if err != nil && !errors.As(err, &missing) {
			return err
		}
	}
	if t.Kind == "recurring" {
		spec := client.ScheduleSpec{CronExpressions: []string{t.Cron}, TimeZoneName: t.TimeZone}
		action := &client.ScheduleWorkflowAction{ID: "task-" + t.ID, Workflow: "TaskWorkflow", Args: []any{taskentity.Input{TaskID: t.ID, Revision: t.Revision}}, TaskQueue: e.queue()}
		handle := e.Client.ScheduleClient().GetHandle(ctx, t.ID)
		_, err := handle.Describe(ctx)
		if err == nil {
			return handle.Update(ctx, client.ScheduleUpdateOptions{DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
				s := in.Description.Schedule
				s.Spec = &spec
				s.Action = action
				s.State.Paused = t.Paused
				s.Policy.Overlap = enumspb.SCHEDULE_OVERLAP_POLICY_SKIP
				s.Policy.CatchupWindow = time.Duration(t.CatchupSec) * time.Second
				return &client.ScheduleUpdate{Schedule: &s}, nil
			}})
		}
		var missing *serviceerror.NotFound
		if !errors.As(err, &missing) {
			return err
		}
		_, err = e.Client.ScheduleClient().Create(ctx, client.ScheduleOptions{ID: t.ID, Spec: spec, Action: action, Paused: t.Paused, Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP, CatchupWindow: time.Duration(t.CatchupSec) * time.Second})
		return err
	}
	if t.Kind == "once" && !t.Paused {
		delay := time.Until(*t.RunAt)
		if delay < 0 {
			delay = 0
		}
		_, err := e.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: onceID(t), TaskQueue: e.queue(), StartDelay: delay, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, "TaskWorkflow", taskentity.Input{TaskID: t.ID, Revision: t.Revision})
		var started *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &started) {
			return nil
		}
		return err
	}
	return nil
}

func (e *Scheduler) Trigger(ctx context.Context, t taskentity.Task, operationID string) (string, error) {
	sum := sha256.Sum256([]byte(operationID))
	id := "manual-" + t.ID + "-" + hex.EncodeToString(sum[:12])
	_, err := e.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: e.queue(), WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, "TaskWorkflow", taskentity.Input{TaskID: t.ID, Revision: t.Revision, Manual: true})
	var started *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &started) {
		return id, nil
	}
	return id, err
}

func (e *Scheduler) Cancel(ctx context.Context, t taskentity.Task) error {
	if t.Kind == "recurring" {
		err := e.Client.ScheduleClient().GetHandle(ctx, t.ID).Delete(ctx)
		var missing *serviceerror.NotFound
		if err != nil && !errors.As(err, &missing) {
			return err
		}
	}
	if t.Kind == "once" {
		err := e.Client.CancelWorkflow(ctx, onceID(t), "")
		var missing *serviceerror.NotFound
		if err != nil && !errors.As(err, &missing) {
			return err
		}
	}
	executions, err := store.All[taskentity.TaskExecution](ctx, e.Store, "execution")
	if err != nil {
		return err
	}
	for _, x := range executions {
		if x.TaskID == t.ID && x.Status == "running" {
			if err := e.Client.CancelWorkflow(ctx, x.ID, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

// ExecutionClosed confirms whether an execution can no longer perform work.
// Continued-as-new is kept occupied until an explicit terminal state is known.
func (e *Scheduler) ExecutionClosed(ctx context.Context, id string) (bool, error) {
	description, err := e.Client.DescribeWorkflowExecution(ctx, id, "")
	var missing *serviceerror.NotFound
	if errors.As(err, &missing) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if description == nil || description.WorkflowExecutionInfo == nil {
		return false, errors.New("Temporal returned no task execution status")
	}
	switch description.WorkflowExecutionInfo.Status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
		enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
		enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return true, nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		return false, nil
	default:
		return false, errors.New("Temporal task execution status is unknown")
	}
}
