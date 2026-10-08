package job

import (
	"testing"
	"time"

	"agentTest/internal/domain"
	"agentTest/internal/store"
)

func TestRecurringFailureNotificationsAreThrottledAndRecover(t *testing.T) {
	e := New(nil, store.NewMemory(), nil)
	task := domain.Task{ID: "mail", Kind: "recurring"}
	check := func(id, status string, want bool) {
		t.Helper()
		got, err := e.allowNotification(t.Context(), task, id, status)
		if err != nil || got != want {
			t.Fatalf("%s/%s = %v %v", id, status, got, err)
		}
	}
	check("first", "failed", true)
	check("first", "failed", true) // An Activity retry still reaches the idempotent delivery.
	check("second", "failed", false)
	if err := e.Store.Put(t.Context(), "notification-incident", task.ID, notificationIncident{ExecutionID: "first", AttemptedAt: time.Now().Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	check("third", "failed", true)
	check("healthy", "completed", true)
	check("new-incident", "failed", true)
	task.Kind = "once"
	check("once", "failed", true)
}
