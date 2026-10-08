package store

import (
	"encoding/json"
	"testing"
	"time"
)

func storedRunID(raw json.RawMessage) string {
	var row struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		panic(err)
	}
	return row.ID
}

func TestRunQueriesPreserveOpaquePayload(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		var s Store = NewMemory()
		if wrapped {
			s = legacyQueueStore{s}
		}
		row := map[string]any{"id": "opaque-run", "sessionId": "session", "status": "queued", "createdAt": time.Now(), "persona": map[string]any{"name": "米雪儿"}, "unknownFutureField": []string{"preserve"}}
		if err := s.Put(t.Context(), "run", "opaque-run", row); err != nil {
			t.Fatal(err)
		}
		queued, err := QueuedRuns(t.Context(), s, nil, 1)
		if err != nil || len(queued) != 1 {
			t.Fatalf("queued: %s %v", queued, err)
		}
		var got map[string]any
		if err := json.Unmarshal(queued[0], &got); err != nil {
			t.Fatal(err)
		}
		if got["persona"].(map[string]any)["name"] != "米雪儿" || got["unknownFutureField"].([]any)[0] != "preserve" {
			t.Fatalf("payload truncated: %v", got)
		}
		got["status"] = "completed"
		got["replyPending"] = true
		if err := s.Put(t.Context(), "run", "opaque-run", got); err != nil {
			t.Fatal(err)
		}
		replies, err := PendingReplies(t.Context(), s, 1)
		if err != nil || len(replies) != 1 {
			t.Fatalf("replies: %s %v", replies, err)
		}
		if err := json.Unmarshal(replies[0], &got); err != nil {
			t.Fatal(err)
		}
		if got["unknownFutureField"].([]any)[0] != "preserve" {
			t.Fatalf("reply payload truncated: %v", got)
		}
	}
}
