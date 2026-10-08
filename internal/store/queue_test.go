package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"agentTest/internal/domain"
)

// Hiding optional methods verifies compatibility with existing Store wrappers.
type legacyQueueStore struct{ Store }

func queueFixture(t *testing.T, ctx context.Context, s Store, prefix string) []domain.Run {
	t.Helper()
	base := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	runs := []domain.Run{
		{ID: prefix + "cancelled", Status: "cancelled", SessionID: prefix + "other", CreatedAt: base.Add(-time.Hour)},
		{ID: "background-" + prefix, Status: "queued", SessionID: prefix + "other", CreatedAt: base.Add(-time.Hour)},
		{ID: prefix + "busy", Status: "queued", SessionID: prefix + "busy-session", CreatedAt: base.Add(-time.Hour)},
		{ID: prefix + "completed", Status: "completed", SessionID: prefix + "other", CreatedAt: base.Add(-time.Hour)},
		{ID: prefix + "running", Status: "running", SessionID: prefix + "other", CreatedAt: base.Add(-time.Hour)},
		{ID: prefix + "late", Status: "queued", SessionID: prefix + "idle", CreatedAt: base.Add(time.Hour)},
		{ID: prefix + "fraction", Status: "queued", SessionID: prefix + "idle", CreatedAt: base.Add(100 * time.Millisecond)},
		{ID: prefix + "b", Status: "queued", SessionID: prefix + "idle", CreatedAt: base},
		{ID: prefix + "a", Status: "queued", SessionID: prefix + "idle", CreatedAt: base.In(time.FixedZone("offset", 8*3600))},
	}
	for _, run := range runs {
		if err := s.Put(ctx, "run", run.ID, run); err != nil {
			t.Fatal(err)
		}
	}
	return runs
}

func queuedIDs(runs []domain.Run) []string {
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	return ids
}

func TestQueuedRunsFiltersBeforeLimitAndSorts(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory", "legacy store wrapper"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var s Store = NewMemory()
			if name != "memory" {
				s = legacyQueueStore{s}
			}
			queueFixture(t, t.Context(), s, "fixture-")
			runs, err := QueuedRuns(t.Context(), s, []string{"fixture-busy-session"}, 3)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"fixture-a", "fixture-b", "fixture-fraction"}
			if !slices.Equal(queuedIDs(runs), want) {
				t.Fatalf("queued runs=%v want=%v", queuedIDs(runs), want)
			}
			runs, err = QueuedRuns(t.Context(), s, nil, 1)
			if err != nil || len(runs) != 1 || runs[0].ID != "fixture-busy" {
				t.Fatalf("nil excludes changed semantics: %v %v", queuedIDs(runs), err)
			}
		})
	}
}

func TestQueuedRunsBoundsAndCancellation(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	for i := range 200 {
		id := fmt.Sprintf("queued-%03d", i)
		if err := s.Put(t.Context(), "run", id, domain.Run{ID: id, Status: "queued", SessionID: "idle", CreatedAt: time.Unix(int64(i), 0)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct{ limit, want int }{{0, 64}, {-1, 64}, {1, 1}, {128, 128}, {50000, 128}} {
		t.Run(fmt.Sprint(tt.limit), func(t *testing.T) {
			runs, err := QueuedRuns(t.Context(), s, nil, tt.limit)
			if err != nil || len(runs) != tt.want {
				t.Fatalf("count=%d want=%d err=%v", len(runs), tt.want, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := QueuedRuns(ctx, s, nil, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if runs, err := QueuedRuns(t.Context(), s, []string{"idle"}, 128); err != nil || len(runs) != 0 {
		t.Fatalf("busy session was returned: %d %v", len(runs), err)
	}
}

func TestPostgresQueuedRuns(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL required for real PostgreSQL queue integration")
	}
	if !strings.Contains(url, "/secretary_test") {
		t.Fatal("integration database must be named secretary_test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	existing, err := All[domain.Run](ctx, s, "run")
	if err != nil {
		t.Fatal(err)
	}
	// Isolate this fixture without deleting any records from other test runs.
	excluded := []string{}
	for _, run := range existing {
		excluded = append(excluded, run.SessionID)
	}
	prefix := "queue-test-" + domain.ID() + "-"
	fixture := queueFixture(t, ctx, s, prefix)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		for _, run := range fixture {
			if err := s.Delete(cleanup, "run", run.ID); err != nil {
				t.Error(err)
			}
		}
	}()
	all, err := QueuedRuns(ctx, s, excluded, 128)
	if err != nil || len(all) != 5 {
		t.Fatalf("queued candidates=%v err=%v", queuedIDs(all), err)
	}
	excluded = append(excluded, prefix+"busy-session")
	runs, err := QueuedRuns(ctx, s, excluded, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{prefix + "a", prefix + "b", prefix + "fraction"}
	if !slices.Equal(queuedIDs(runs), want) {
		t.Fatalf("Postgres ordering/filtering=%v want=%v", queuedIDs(runs), want)
	}
	var indexExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE tablename='records' AND indexname='records_run_queue_idx')`).Scan(&indexExists); err != nil || !indexExists {
		t.Fatalf("queue index missing: %v", err)
	}
}
