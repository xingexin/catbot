package temporal

import (
	"time"

	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func TaskWorkflow(ctx workflow.Context, input taskentity.Input) (result map[string]any, runErr error) {
	opts := workflow.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 3}}
	ctx = workflow.WithActivityOptions(ctx, opts)
	id := workflow.GetInfo(ctx).WorkflowExecution.ID
	var snapshot taskentity.Snapshot
	if err := workflow.ExecuteActivity(ctx, "Begin", input, id).Get(ctx, &snapshot); err != nil {
		return nil, err
	}
	result = map[string]any{}
	defer func() {
		finishCtx, _ := workflow.NewDisconnectedContext(ctx)
		status := "completed"
		message := ""
		if runErr != nil {
			status = "failed"
			message = runErr.Error()
			if temporal.IsCanceledError(runErr) {
				status = "cancelled"
			}
		}
		err := workflow.ExecuteActivity(finishCtx, "Finish", snapshot, id, status, message, result).Get(finishCtx, nil)
		if runErr == nil && err != nil {
			runErr = err
		}
	}()
	for _, step := range snapshot.Task.Steps {
		if step.DelaySec > 0 {
			if err := workflow.NewTimer(ctx, time.Duration(step.DelaySec)*time.Second).Get(ctx, nil); err != nil {
				return result, err
			}
		}
		stepCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: time.Hour, HeartbeatTimeout: 30 * time.Second, WaitForCancellation: true,
			RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second, MaximumAttempts: 3},
		})
		var value any
		if err := workflow.ExecuteActivity(stepCtx, "ExecuteStep", taskentity.StepInput{Snapshot: snapshot, Step: step, ExecutionID: id, Results: result}).Get(stepCtx, &value); err != nil {
			return result, err
		}
		result[step.ID] = value
	}
	return result, nil
}
