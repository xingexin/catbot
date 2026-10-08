//go:build integration

package temporal

import (
	"context"
	"os"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// This opt-in test reads existing histories. Replaying a workflow only executes
// deterministic workflow code; recorded Activity results replace all real tools.
func TestReplayExistingTaskWorkflowHistories(t *testing.T) {
	address := os.Getenv("TEST_TEMPORAL_ADDRESS")
	if address == "" || os.Getenv("TEST_TEMPORAL_REPLAY_HISTORY") != "true" {
		t.Skip("explicit TEST_TEMPORAL_REPLAY_HISTORY=true and Temporal address required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, client.Options{HostPort: address})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	executions, err := c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: "default", PageSize: 3, Query: "WorkflowType = 'TaskWorkflow' AND ExecutionStatus = 'Completed'",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executions.Executions) == 0 {
		t.Fatal("no historical TaskWorkflow execution available for replay")
	}
	for i, execution := range executions.Executions {
		history := &historypb.History{}
		iterator := c.GetWorkflowHistory(ctx, execution.Execution.WorkflowId, execution.Execution.RunId, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for iterator.HasNext() {
			event, err := iterator.Next()
			if err != nil {
				t.Fatal(err)
			}
			history.Events = append(history.Events, event)
		}
		replayer := worker.NewWorkflowReplayer()
		replayer.RegisterWorkflowWithOptions(TaskWorkflow, workflow.RegisterOptions{Name: "TaskWorkflow"})
		if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
			t.Fatalf("historical execution %d failed replay: %v", i+1, err)
		}
		// Only metadata is logged; model input, persona and Activity output stay in memory.
		t.Logf("historical execution %d: %d events replayed; started %s", i+1, len(history.Events), execution.StartTime.AsTime().UTC().Format(time.RFC3339))
	}
}
