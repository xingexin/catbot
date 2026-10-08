package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xingexin/catbot/internal/infra/idgen"
)

// A Store wrapper that exposes only the original Store contract.
type legacyOutboxStore struct{ Store }

func seedOutbox(t *testing.T, s Store) {
	t.Helper()
	base := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	for _, run := range []runRecord{
		{ID: "queued", ReplyPending: true, Status: "queued", CreatedAt: base.Add(-time.Hour)},
		{ID: "running", ReplyPending: true, Status: "running", CreatedAt: base.Add(-time.Hour)},
		{ID: "background-pending", ReplyPending: true, Status: "completed", CreatedAt: base.Add(-time.Hour)},
		{ID: "already-replied", Status: "completed", CreatedAt: base.Add(-time.Hour)},
		{ID: "reply-late", ReplyPending: true, Status: "completed", CreatedAt: base.Add(time.Hour)},
		{ID: "reply-fraction", ReplyPending: true, Status: "interrupted", CreatedAt: base.Add(100 * time.Millisecond)},
		{ID: "reply-b", ReplyPending: true, Status: "failed", CreatedAt: base},
		{ID: "reply-a", ReplyPending: true, Status: "cancelled", CreatedAt: base.In(time.FixedZone("offset", 8*3600))},
	} {
		if err := s.Put(t.Context(), "run", run.ID, run); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range []map[string]any{
		{"id": "notice-sent", "status": "sent", "createdAt": base.Add(-time.Hour)},
		{"id": "notice-saved", "status": "saved", "createdAt": base.Add(-time.Hour)},
		{"id": "notice-failed", "status": "failed", "createdAt": base.Add(-time.Hour)},
		{"id": "notice-uncertain", "status": "uncertain", "createdAt": base.Add(-time.Hour)},
		{"id": "notice-late", "status": "pending", "createdAt": base.Add(time.Hour)},
		{"id": "notice-fraction", "status": "pending", "createdAt": base.Add(100 * time.Millisecond)},
		{"id": "notice-b", "status": "pending", "createdAt": base},
		{"id": "notice-a", "status": "pending", "createdAt": base.In(time.FixedZone("offset", 8*3600)), "text": "保留完整通知正文", "operationId": "stable-op", "extensions": map[string]any{"version": 3}},
	} {
		if err := s.Put(t.Context(), "notification", record["id"].(string), record); err != nil {
			t.Fatal(err)
		}
	}
}

func notificationIDs(t *testing.T, records []json.RawMessage) []string {
	t.Helper()
	result := make([]string, 0, len(records))
	for _, raw := range records {
		var record struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		result = append(result, record.ID)
	}
	return result
}

func outboxReplyIDs(runs []json.RawMessage) []string {
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, storedRunID(run))
	}
	return ids
}

func checkOutboxSelection(t *testing.T, s Store) {
	t.Helper()
	replies, err := PendingReplies(t.Context(), s, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := outboxReplyIDs(replies); !slices.Equal(got, []string{"reply-a", "reply-b", "reply-fraction"}) {
		t.Fatal("reply filter/order/limit failed", got)
	}
	allReplies, err := PendingReplies(t.Context(), s, 128)
	if err != nil || len(allReplies) != 4 {
		t.Fatal("unexpected reply candidates", allReplies, err)
	}
	notifications, err := PendingNotifications(t.Context(), s, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := notificationIDs(t, notifications); !slices.Equal(got, []string{"notice-a", "notice-b", "notice-fraction"}) {
		t.Fatal("notification filter/order/limit failed", got)
	}
	var first map[string]any
	if err := json.Unmarshal(notifications[0], &first); err != nil {
		t.Fatal(err)
	}
	if first["text"] != "保留完整通知正文" || first["operationId"] != "stable-op" || first["extensions"].(map[string]any)["version"] != float64(3) {
		t.Fatal("notification payload was not preserved", first)
	}
	allNotifications, err := PendingNotifications(t.Context(), s, 128)
	if err != nil || len(allNotifications) != 4 {
		t.Fatal("unexpected notification candidates", notificationIDs(t, allNotifications), err)
	}

	var done runRecord
	if err := s.Get(t.Context(), "run", "reply-a", &done); err != nil {
		t.Fatal(err)
	}
	done.ReplyPending = false
	if err := s.Put(t.Context(), "run", done.ID, done); err != nil {
		t.Fatal(err)
	}
	first["status"] = "sent"
	if err := s.Put(t.Context(), "notification", "notice-a", first); err != nil {
		t.Fatal(err)
	}
	replies, err = PendingReplies(t.Context(), s, 1)
	if err != nil || len(replies) != 1 || storedRunID(replies[0]) != "reply-b" {
		t.Fatal("reconciled reply returned again", replies, err)
	}
	notifications, err = PendingNotifications(t.Context(), s, 1)
	if err != nil || !slices.Equal(notificationIDs(t, notifications), []string{"notice-b"}) {
		t.Fatal("reconciled notification returned again", notifications, err)
	}
}

func TestPendingOutboxFiltersBeforeLimitAndPreservesPayload(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory", "legacy Store wrapper"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var s Store = NewMemory()
			if name != "memory" {
				s = legacyOutboxStore{s}
			}
			seedOutbox(t, s)
			checkOutboxSelection(t, s)
		})
	}
}

