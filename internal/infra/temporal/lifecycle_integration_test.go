//go:build integration

package temporal

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	taskbiz "github.com/xingexin/catbot/internal/biz/task"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

// The dedicated schema, unused unique task queue, distant schedule and disabled
// notifications ensure this test cannot invoke a model, plugin or QQ channel.
func TestRealTaskArchiveRestoreAndPurgeLifecycle(t *testing.T) {
	address, databaseURL := os.Getenv("TEST_TEMPORAL_ADDRESS"), os.Getenv("TEST_DATABASE_URL")
	if address == "" || databaseURL == "" {
		t.Skip("real Temporal and isolated TEST_DATABASE_URL required")
	}
	requireTestDatabase(t, databaseURL)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	token := idgen.New()
	schema := "task_lifecycle_" + strings.ReplaceAll(token, "-", "_")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Error("clean lifecycle test schema:", err)
		}
	}()
	isolatedURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := isolatedURL.Query()
	query.Set("search_path", schema)
	isolatedURL.RawQuery = query.Encode()
	s, err := store.Open(ctx, isolatedURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := client.DialContext(ctx, client.Options{HostPort: address})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	scheduler := New(c, s)
	scheduler.TaskQueue = "integration-lifecycle-" + token
	commands := &taskbiz.Commands{Store: s, Scheduler: scheduler, Plugins: lifecycleNoPlugins{}}
	next := time.Now().UTC().Add(7 * 24 * time.Hour)
	task := taskentity.Task{
		ID: "integration-lifecycle-" + token, Name: "isolated archive lifecycle",
		Kind: "recurring", Cron: fmt.Sprintf("%d %d %d %d *", next.Minute(), next.Hour(), next.Day(), int(next.Month())),
		TimeZone: "UTC", CatchupSec: 60, Revision: 1, Status: taskentity.StateActive.WireName(),
		ConfigID: "config-" + token, PersonaID: "persona-" + token, Notify: false,
		Steps: []taskentity.Step{{ID: "unused", Kind: "agent", Prompt: "This isolated schedule is never executed."}},
	}
	for _, record := range []struct {
		kind, id string
		value    any
	}{
		{"task", task.ID, task},
		{"config", task.ConfigID, agent.Config{ID: task.ConfigID, Name: "isolated lifecycle fixture", Kind: "api", Model: "unused"}},
		{"persona", task.PersonaID, persona.Persona{ID: task.PersonaID, Name: "isolated lifecycle fixture"}},
	} {
		if err := s.Put(ctx, record.kind, record.id, record.value); err != nil {
			t.Fatal(err)
		}
	}
	handle := c.ScheduleClient().GetHandle(ctx, task.ID)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		err := handle.Delete(cleanup)
		var missing *serviceerror.NotFound
		if err != nil && !errors.As(err, &missing) {
			t.Error("clean lifecycle test schedule:", err)
		}
	}()
	assertMissingSchedule := func() {
		t.Helper()
		_, err := handle.Describe(ctx)
		var missing *serviceerror.NotFound
		if !errors.As(err, &missing) {
			t.Fatalf("schedule must be absent, got %v", err)
		}
	}
	assertArchive := func(want bool) {
		t.Helper()
		archived, err := lifecycleRepository.Archived(ctx, s, lifecycle.ResourceTask, task.ID)
		if err != nil || archived != want {
			t.Fatalf("archive index: got %v, want %v, error %v", archived, want, err)
		}
	}

	if err := scheduler.Apply(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Describe(ctx); err != nil {
		t.Fatal("initial schedule missing:", err)
	}
	if err := commands.Archive(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	assertMissingSchedule()
	assertArchive(true)
	if err := commands.Restore(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	assertMissingSchedule()
	assertArchive(false)
	var restored taskentity.Task
	if err := s.Get(ctx, "task", task.ID, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Paused || taskentity.ParseState(restored.Status) != taskentity.StatePaused || restored.Revision <= task.Revision {
		t.Fatalf("restored task is not safely paused: %+v", restored)
	}
	value, err := commands.ControlTaskWithOperation(ctx, task.ID, "resume", "resume-"+token)
	if err != nil {
		t.Fatal(err)
	}
	resumed, ok := value.(taskentity.Task)
	if !ok || resumed.Paused || taskentity.ParseState(resumed.Status) != taskentity.StateActive {
		t.Fatalf("explicit resume did not activate task: %#v", value)
	}
	description, err := handle.Describe(ctx)
	if err != nil {
		t.Fatal("explicit resume did not recreate schedule:", err)
	}
	if description.Schedule.State == nil || description.Schedule.State.Paused {
		t.Fatal("recreated schedule is still paused")
	}
	if err := commands.Archive(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	assertMissingSchedule()
	if err := commands.Purge(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	assertMissingSchedule()
	assertArchive(false)
	var row any
	if err := s.Get(ctx, "task", task.ID, &row); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("task definition was not physically deleted:", err)
	}
	purged, err := store.Purged(ctx, s, "task", task.ID)
	if err != nil || !purged {
		t.Fatal("task replay guard missing:", err)
	}
	engine := &taskbiz.ExecutionHost{Store: s}
	for _, input := range []taskentity.Input{
		{TaskID: task.ID, Revision: task.Revision},
		{TaskID: task.ID, Revision: resumed.Revision, Manual: true},
	} {
		executionID := "late-" + idgen.New()
		if _, err := engine.Begin(ctx, input, executionID); !errors.Is(err, lifecycleRepository.ErrPurged) {
			t.Fatalf("late Begin did not reject deleted task: %v", err)
		}
		for _, kind := range []string{"execution", "execution-snapshot"} {
			if err := s.Get(ctx, kind, executionID, &row); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("late Begin recreated %s: %v", kind, err)
			}
		}
	}
	if _, err := commands.SaveTask(ctx, resumed); !errors.Is(err, lifecycleRepository.ErrPurged) {
		t.Fatalf("late edit recreated deleted task: %v", err)
	}
	if err := s.Put(ctx, "task", task.ID, resumed); !errors.Is(err, store.ErrPurgedRecord) {
		t.Fatalf("PostgreSQL accepted stale task write: %v", err)
	}
	assertMissingSchedule()
	t.Log("real Schedule deleted on archive, absent after paused restore, recreated only by explicit resume, then purged without replay")
}

type lifecycleNoPlugins struct{}

func (lifecycleNoPlugins) Snapshots(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (lifecycleNoPlugins) Tools(context.Context, map[string]string, []string) ([]agent.Tool, error) {
	return []agent.Tool{}, nil
}
