package job

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"agentTest/internal/domain"
	"agentTest/internal/store"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

func TestRealScheduleUpdatePauseResumeAndDelete(t *testing.T) {
	address := os.Getenv("TEST_TEMPORAL_ADDRESS")
	db := os.Getenv("TEST_DATABASE_URL")
	if address == "" || db == "" {
		t.Skip("real Temporal and isolated TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := client.DialContext(ctx, client.Options{HostPort: address})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	e := New(c, s, &integrationHost{})
	e.TaskQueue = "integration-" + domain.ID()
	task := domain.Task{ID: domain.ID(), Kind: "recurring", Cron: "17 9 * * *", TimeZone: "Asia/Shanghai", CatchupSec: 120, Revision: 1}
	handle := c.ScheduleClient().GetHandle(ctx, task.ID)
	defer handle.Delete(context.Background())
	assertSchedule := func(paused bool, catchup int, minute int) {
		t.Helper()
		desc, err := handle.Describe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if desc.Schedule.State.Paused != paused ||
			desc.Schedule.Policy.Overlap != enumspb.SCHEDULE_OVERLAP_POLICY_SKIP ||
			desc.Schedule.Policy.CatchupWindow != time.Duration(catchup)*time.Second ||
			desc.Schedule.Spec.TimeZoneName != "Asia/Shanghai" {
			t.Fatalf("schedule state differs from requested state: %+v", desc.Schedule)
		}
		calendars := desc.Schedule.Spec.Calendars
		if len(calendars) != 1 || len(calendars[0].Minute) != 1 || calendars[0].Minute[0].Start != minute {
			t.Fatalf("schedule calendar differs from requested cron: %+v", calendars)
		}
	}
	if err := e.Apply(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	assertSchedule(false, 120, 17)
	old := task
	task.Cron, task.CatchupSec, task.Revision, task.Paused = "43 10 * * *", 300, 2, true
	if err := e.Apply(ctx, task, &old); err != nil {
		t.Fatal(err)
	}
	assertSchedule(true, 300, 43)
	old = task
	task.Paused, task.Revision = false, 3
	if err := e.Apply(ctx, task, &old); err != nil {
		t.Fatal(err)
	}
	assertSchedule(false, 300, 43)
	if err := e.Cancel(ctx, task); err != nil {
		t.Fatal(err)
	}
	_, err = handle.Describe(ctx)
	var missing *serviceerror.NotFound
	if !errors.As(err, &missing) {
		t.Fatalf("cancelled schedule was not deleted: %v", err)
	}
}

type integrationHost struct{ calls atomic.Int32 }

func (h *integrationHost) Step(ctx context.Context, in StepInput) (any, error) {
	h.calls.Add(1)
	return map[string]any{"done": in.Step.ID}, nil
}
func (h *integrationHost) Notify(context.Context, Snapshot, string, string) error { return nil }
func TestRealTemporalDelayCancelAndStepPersistence(t *testing.T) {
	address := os.Getenv("TEST_TEMPORAL_ADDRESS")
	db := os.Getenv("TEST_DATABASE_URL")
	if address == "" || db == "" {
		t.Skip("real Temporal and isolated TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := client.DialContext(ctx, client.Options{HostPort: address})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h := &integrationHost{}
	e := New(c, s, h)
	e.TaskQueue = "integration-" + domain.ID()
	w := e.Worker()
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	id := domain.ID()
	at := time.Now().Add(time.Second)
	task := domain.Task{ID: id, Kind: "once", Name: "integration", Revision: 1, Status: "active", RunAt: &at, ConfigID: id, PersonaID: id, Steps: []domain.Step{{ID: "one", Kind: "tool"}, {ID: "two", Kind: "agent"}}}
	for kind, value := range map[string]any{"task": task, "config": domain.Config{ID: id}, "persona": domain.Persona{ID: id}} {
		if err := s.Put(ctx, kind, id, value); err != nil {
			t.Fatal(err)
		}
		defer s.Delete(context.Background(), kind, id)
	}
	if err := e.Apply(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := c.GetWorkflow(ctx, onceID(task), "").Get(ctx, &result); err != nil {
		t.Fatal(err)
	}
	if h.calls.Load() != 2 {
		t.Fatal("unexpected calls", h.calls.Load())
	}
	if err := e.Apply(ctx, task, nil); err != nil {
		t.Fatal("idempotent delayed start:", err)
	}
	if err := c.GetWorkflow(ctx, onceID(task), "").Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if h.calls.Load() != 2 {
		t.Fatal("completed one-time task ran twice")
	}
	manualID, err := e.Trigger(ctx, task, "stable-operation")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.GetWorkflow(ctx, manualID, "").Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	again, err := e.Trigger(ctx, task, "stable-operation")
	if err != nil || again != manualID {
		t.Fatal(again, err)
	}
	if err := c.GetWorkflow(ctx, again, "").Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if h.calls.Load() != 4 {
		t.Fatal("completed manual trigger was not deduplicated")
	}
	var execution domain.TaskExecution
	if err := s.Get(ctx, "execution", onceID(task), &execution); err != nil {
		t.Fatal(err)
	}
	if execution.Status != "completed" || len(execution.Results) != 2 {
		t.Fatal(execution)
	}
	at = time.Now().Add(20 * time.Second)
	task.ID = domain.ID()
	task.RunAt = &at
	task.Status = "active"
	if err := s.Put(ctx, "task", task.ID, task); err != nil {
		t.Fatal(err)
	}
	defer s.Delete(context.Background(), "task", task.ID)
	if err := e.Apply(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	task.Status = "cancelled"
	_ = s.Put(ctx, "task", task.ID, task)
	if err := e.Cancel(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := c.GetWorkflow(ctx, onceID(task), "").Get(ctx, nil); err == nil {
		t.Fatal("cancelled timer executed")
	}
	if h.calls.Load() != 4 {
		t.Fatal("cancelled task called host")
	}
}
