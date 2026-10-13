package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/infra/store"
)

func TestMemoryGroupHistory(t *testing.T) {
	t.Parallel()
	runRepositoryContract(t, func(t *testing.T) store.Store { return store.NewMemory() })
}

func runRepositoryContract(t *testing.T, newStore func(*testing.T) store.Store) {
	t.Helper()
	fixture := func(t *testing.T) (*Repository, messaging.GroupScope, messaging.GroupHistorySettings, *time.Time) {
		t.Helper()
		now := time.Date(2026, 10, 14, 12, 0, 0, 123456000, time.UTC)
		r := New(newStore(t))
		r.now = func() time.Time { return now }
		scope := messaging.GroupScope{Route: "onebot", Account: "bot", RoomID: t.Name()}
		settings := messaging.DefaultGroupHistorySettings()
		settings.Enabled, settings.RoomIDs = true, []string{scope.RoomID}
		if err := r.ApplySettings(t.Context(), scope, settings); err != nil {
			t.Fatal(err)
		}
		return r, scope, settings, &now
	}
	appendMessage := func(t *testing.T, r *Repository, scope messaging.GroupScope, settings messaging.GroupHistorySettings, id, sender, text string, when time.Time) {
		t.Helper()
		err := r.Append(t.Context(), scope, messaging.GroupMessage{ID: id, SenderID: sender, Text: text, SentAt: when, Direction: messaging.GroupMessageDirectionIncoming}, settings)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Run("stable pagination isolation and filters", func(t *testing.T) {
		r, scope, settings, now := fixture(t)
		for _, id := range []string{"a", "b", "c", "d", "e"} {
			appendMessage(t, r, scope, settings, id, "alice", "100% 'quoted' _ release", now.Add(-time.Minute))
		}
		appendMessage(t, r, scope, settings, "f", "bob", "newest", *now)
		first, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{Limit: 2})
		if err != nil || !first.HasMore || first.NextCursor == "" || !reflect.DeepEqual(messageIDs(first), []string{"f", "e"}) {
			t.Fatal(first, err)
		}
		// Later arrivals do not shift an established pagination time boundary.
		*now = now.Add(time.Minute)
		appendMessage(t, r, scope, settings, "g", "carol", "arrived later", *now)
		ids := messageIDs(first)
		cursor := first.NextCursor
		for cursor != "" {
			page, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, messageIDs(page)...)
			cursor = page.NextCursor
		}
		if !reflect.DeepEqual(ids, []string{"f", "e", "d", "c", "b", "a"}) {
			t.Fatal("unstable pages", ids)
		}
		filtered, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{SenderID: "alice", Keyword: "% 'quoted' _", Limit: 2})
		if err != nil || !reflect.DeepEqual(messageIDs(filtered), []string{"e", "d"}) {
			t.Fatal(filtered, err)
		}
		if _, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{Cursor: filtered.NextCursor, Keyword: "different"}); err == nil {
			t.Fatal("cursor accepted changed filter")
		}
		for _, edit := range []func(*messaging.GroupScope){
			func(s *messaging.GroupScope) { s.Account = "other" },
			func(s *messaging.GroupScope) { s.Route = "custom" },
			func(s *messaging.GroupScope) { s.RoomID = "other" },
		} {
			other := scope
			edit(&other)
			page, err := r.Query(t.Context(), other, messaging.GroupHistoryQuery{})
			if err != nil || len(page.Messages) != 0 {
				t.Fatal("scope leaked", page, err)
			}
			if _, err := r.Query(t.Context(), other, messaging.GroupHistoryQuery{Cursor: first.NextCursor}); err == nil {
				t.Fatal("cross-scope cursor accepted")
			}
		}
	})
	t.Run("concurrent replay preserves original and quota", func(t *testing.T) {
		r, scope, settings, now := fixture(t)
		settings.MaxMessages = 100
		if err := r.ApplySettings(t.Context(), scope, settings); err != nil {
			t.Fatal(err)
		}
		appendMessage(t, r, scope, settings, "same", "alice", "original", *now)
		var wg sync.WaitGroup
		for i := range 199 {
			wg.Go(func() {
				appendMessage(t, r, scope, settings, fmt.Sprintf("%03d", i), "alice", "entry", now.Add(-time.Duration(i+1)*time.Second))
				appendMessage(t, r, scope, settings, "same", "mallory", "rewritten", now.Add(-time.Hour))
			})
		}
		wg.Wait()
		page, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{Limit: 200})
		if err != nil || len(page.Messages) != 100 || page.Messages[0].ID != "same" || page.Messages[0].Text != "original" || page.Messages[0].SenderID != "alice" {
			t.Fatal("dedup/quota failed", len(page.Messages), err)
		}
		if !page.Coverage.Limited || page.Coverage.MaxMessages != 100 {
			t.Fatal("quota loss not reported", page.Coverage)
		}
	})
	runBatchRetentionContract(t, fixture)
	t.Run("retention policy immediate and disabled cleanup", func(t *testing.T) {
		r, scope, settings, now := fixture(t)
		originalStart := *now
		empty, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{})
		if err != nil || !empty.Coverage.RecordedFrom.Equal(originalStart) || !empty.Coverage.LocalOnly {
			t.Fatal("empty recording start lost", empty, err)
		}
		appendMessage(t, r, scope, settings, "old", "alice", "expired", now.Add(-48*time.Hour))
		appendMessage(t, r, scope, settings, "recent", "alice", "kept", now.Add(-time.Hour))
		short := settings
		short.RetentionDays, short.Enabled = 1, false
		if err := r.ApplySettings(t.Context(), scope, short); err != nil {
			t.Fatal(err)
		}
		// A callback carrying the old 7-day policy must not restore expired data.
		appendMessage(t, r, scope, settings, "old", "alice", "replayed", now.Add(-48*time.Hour))
		page, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{})
		if err != nil || !reflect.DeepEqual(messageIDs(page), []string{"recent"}) || page.Coverage.RetentionDays != 1 || !page.Coverage.RecordedFrom.Equal(originalStart) {
			t.Fatal(page, err)
		}
		*now = now.Add(48 * time.Hour)
		page, err = r.Query(t.Context(), scope, messaging.GroupHistoryQuery{})
		if err != nil || len(page.Messages) != 0 {
			t.Fatal("query leaked expired record before prune", page, err)
		}
		if err := r.Prune(t.Context(), *now); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("bounded unicode observation and cursor progresses", func(t *testing.T) {
		r, scope, settings, now := fixture(t)
		for _, id := range []string{"a", "b", "c"} {
			appendMessage(t, r, scope, settings, id, "alice", strings.Repeat("群聊<&\"内容", 4000), *now)
		}
		page, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{Limit: 3, MaxBytes: 2048})
		data, _ := json.Marshal(page)
		if err != nil || len(data) > 2048 || len(page.Messages) != 1 || !page.Truncated || !page.Messages[0].Truncated || !utf8.ValidString(page.Messages[0].Text) || !page.HasMore {
			t.Fatal("unbounded observation", len(data), page, err)
		}
		next, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{Cursor: page.NextCursor, MaxBytes: 2048})
		if err != nil || len(next.Messages) != 1 || next.Messages[0].ID == page.Messages[0].ID {
			t.Fatal("cursor did not progress", next, err)
		}
	})
	t.Run("cancelled query", func(t *testing.T) {
		r, scope, _, _ := fixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := r.Query(ctx, scope, messaging.GroupHistoryQuery{}); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func messageIDs(page messaging.GroupHistoryPage) []string {
	ids := make([]string, 0, len(page.Messages))
	for _, msg := range page.Messages {
		ids = append(ids, msg.ID)
	}
	return ids
}

func TestGroupHistoryInvalidInputsAndUnsupportedStore(t *testing.T) {
	t.Parallel()
	r := New(store.NewMemory())
	scope := messaging.GroupScope{Route: "onebot", Account: "bot", RoomID: "room"}
	for _, tc := range []struct {
		name string
		q    messaging.GroupHistoryQuery
	}{
		{"negative limit", messaging.GroupHistoryQuery{Limit: -1}},
		{"large limit", messaging.GroupHistoryQuery{Limit: 201}},
		{"small budget", messaging.GroupHistoryQuery{MaxBytes: 100}},
		{"large budget", messaging.GroupHistoryQuery{MaxBytes: 65537}},
		{"bad cursor", messaging.GroupHistoryQuery{Cursor: "%%%"}},
		{"huge cursor", messaging.GroupHistoryQuery{Cursor: strings.Repeat("x", 4097)}},
		{"backward range", messaging.GroupHistoryQuery{After: time.Now().Add(time.Hour), Before: time.Now()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.Query(t.Context(), scope, tc.q); err == nil {
				t.Fatal("invalid query accepted")
			}
		})
	}
	wrapper := struct{ store.Store }{store.NewMemory()}
	unsupported := New(wrapper)
	if _, err := unsupported.Query(t.Context(), scope, messaging.GroupHistoryQuery{}); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if err := unsupported.Prune(t.Context(), time.Now()); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	settings := messaging.DefaultGroupHistorySettings()
	msg := messaging.GroupMessage{ID: "one", SenderID: "peer", Text: "hi", SentAt: time.Now(), Direction: messaging.GroupMessageDirectionIncoming}
	if err := r.Append(t.Context(), scope, msg, settings); err == nil {
		t.Fatal("disabled collector accepted input")
	}
	settings.Enabled, settings.RoomIDs = true, []string{"room"}
	msg.Direction = messaging.GroupMessageDirectionUnknown
	if err := r.Append(t.Context(), scope, msg, settings); err == nil {
		t.Fatal("unknown direction accepted")
	}
}

func TestGroupHistoryBudgetIncludesCursorAndMetadata(t *testing.T) {
	t.Parallel()
	r := New(store.NewMemory())
	scope := messaging.GroupScope{Route: "onebot", Account: "bot", RoomID: "room"}
	settings := messaging.DefaultGroupHistorySettings()
	settings.Enabled, settings.RoomIDs = true, []string{"room"}
	keyword, sender := strings.Repeat("x", 256), strings.Repeat("p", 256)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, prefix := range []string{"a", "b"} {
		err := r.Append(t.Context(), scope, messaging.GroupMessage{
			ID: prefix + strings.Repeat("i", 255), SenderID: sender, SenderName: strings.Repeat("n", 256),
			Text: keyword, SentAt: now, Direction: messaging.GroupMessageDirectionIncoming,
		}, settings)
		if err != nil {
			t.Fatal(err)
		}
	}
	if page, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{Limit: 1, Keyword: keyword, SenderID: sender, MaxBytes: 2048}); err == nil {
		data, _ := json.Marshal(page)
		t.Fatalf("expected explicit budget error for oversized cursor and metadata, got %d bytes", len(data))
	}
	page, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{Limit: 1, Keyword: keyword, SenderID: sender, MaxBytes: 4096})
	data, _ := json.Marshal(page)
	if err != nil || len(data) > 4096 || len(page.Messages) != 1 || !page.HasMore {
		t.Fatal("larger budget should work", len(data), err)
	}
}
