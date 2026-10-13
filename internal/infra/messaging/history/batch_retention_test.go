package history

import (
	"fmt"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain/messaging"
)

type retentionFixture func(*testing.T) (*Repository, messaging.GroupScope, messaging.GroupHistorySettings, *time.Time)

func runBatchRetentionContract(t *testing.T, fixture retentionFixture) {
	t.Helper()
	for _, target := range []int{100, 137} {
		t.Run(fmt.Sprintf("batch retention target %d", target), func(t *testing.T) {
			r, scope, settings, now := fixture(t)
			settings.MaxMessages = target
			if err := r.ApplySettings(t.Context(), scope, settings); err != nil {
				t.Fatal(err)
			}
			for i := range target + 99 {
				appendRetentionMessage(t, r, scope, settings, i, *now)
			}
			assertRetentionRange(t, r, scope, 0, target+99)
			// Replaying IDs, periodic housekeeping and unchanged settings must
			// not turn the retention target back into a strict per-write cap.
			for range 3 {
				appendRetentionMessage(t, r, scope, settings, target+98, *now)
				if err := r.ApplySettings(t.Context(), scope, settings); err != nil {
					t.Fatal(err)
				}
				if err := r.Prune(t.Context(), *now); err != nil {
					t.Fatal(err)
				}
				assertRetentionRange(t, r, scope, 0, target+99)
			}
			appendRetentionMessage(t, r, scope, settings, target+99, *now)
			assertRetentionRange(t, r, scope, 100, target+100)
			// The next cycle starts from actual retained records, not a count
			// of callbacks or a stale counter left over from the prior batch.
			appendRetentionMessage(t, r, scope, settings, target+100, *now)
			assertRetentionRange(t, r, scope, 100, target+101)
		})
	}
	t.Run("batch retention is scoped by route account and room", func(t *testing.T) {
		r, scope, settings, now := fixture(t)
		settings.MaxMessages = 100
		if err := r.ApplySettings(t.Context(), scope, settings); err != nil {
			t.Fatal(err)
		}
		others := []messaging.GroupScope{scope, scope, scope}
		others[0].Route = "other-route"
		others[1].Account = "other-account"
		others[2].RoomID = "other-room"
		for _, other := range others {
			otherSettings := settings
			otherSettings.RoomIDs = []string{other.RoomID}
			for i := range 3 {
				appendRetentionMessage(t, r, other, otherSettings, i, *now)
			}
		}
		for i := range 200 {
			appendRetentionMessage(t, r, scope, settings, i, *now)
		}
		assertRetentionRange(t, r, scope, 100, 200)
		for _, other := range others {
			assertRetentionRange(t, r, other, 0, 3)
		}
	})
	t.Run("decreasing target trims immediately and stale appends cannot restore it", func(t *testing.T) {
		r, scope, settings, now := fixture(t)
		settings.MaxMessages = 150
		if err := r.ApplySettings(t.Context(), scope, settings); err != nil {
			t.Fatal(err)
		}
		for i := range 199 {
			appendRetentionMessage(t, r, scope, settings, i, *now)
		}
		reduced := settings
		reduced.MaxMessages = 100
		if err := r.ApplySettings(t.Context(), scope, reduced); err != nil {
			t.Fatal(err)
		}
		assertRetentionRange(t, r, scope, 99, 199)
		appendRetentionMessage(t, r, scope, settings, 199, *now)
		page := assertRetentionRange(t, r, scope, 99, 200)
		if page.Coverage.MaxMessages != 100 {
			t.Fatalf("stale append changed target: %+v", page.Coverage)
		}
		if err := r.ApplySettings(t.Context(), scope, reduced); err != nil {
			t.Fatal(err)
		}
		assertRetentionRange(t, r, scope, 99, 200)
	})
	t.Run("expired messages are removed before counting the batch threshold", func(t *testing.T) {
		r, scope, settings, now := fixture(t)
		settings.MaxMessages, settings.RetentionDays = 100, 1
		if err := r.ApplySettings(t.Context(), scope, settings); err != nil {
			t.Fatal(err)
		}
		// Forty records expire together; the remaining 159 records must not
		// lose an extra quantity batch when the 200th lifetime message arrives.
		oldBase := now.Add(-24*time.Hour + time.Minute)
		for i := range 40 {
			appendRetentionMessage(t, r, scope, settings, i, oldBase)
		}
		for i := 40; i < 199; i++ {
			appendRetentionMessage(t, r, scope, settings, i, *now)
		}
		assertRetentionRange(t, r, scope, 0, 199)
		*now = now.Add(2 * time.Minute)
		appendRetentionMessage(t, r, scope, settings, 199, *now)
		assertRetentionRange(t, r, scope, 40, 200)
		if err := r.Prune(t.Context(), *now); err != nil {
			t.Fatal(err)
		}
		assertRetentionRange(t, r, scope, 40, 200)
	})
}

func appendRetentionMessage(t *testing.T, r *Repository, scope messaging.GroupScope, settings messaging.GroupHistorySettings, index int, now time.Time) {
	t.Helper()
	err := r.Append(t.Context(), scope, messaging.GroupMessage{
		ID: fmt.Sprintf("batch-%04d", index), SenderID: "alice", Text: "group history",
		SentAt: now.Add(time.Duration(index-1000) * time.Millisecond), Direction: messaging.GroupMessageDirectionIncoming,
	}, settings)
	if err != nil {
		t.Fatal(err)
	}
}

func assertRetentionRange(t *testing.T, r *Repository, scope messaging.GroupScope, start, end int) messaging.GroupHistoryPage {
	t.Helper()
	var result messaging.GroupHistoryPage
	var messages []messaging.GroupMessage
	cursor := ""
	for {
		page, err := r.Query(t.Context(), scope, messaging.GroupHistoryQuery{Limit: 200, MaxBytes: 64 * 1024, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		result = page
		messages = append(messages, page.Messages...)
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			t.Fatal("pagination did not progress")
		}
		cursor = page.NextCursor
	}
	if len(messages) != end-start {
		t.Fatalf("retained %d messages, want %d (range %d..%d)", len(messages), end-start, start, end-1)
	}
	for i, message := range messages {
		want := fmt.Sprintf("batch-%04d", end-i-1)
		if message.ID != want {
			t.Fatalf("message[%d] = %q, want %q", i, message.ID, want)
		}
	}
	return result
}
