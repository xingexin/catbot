package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/infra/store"
)

type brokenReceiptStore struct {
	store.Store
	err error
}

func (s brokenReceiptStore) Get(ctx context.Context, kind, id string, v any) error {
	if kind == "qq-receipt" {
		return s.err
	}
	return s.Store.Get(ctx, kind, id, v)
}

func TestRunCutoffUsesReceiptOrStableCreationTime(t *testing.T) {
	t.Parallel()
	s := store.NewMemory()
	run := conversation.Run{ID: "r", CreatedAt: time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)}
	got, err := RunCutoff(t.Context(), s, run)
	if err != nil || !got.Equal(run.CreatedAt) {
		t.Fatal(got, err)
	}
	received := run.CreatedAt.Add(-time.Minute)
	if err = s.Put(t.Context(), "qq-receipt", run.ID, messaging.ReplyReference{ReceivedAt: received}); err != nil {
		t.Fatal(err)
	}
	got, err = RunCutoff(t.Context(), s, run)
	if err != nil || !got.Equal(received) {
		t.Fatal(got, err)
	}
	failure := errors.New("storage unavailable")
	if _, err = RunCutoff(t.Context(), brokenReceiptStore{s, failure}, run); !errors.Is(err, failure) {
		t.Fatal("read errors must not widen the cutoff", err)
	}
	if err = s.Put(t.Context(), "qq-receipt", run.ID, messaging.ReplyReference{}); err != nil {
		t.Fatal(err)
	}
	got, err = RunCutoff(t.Context(), s, run)
	if err != nil || !got.Equal(run.CreatedAt) {
		t.Fatal("legacy receipt should preserve run creation", got, err)
	}
}
