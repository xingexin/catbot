//go:build integration

package history

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/infra/store"
)

func postgresFixture(t *testing.T) (*store.Postgres, *pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for real PostgreSQL group history integration")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Path != "/secretary_test" {
		t.Fatal("group history integration requires the dedicated secretary_test database")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "group_history_test_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	dsn = parsed.String()
	s, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return s, pool, dsn
}

func TestPostgresGroupHistoryContract(t *testing.T) {
	runRepositoryContract(t, func(t *testing.T) store.Store {
		s, _, _ := postgresFixture(t)
		return s
	})
}

func TestPostgresGroupHistoryPhysicalCleanupPersistenceAndRollback(t *testing.T) {
	s, pool, dsn := postgresFixture(t)
	r := New(s)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	scope := messaging.GroupScope{Route: "onebot", Account: "bot", RoomID: "room"}
	settings := messaging.DefaultGroupHistorySettings()
	settings.Enabled, settings.RoomIDs = true, []string{"room"}
	msg := messaging.GroupMessage{ID: "message", SenderID: "alice", Text: "stored", SentAt: now.Add(-48 * time.Hour), Direction: messaging.GroupMessageDirectionIncoming}
	if err := r.Append(t.Context(), scope, msg, settings); err != nil {
		t.Fatal(err)
	}
	physicalCount := func(want int) {
		t.Helper()
		var got int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM time_series_entries").Scan(&got); err != nil || got != want {
			t.Fatalf("physical count = %d, want %d, err=%v", got, want, err)
		}
	}
	physicalCount(1)
	settings.RetentionDays = 1
	if err := r.ApplySettings(t.Context(), scope, settings); err != nil {
		t.Fatal(err)
	}
	physicalCount(0)
	msg.SentAt = now.Add(-time.Hour)
	if err := r.Append(t.Context(), scope, msg, settings); err != nil {
		t.Fatal(err)
	}
	reopenedStore, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedStore.Close()
	reopened := New(reopenedStore)
	reopened.now = r.now
	page, err := reopened.Query(t.Context(), scope, messaging.GroupHistoryQuery{})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "stored" {
		t.Fatal("second process cannot read persisted history", page, err)
	}
	now = now.Add(48 * time.Hour)
	page, err = reopened.Query(t.Context(), scope, messaging.GroupHistoryQuery{})
	if err != nil || len(page.Messages) != 0 {
		t.Fatal("expired data visible before worker prune", page, err)
	}
	physicalCount(1)
	if err := r.Prune(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	physicalCount(0)

	// A storage failure must roll back both scope creation and message insertion.
	if _, err := pool.Exec(t.Context(), `ALTER TABLE time_series_entries ADD CONSTRAINT reject_test_message CHECK (search_text <> 'reject')`); err != nil {
		t.Fatal(err)
	}
	badScope := scope
	badScope.Account = "new-account"
	msg.SentAt, msg.Text = now, "reject"
	if err := r.Append(t.Context(), badScope, msg, settings); err == nil {
		t.Fatal("constraint should reject message")
	}
	var scopes int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM time_series_scopes WHERE scope=$1`, scopeKey(badScope)).Scan(&scopes); err != nil || scopes != 0 {
		t.Fatal("partial scope transaction committed", scopes, err)
	}
	physicalCount(0)
	var definition string
	if err := pool.QueryRow(t.Context(), `SELECT indexdef FROM pg_indexes WHERE schemaname=current_schema() AND indexname='time_series_range_idx'`).Scan(&definition); err != nil || !strings.Contains(definition, "namespace, scope, occurred_at DESC") {
		t.Fatal("group time range index missing", definition, err)
	}
}

func TestPostgresGroupHistoryConcurrentFirstMigration(t *testing.T) {
	_, pool, dsn := postgresFixture(t)
	if _, err := pool.Exec(t.Context(), `DROP TABLE time_series_entries; DROP TABLE time_series_scopes`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			s, err := store.Open(t.Context(), dsn)
			if err != nil {
				t.Error(err)
				return
			}
			s.Close()
		})
	}
	wg.Wait()
}

func TestPostgresGroupHistoryDefaultBatchRetentionPhysicalRows(t *testing.T) {
	s, pool, dsn := postgresFixture(t)
	r := New(s)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	scope := messaging.GroupScope{Route: "onebot", Account: "bot", RoomID: "default-batch-room"}
	settings := messaging.DefaultGroupHistorySettings()
	settings.Enabled, settings.RoomIDs = true, []string{scope.RoomID}
	if settings.MaxMessages != 10000 {
		t.Fatalf("default retention target = %d, want 10000", settings.MaxMessages)
	}
	if err := r.ApplySettings(t.Context(), scope, settings); err != nil {
		t.Fatal(err)
	}
	// Seed the existing history in one statement; operations under test still
	// go through the public repository and its transaction/retention contract.
	base := now.Add(-2 * time.Minute)
	_, err := pool.Exec(t.Context(), `INSERT INTO time_series_entries(namespace,scope,id,occurred_at,source,search_text,data)
		SELECT $1,$2,'default-' || lpad(n::text,5,'0'),$3::timestamptz+n*interval '1 millisecond','alice','message',
		jsonb_build_object('id','default-' || lpad(n::text,5,'0'),'senderId','alice','text','message',
		'sentAt',$3::timestamptz+n*interval '1 millisecond','direction',1)
		FROM generate_series(0,10098) AS n`, namespace, scopeKey(scope), base)
	if err != nil {
		t.Fatal(err)
	}
	physicalRange := func(wantCount int, wantFirst, wantLast string) {
		t.Helper()
		var count int
		var first, last string
		err := pool.QueryRow(t.Context(), `SELECT count(*),min(id),max(id) FROM time_series_entries WHERE namespace=$1 AND scope=$2`, namespace, scopeKey(scope)).Scan(&count, &first, &last)
		if err != nil || count != wantCount || first != wantFirst || last != wantLast {
			t.Fatalf("physical rows = (%d, %q, %q), want (%d, %q, %q); err=%v", count, first, last, wantCount, wantFirst, wantLast, err)
		}
	}
	physicalRange(10099, "default-00000", "default-10098")
	duplicate := messaging.GroupMessage{ID: "default-10098", SenderID: "alice", Text: "duplicate must not replace text", SentAt: base.Add(10098 * time.Millisecond), Direction: messaging.GroupMessageDirectionIncoming}
	for range 3 {
		if err := r.Append(t.Context(), scope, duplicate, settings); err != nil {
			t.Fatal(err)
		}
		if err := r.ApplySettings(t.Context(), scope, settings); err != nil {
			t.Fatal(err)
		}
		if err := r.Prune(t.Context(), now); err != nil {
			t.Fatal(err)
		}
		physicalRange(10099, "default-00000", "default-10098")
	}
	// Reopening the store must neither reset an in-memory counter nor hide the
	// 99 records already persisted above the target.
	reopenedStore, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedStore.Close()
	reopened := New(reopenedStore)
	reopened.now = r.now
	page, err := reopened.Query(t.Context(), scope, messaging.GroupHistoryQuery{Limit: 1})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != duplicate.ID || page.Messages[0].Text != "message" {
		t.Fatal("latest persisted message was lost or overwritten", page, err)
	}
	next := duplicate
	next.ID, next.Text, next.SentAt = "default-10099", "new message", base.Add(10099*time.Millisecond)
	if err := reopened.Append(t.Context(), scope, next, settings); err != nil {
		t.Fatal(err)
	}
	physicalRange(10000, "default-00100", "default-10099")
}

func TestPostgresGroupHistoryBatchDeletionFailureRollsBackAppend(t *testing.T) {
	s, pool, _ := postgresFixture(t)
	r := New(s)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	scope := messaging.GroupScope{Route: "onebot", Account: "bot", RoomID: "rollback-room"}
	settings := messaging.DefaultGroupHistorySettings()
	settings.Enabled, settings.RoomIDs, settings.MaxMessages = true, []string{scope.RoomID}, 100
	for i := range 199 {
		appendRetentionMessage(t, r, scope, settings, i, now)
	}
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_retention_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'retention deletion test failure'; END $$;
		CREATE TRIGGER reject_retention_delete BEFORE DELETE ON time_series_entries
		FOR EACH ROW EXECUTE FUNCTION reject_retention_delete()`); err != nil {
		t.Fatal(err)
	}
	msg := messaging.GroupMessage{ID: "batch-0199", SenderID: "alice", Text: "group history", SentAt: now.Add(-801 * time.Millisecond), Direction: messaging.GroupMessageDirectionIncoming}
	if err := r.Append(t.Context(), scope, msg, settings); err == nil {
		t.Fatal("retention deletion failure was not returned")
	}
	// The failed transaction must not commit the new record or any old-record
	// deletions; retrying the same message ID must complete the full operation.
	assertRetentionRange(t, r, scope, 0, 199)
	var count, newRecord int
	if err := pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE id='batch-0199') FROM time_series_entries`).Scan(&count, &newRecord); err != nil || count != 199 || newRecord != 0 {
		t.Fatalf("failed transaction left count=%d newRecord=%d err=%v", count, newRecord, err)
	}
	if _, err := pool.Exec(t.Context(), `DROP TRIGGER reject_retention_delete ON time_series_entries`); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(t.Context(), scope, msg, settings); err != nil {
		t.Fatal(err)
	}
	assertRetentionRange(t, r, scope, 100, 200)
}
