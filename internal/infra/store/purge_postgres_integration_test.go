//go:build integration

package store

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresPurgeTransactionRollbackAndCommit(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL required for real PostgreSQL integration")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Path != "/secretary_test" {
		t.Fatal("purge integration requires the dedicated secretary_test database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "purge_test_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &Postgres{pool: pool}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE records (
			kind text NOT NULL, id text NOT NULL, data jsonb NOT NULL,
			updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(kind,id),
			CONSTRAINT reject_second_guard CHECK (kind <> 'purged-record' OR data->>'id' <> 'reject')
		);
		CREATE TABLE run_events (
			sequence bigserial PRIMARY KEY, run_id text NOT NULL,
			type text NOT NULL, data jsonb NOT NULL, created_at timestamptz NOT NULL
		);
	`); err != nil {
		t.Fatal(err)
	}
	refs := []RecordRef{{Kind: "run", ID: "first"}, {Kind: "run", ID: "reject"}}
	for _, ref := range refs {
		if err := s.Put(ctx, ref.Kind, ref.ID, map[string]string{"text": "private contents"}); err != nil {
			t.Fatal(err)
		}
		if err := s.Append(ctx, EventRecord{RunID: ref.ID, Type: "test", Time: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Purge(ctx, s, refs, []string{"first", "reject"}); err == nil {
		t.Fatal("second guard should fail, forcing transaction rollback")
	}
	for _, ref := range refs {
		var row map[string]string
		if err := s.Get(ctx, ref.Kind, ref.ID, &row); err != nil || row["text"] != "private contents" {
			t.Fatal("partial transaction committed", row, err)
		}
		events, err := s.Events(ctx, ref.ID, 0)
		if err != nil || len(events) != 1 {
			t.Fatal("events were partially purged", events, err)
		}
	}
	guards, err := s.List(ctx, PurgedRecordKind)
	if err != nil || len(guards) != 0 {
		t.Fatal("guard was partially committed", guards, err)
	}
	if _, err := pool.Exec(ctx, "ALTER TABLE records DROP CONSTRAINT reject_second_guard"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Purge(ctx, s, refs, []string{"first", "reject"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range refs {
		var row map[string]string
		if err := s.Get(ctx, ref.Kind, ref.ID, &row); !errors.Is(err, ErrNotFound) {
			t.Fatal(row, err)
		}
		events, err := s.Events(ctx, ref.ID, 0)
		if err != nil || len(events) != 0 {
			t.Fatal(events, err)
		}
		purged, err := Purged(ctx, s, ref.Kind, ref.ID)
		if err != nil || !purged {
			t.Fatal("missing persisted guard", purged, err)
		}
	}
	for _, ref := range refs {
		if err := s.Put(ctx, ref.Kind, ref.ID, map[string]string{"text": "late write"}); !errors.Is(err, ErrPurgedRecord) {
			t.Fatal("purged row was recreated", err)
		}
		if err := s.Append(ctx, EventRecord{RunID: ref.ID, Type: "test", Time: time.Now()}); !errors.Is(err, ErrPurgedRecord) {
			t.Fatal("purged event was recreated", err)
		}
	}
	reusable := RecordRef{Kind: "session", ID: "new-conversation", Reusable: true}
	if err := s.Put(ctx, reusable.Kind, reusable.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := Purge(ctx, s, []RecordRef{reusable}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, reusable.Kind, reusable.ID, false); err != nil {
		t.Fatal("reusable identity cannot be recreated", err)
	}
	listed, err := RecordRefs(ctx, s, reusable.Kind)
	if err != nil || len(listed) != 1 || listed[0].Kind != reusable.Kind || listed[0].ID != reusable.ID {
		t.Fatal("storage keys were not listed", listed, err)
	}
	if err := s.Append(ctx, EventRecord{RunID: "kept", Type: "test", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, "kept", 0)
	if err != nil || len(events) != 1 || events[0].Sequence <= 2 {
		t.Fatal("event cursor reused after purge", events, err)
	}
}
