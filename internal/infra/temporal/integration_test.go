//go:build integration

package temporal

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	taskbiz "github.com/xingexin/catbot/internal/biz/task"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
	workerentry "github.com/xingexin/catbot/internal/worker/temporal"

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
	requireTestDatabase(t, db)
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
	e := New(c, s)
	e.TaskQueue = "integration-" + idgen.New()
	task := taskentity.Task{ID: idgen.New(), Kind: "recurring", Cron: "17 9 * * *", TimeZone: "Asia/Shanghai", CatchupSec: 120, Revision: 1}
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

func TestRealTemporalDelayCancelAndStepPersistence(t *testing.T) {
	address := os.Getenv("TEST_TEMPORAL_ADDRESS")
	db := os.Getenv("TEST_DATABASE_URL")
	if address == "" || db == "" {
		t.Skip("real Temporal and isolated TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	requireTestDatabase(t, db)
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
	e := New(c, s)
	e.TaskQueue = "integration-" + idgen.New()
	w := workerentry.New(c, e.TaskQueue, &taskbiz.ExecutionHost{Store: s, Host: h, ExecutionClosed: e.ExecutionClosed})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	id := idgen.New()
	at := time.Now().Add(time.Second)
	task := taskentity.Task{ID: id, Kind: "once", Name: "integration", Revision: 1, Status: "active", RunAt: &at, ConfigID: id, PersonaID: id, Steps: []taskentity.Step{{ID: "one", Kind: "tool"}, {ID: "two", Kind: "agent"}}}
	for kind, value := range map[string]any{"task": task, "config": agent.Config{ID: id}, "persona": persona.Persona{ID: id}} {
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
	var execution taskentity.TaskExecution
	if err := s.Get(ctx, "execution", onceID(task), &execution); err != nil {
		t.Fatal(err)
	}
	if execution.Status != "completed" || len(execution.Results) != 2 {
		t.Fatal(execution)
	}
	at = time.Now().Add(20 * time.Second)
	task.ID = idgen.New()
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

func requireTestDatabase(t *testing.T, dsn string) {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Path != "/secretary_test" {
		t.Fatal("Temporal integration tests require the isolated secretary_test database")
	}
}

type restartHost struct {
	integrationHost
	personaName string
}

func (h *restartHost) Step(ctx context.Context, in taskentity.StepInput) (any, error) {
	if in.Snapshot.Persona.Name != h.personaName {
		return nil, errors.New("recovery replaced the execution persona snapshot")
	}
	return h.integrationHost.Step(ctx, in)
}

func TestRealWorkerRestartKeepsCompletedStepAndSnapshot(t *testing.T) {
	address, db := os.Getenv("TEST_TEMPORAL_ADDRESS"), os.Getenv("TEST_DATABASE_URL")
	if address == "" || db == "" {
		t.Skip("real Temporal and isolated TEST_DATABASE_URL required")
	}
	requireTestDatabase(t, db)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	c, err := client.DialContext(ctx, client.Options{HostPort: address})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id := idgen.New()
	scheduler := New(c, s)
	scheduler.TaskQueue = "integration-restart-" + id
	at := time.Now().Add(time.Second)
	task := taskentity.Task{ID: id, Kind: "once", Name: "worker recovery", Revision: 1, Status: "active", RunAt: &at, ConfigID: id, PersonaID: id, Steps: []taskentity.Step{{ID: "one", Kind: "tool"}, {ID: "two", Kind: "tool", DelaySec: 4}}}
	for kind, value := range map[string]any{"task": task, "config": agent.Config{ID: id}, "persona": persona.Persona{ID: id, Name: "frozen-before-restart"}} {
		if err := s.Put(ctx, kind, id, value); err != nil {
			t.Fatal(err)
		}
	}
	workflowID := onceID(task)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = c.CancelWorkflow(cleanup, workflowID, "")
		for _, record := range []struct{ kind, id string }{{"task", id}, {"config", id}, {"persona", id}, {"execution", workflowID}, {"execution-snapshot", workflowID}, {"step-result", workflowID + ":one"}, {"step-result", workflowID + ":two"}} {
			_ = s.Delete(cleanup, record.kind, record.id)
		}
	}()
	before := &restartHost{personaName: "frozen-before-restart"}
	worker := workerentry.New(c, scheduler.TaskQueue, &taskbiz.ExecutionHost{Store: s, Host: before, ExecutionClosed: scheduler.ExecutionClosed})
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	// Capture the current worker variable so failures stop whichever generation runs.
	defer func() {
		if worker != nil {
			worker.Stop()
		}
	}()
	if err := scheduler.Apply(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	for {
		var value any
		if err := s.Get(ctx, "step-result", workflowID+":one", &value); err == nil {
			break
		} else if !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("first step did not persist before restart")
		case <-time.After(50 * time.Millisecond):
		}
	}
	worker.Stop()
	worker = nil
	if before.calls.Load() != 1 {
		t.Fatalf("unexpected work before restart: %d", before.calls.Load())
	}
	// Reopen persistence as a new application process would; mutate only this
	// test persona to prove the existing workflow keeps its frozen snapshot.
	s.Close()
	s, err = store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "persona", id, persona.Persona{ID: id, Name: "changed-after-restart"}); err != nil {
		t.Fatal(err)
	}
	after := &restartHost{personaName: "frozen-before-restart"}
	scheduler = New(c, s)
	scheduler.TaskQueue = "integration-restart-" + id
	worker = workerentry.New(c, scheduler.TaskQueue, &taskbiz.ExecutionHost{Store: s, Host: after, ExecutionClosed: scheduler.ExecutionClosed})
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	if err := c.GetWorkflow(ctx, workflowID, "").Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var execution taskentity.TaskExecution
	if err := s.Get(ctx, "execution", workflowID, &execution); err != nil {
		t.Fatal(err)
	}
	if before.calls.Load() != 1 || after.calls.Load() != 1 || execution.Status != "completed" || len(execution.Results) != 2 || execution.Persona == nil || execution.Persona.Name != "frozen-before-restart" {
		t.Fatalf("recovery replayed work or lost its snapshot: before=%d after=%d status=%s results=%d", before.calls.Load(), after.calls.Load(), execution.Status, len(execution.Results))
	}
	t.Log("test worker restarted; completed step retained, next step executed once, original persona preserved")
}