func fillOutbox(t *testing.T, s Store) {
	t.Helper()
	for i := range 160 {
		id := fmt.Sprintf("batch-%03d", i)
		created := time.Date(2027, 1, 1, 0, 0, i, 0, time.UTC)
		if err := s.Put(t.Context(), "run", id, runRecord{ID: id, Status: "completed", ReplyPending: true, CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(t.Context(), "notification", id, map[string]any{"id": id, "status": "pending", "createdAt": created}); err != nil {
			t.Fatal(err)
		}
	}
}

func checkOutboxBoundsAndCancellation(t *testing.T, s Store) {
	t.Helper()
	for _, tc := range []struct{ limit, want int }{{0, 32}, {-1, 32}, {1, 1}, {128, 128}, {99999, 128}} {
		t.Run(fmt.Sprint(tc.limit), func(t *testing.T) {
			replies, err := PendingReplies(t.Context(), s, tc.limit)
			if err != nil || len(replies) != tc.want {
				t.Fatal("reply batch is not bounded", len(replies), tc.want, err)
			}
			notifications, err := PendingNotifications(t.Context(), s, tc.limit)
			if err != nil || len(notifications) != tc.want {
				t.Fatal("notification batch is not bounded", len(notifications), tc.want, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := PendingReplies(ctx, s, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("reply cancellation lost", err)
	}
	if _, err := PendingNotifications(ctx, s, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("notification cancellation lost", err)
	}
}

func TestPendingOutboxBoundsAndCancellation(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	fillOutbox(t, s)
	checkOutboxBoundsAndCancellation(t, s)
}

type optionalOutboxReader struct {
	Store
	replyLimit, notificationLimit int
	err                           error
}

func (s *optionalOutboxReader) PendingReplies(_ context.Context, limit int) ([]json.RawMessage, error) {
	s.replyLimit = limit
	return nil, s.err
}

func (s *optionalOutboxReader) PendingNotifications(_ context.Context, limit int) ([]json.RawMessage, error) {
	s.notificationLimit = limit
	return nil, s.err
}

func TestPendingOutboxUsesOptionalBoundedReaders(t *testing.T) {
	t.Parallel()
	failure := errors.New("query failed")
	s := &optionalOutboxReader{err: failure}
	if _, err := PendingReplies(t.Context(), s, 999); !errors.Is(err, failure) || s.replyLimit != 128 {
		t.Fatal("optional reply query not used", s.replyLimit, err)
	}
	if _, err := PendingNotifications(t.Context(), s, 0); !errors.Is(err, failure) || s.notificationLimit != 32 {
		t.Fatal("optional notification query not used", s.notificationLimit, err)
	}
}

type cancelOutboxOnList struct {
	Store
	cancel context.CancelFunc
}

func (s cancelOutboxOnList) List(ctx context.Context, kind string) ([]json.RawMessage, error) {
	raw, err := s.Store.List(ctx, kind)
	s.cancel()
	return raw, err
}

func TestPendingOutboxCancellationDuringFallbackRead(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"reply", "notification"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s := cancelOutboxOnList{Store: NewMemory(), cancel: cancel}
			seedOutbox(t, s)
			var err error
			if name == "reply" {
				_, err = PendingReplies(ctx, s, 32)
			} else {
				_, err = PendingNotifications(ctx, s, 32)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation during List was ignored", err)
			}
		})
	}
}

func TestPostgresPendingOutbox(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for real PostgreSQL outbox integration")
	}
	if !strings.Contains(dsn, "/secretary_test") {
		t.Fatal("integration database must be named secretary_test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "outbox_test_" + strings.ReplaceAll(idgen.New(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	s, err := Open(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedOutbox(t, s)
	checkOutboxSelection(t, s)
	fillOutbox(t, s)
	checkOutboxBoundsAndCancellation(t, s)
}
