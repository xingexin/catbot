package temporal

import (
	"context"
	"errors"
	"fmt"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	sdktemporal "go.temporal.io/sdk/temporal"
	"testing"
)

func TestActivityBoundaryPreservesRetryAndCancellationMeaning(t *testing.T) {
	t.Parallel()
	transient := errors.New("temporary database failure")
	if got := activityError(transient); got != transient {
		t.Fatalf("retryable error changed: %v", got)
	}
	if got := activityError(fmt.Errorf("plugin stopped: %w", context.Canceled)); !sdktemporal.IsCanceledError(got) {
		t.Fatalf("cancellation became retryable failure: %v", got)
	}
	for _, code := range []string{"InactiveTask", "PluginDisabled", "OverlapSkipped", "StepFailed"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			reason := errors.New("unsafe to retry")
			got := activityError(fmt.Errorf("business boundary: %w", taskentity.NewExecutionError("stopped", code, reason)))
			var application *sdktemporal.ApplicationError
			if !errors.As(got, &application) || application.Type() != code || !application.NonRetryable() || !errors.Is(got, reason) {
				t.Fatalf("lost persisted failure classification: %v", got)
			}
		})
	}
}
