package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestMemoryPurgeDeletesPayloadsAndEventsAndRetainsReplayGuards(t *testing.T) {
	s := NewMemory()
	ctx := t.Context()
	refs := []RecordRef{{Kind: "run", ID: "remove"}, {Kind: "session", ID: "remove"}, {Kind: "archive", ID: "1:remove"}}
	for _, ref := range append(refs, RecordRef{Kind: "run", ID: "keep"}) {
		if err := s.Put(ctx, ref.Kind, ref.ID, map[string]string{"text": "private contents"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"remove", "keep", "remove"} {
		if err := s.Append(ctx, EventRecord{RunID: id, Type: "test", Time: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Purge(ctx, s, refs, []string{"remove"}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		var payload map[string]any
		if err := s.Get(ctx, ref.Kind, ref.ID, &payload); !errors.Is(err, ErrNotFound) {
			t.Fatal("purged content survived", ref, payload, err)
		}
		if purged, err := Purged(ctx, s, ref.Kind, ref.ID); err != nil || !purged {
			t.Fatal("missing replay guard", ref, purged, err)
		}
	}
	guards, err := s.List(ctx, PurgedRecordKind)
	if err != nil || len(guards) != len(refs) {
		t.Fatal(guards, err)
	}
	for _, guard := range guards {
		var payload map[string]any
		if err := json.Unmarshal(guard, &payload); err != nil || len(payload) != 3 || payload["kind"] == nil || payload["id"] == nil || payload["purgedAt"] == nil {
			t.Fatal("guard must retain only minimal identity and timestamp", payload, err)
		}
	}
	if err := Purge(ctx, s, refs, []string{"remove"}); err != nil {
		t.Fatal(err)
	}
	again, err := s.List(ctx, PurgedRecordKind)
	if err != nil || !reflect.DeepEqual(guards, again) {
		t.Fatal("retry changed first purge timestamp", err)
	}
	events, err := s.Events(ctx, "remove", 0)
	if err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
	if err := s.Append(ctx, EventRecord{RunID: "keep", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	events, err = s.Events(ctx, "keep", 2)
	if err != nil || len(events) != 1 || events[0].Sequence != 4 {
		t.Fatal("event sequence was reused after removal", events, err)
	}
	var kept map[string]any
	if err := s.Get(ctx, "run", "keep", &kept); err != nil {
		t.Fatal("unrelated content deleted", err)
	}
}

func TestPurgeInvalidOrCancelledRequestDoesNotPartiallyDelete(t *testing.T) {
	cases := []struct {
		name   string
		refs   []RecordRef
		runs   []string
		cancel bool
	}{
		{name: "invalid later ref", refs: []RecordRef{{Kind: "run", ID: "row"}, {Kind: "run"}}},
		{name: "invalid run", refs: []RecordRef{{Kind: "run", ID: "row"}}, runs: []string{""}},
		{name: "guard deletion", refs: []RecordRef{{Kind: "run", ID: "row"}, {Kind: PurgedRecordKind, ID: "guard"}}},
		{name: "cancelled", refs: []RecordRef{{Kind: "run", ID: "row"}}, cancel: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemory()
			if err := s.Put(t.Context(), "run", "row", true); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			if err := Purge(ctx, s, tc.refs, tc.runs); err == nil {
				t.Fatal("invalid purge succeeded")
			}
			var row bool
			if err := s.Get(t.Context(), "run", "row", &row); err != nil || !row {
				t.Fatal("partial purge", row, err)
			}
			guards, err := s.List(t.Context(), PurgedRecordKind)
			if err != nil || len(guards) != 0 {
				t.Fatal("partial guard write", guards, err)
			}
		})
	}
}

func TestPurgeUnsupportedAdapterDoesNotFallBackToSequentialDeletes(t *testing.T) {
	memory := NewMemory()
	if err := memory.Put(t.Context(), "task", "id", true); err != nil {
		t.Fatal(err)
	}
	wrapped := struct{ Store }{Store: memory}
	if err := Purge(t.Context(), wrapped, []RecordRef{{Kind: "task", ID: "id"}}, nil); !errors.Is(err, ErrAtomicPurgeUnsupported) {
		t.Fatal(err)
	}
	var exists bool
	if err := memory.Get(t.Context(), "task", "id", &exists); err != nil || !exists {
		t.Fatal(exists, err)
	}
}

func TestMemoryPurgeIsAtomicForConcurrentReaders(t *testing.T) {
	s := NewMemory()
	refs := make([]RecordRef, 200)
	for i := range refs {
		refs[i] = RecordRef{Kind: "task", ID: time.Unix(int64(i), 0).Format(time.RFC3339)}
		if err := s.Put(t.Context(), refs[i].Kind, refs[i].ID, true); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				rows, err := s.List(t.Context(), "task")
				if err != nil || (len(rows) != 0 && len(rows) != len(refs)) {
					t.Error("partial batch became visible", len(rows), err)
				}
			}
		})
	}
	if err := Purge(t.Context(), s, refs, nil); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func TestPurgeKeyHasUnambiguousIdentity(t *testing.T) {
	if PurgeKey("a:b", "c") == PurgeKey("a", "b:c") || PurgeKey("run", "id") != PurgeKey("run", "id") {
		t.Fatal("unstable or ambiguous purge keys")
	}
}

func TestMemoryPurgedOperationsRejectStaleRowsAndEvents(t *testing.T) {
	s := NewMemory()
	ctx := t.Context()
	if err := s.Put(ctx, "run", "operation", map[string]string{"status": "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := Purge(ctx, s, []RecordRef{{Kind: "run", ID: "operation"}}, []string{"operation"}); err != nil {
		t.Fatal(err)
	}
	var writers sync.WaitGroup
	for range 20 {
		writers.Go(func() {
			if err := s.Put(ctx, "run", "operation", map[string]string{"result": "stale result"}); !errors.Is(err, ErrPurgedRecord) {
				t.Error("late row write escaped guard", err)
			}
			if err := s.Append(ctx, EventRecord{RunID: "operation", Type: "completed", Data: map[string]any{"text": "stale event"}}); !errors.Is(err, ErrPurgedRecord) {
				t.Error("late event escaped guard", err)
			}
		})
	}
	writers.Wait()
	var row json.RawMessage
	if err := s.Get(ctx, "run", "operation", &row); !errors.Is(err, ErrNotFound) {
		t.Fatal("operation resurrected", err)
	}
	events, err := s.Events(ctx, "operation", 0)
	if err != nil || len(events) != 0 {
		t.Fatal("events resurrected", events, err)
	}
}

func TestMemoryPurgeReusableIdentityCanBeExplicitlyRecreated(t *testing.T) {
	s := NewMemory()
	ctx := t.Context()
	refs := []RecordRef{{Kind: "session", ID: "qq-peer", Reusable: true}, {Kind: "archive", ID: "1:qq-peer", Reusable: true}}
	for _, ref := range refs {
		if err := s.Put(ctx, ref.Kind, ref.ID, map[string]string{"message": "old content"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Purge(ctx, s, refs, nil); err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if guarded, err := Purged(ctx, s, ref.Kind, ref.ID); err != nil || guarded {
			t.Fatal(guarded, err)
		}
		if err := s.Put(ctx, ref.Kind, ref.ID, map[string]string{"message": "fresh content"}); err != nil {
			t.Fatal("reusable identity blocked", err)
		}
	}
	// A repeated ref cannot weaken the protection requested by another caller.
	if err := Purge(ctx, s, []RecordRef{{Kind: "run", ID: "guarded"}, {Kind: "run", ID: "guarded", Reusable: true}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "run", "guarded", true); !errors.Is(err, ErrPurgedRecord) {
		t.Fatal("duplicate reusable reference weakened guard", err)
	}
}
