package temporal

import (
	"context"
	"errors"
	"time"

	taskbiz "github.com/xingexin/catbot/internal/biz/task"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func New(c client.Client, queue string, host *taskbiz.ExecutionHost) worker.Worker {
	w := worker.New(c, queue, worker.Options{})
	Register(w, host)
	return w
}

// Register keeps persisted workflow and activity names independent of Go packages.
func Register(registry worker.Registry, host *taskbiz.ExecutionHost) {
	a := &Activities{Host: host}
	registry.RegisterWorkflowWithOptions(TaskWorkflow, workflow.RegisterOptions{Name: "TaskWorkflow"})
	registry.RegisterActivityWithOptions(a.Begin, activity.RegisterOptions{Name: "Begin"})
	registry.RegisterActivityWithOptions(a.ExecuteStep, activity.RegisterOptions{Name: "ExecuteStep"})
	registry.RegisterActivityWithOptions(a.Finish, activity.RegisterOptions{Name: "Finish"})
}

type Activities struct{ Host *taskbiz.ExecutionHost }

func (a *Activities) Begin(ctx context.Context, in taskentity.Input, id string) (taskentity.Snapshot, error) {
	value, err := a.Host.Begin(ctx, in, id)
	return value, activityError(err)
}

func (a *Activities) ExecuteStep(ctx context.Context, in taskentity.StepInput) (any, error) {
	// Heartbeats belong to the Temporal transport; business code can be invoked
	// from other execution backends without requiring an Activity context.
	done := make(chan struct{})
	stopped := make(chan struct{})
	defer func() { close(done); <-stopped }()
	go func() {
		defer close(stopped)
		timer := time.NewTicker(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-timer.C:
				activity.RecordHeartbeat(ctx, in.Step.ID)
			}
		}
	}()
	activity.RecordHeartbeat(ctx, in.Step.ID)
	value, err := a.Host.ExecuteStep(ctx, in)
	return value, activityError(err)
}

func (a *Activities) Finish(ctx context.Context, snapshot taskentity.Snapshot, id, status, message string, result map[string]any) error {
	return activityError(a.Host.Finish(ctx, snapshot, id, status, message, result))
}

func activityError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return temporal.NewCanceledError("step cancelled")
	}
	var stopped *taskentity.ExecutionError
	if errors.As(err, &stopped) {
		return temporal.NewNonRetryableApplicationError(stopped.Message, stopped.Code, stopped.Cause)
	}
	return err
}
