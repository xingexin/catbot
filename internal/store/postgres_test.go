package store

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"agentTest/internal/domain"
)

func TestPostgresPersistenceAndLockContention(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL required for real PostgreSQL integration")
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
	kind := "integration-" + domain.ID()
	defer s.Delete(context.Background(), kind, "row")
	if err := s.Put(ctx, kind, "row", map[string]any{"value": 7}); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var value map[string]any
	if err := s2.Get(ctx, kind, "row", &value); err != nil || value["value"] != float64(7) {
		t.Fatal(value, err)
	}
	key := domain.ID()
	unlock, err := s.Lock(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			release, err := s.Lock(ctx, key)
			if err != nil {
				t.Error(err)
				return
			}
			release()
		})
	}
	// More waiters than the business pool capacity must not starve its SQL.
	if err := s.Put(ctx, kind, "row", map[string]any{"value": 8}); err != nil {
		t.Fatal(err)
	}
	unlock()
	wg.Wait()
	runID := domain.ID()
	if err := s.Append(ctx, domain.Event{RunID: runID, Type: "text.delta", Data: map[string]any{"text": "one"}, Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	events, err := s2.Events(ctx, runID, 0)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	events, err = s.Events(ctx, runID, events[0].Sequence)
	if err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
}
