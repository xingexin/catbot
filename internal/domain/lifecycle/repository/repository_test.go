package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain/lifecycle"
	"github.com/xingexin/catbot/internal/infra/store"
)

func TestArchiveRestorePreservesPayloadAndFirstArchiveTime(t *testing.T) {
	s := store.NewMemory()
	ctx := t.Context()
	payload := map[string]string{"name": "personal conversation", "message": "untouched"}
	if err := s.Put(ctx, "session", "same-id", payload); err != nil {
		t.Fatal(err)
	}
	archived, err := Archived(ctx, s, lifecycle.ResourceSession, "same-id")
	if err != nil || archived {
		t.Fatal(archived, err)
	}
	timestamp := time.Date(2026, 10, 9, 14, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	record := lifecycle.ArchiveRecord{Resource: lifecycle.ResourceSession, RecordID: "same-id", Name: "personal conversation", ArchivedAt: timestamp}
	if err := Mark(ctx, s, record); err != nil {
		t.Fatal(err)
	}
	record.ArchivedAt = record.ArchivedAt.Add(time.Hour)
	if err := Mark(ctx, s, record); err != nil {
		t.Fatal(err)
	}
	if archived, err = Archived(ctx, s, lifecycle.ResourceSession, "same-id"); err != nil || !archived {
		t.Fatal(archived, err)
	}
	if archived, err = Archived(ctx, s, lifecycle.ResourceTask, "same-id"); err != nil || archived {
		t.Fatal("different resources shared an archive marker", archived, err)
	}
	rows, err := List(ctx, s)
	if err != nil || len(rows) != 1 || !rows[0].ArchivedAt.Equal(timestamp) || rows[0].ID != "1:same-id" || rows[0].ArchivedAt.Location() != time.UTC {
		t.Fatal(rows, err)
	}
	if err := Unmark(ctx, s, lifecycle.ResourceSession, "same-id"); err != nil {
		t.Fatal(err)
	}
	if archived, err = Archived(ctx, s, lifecycle.ResourceSession, "same-id"); err != nil || archived {
		t.Fatal(archived, err)
	}
	var actual map[string]string
	if err := s.Get(ctx, "session", "same-id", &actual); err != nil || actual["message"] != "untouched" {
		t.Fatal(actual, err)
	}
}

func TestArchiveRejectsMissingIdentityAndPropagatesReadFailure(t *testing.T) {
	s := store.NewMemory()
	for _, record := range []lifecycle.ArchiveRecord{
		{Resource: lifecycle.ResourceUnknown, RecordID: "id", ArchivedAt: time.Now()},
		{Resource: lifecycle.ResourceSession, ArchivedAt: time.Now()},
		{Resource: lifecycle.ResourceSession, RecordID: "id"},
	} {
		if err := Mark(t.Context(), s, record); err == nil {
			t.Fatal("invalid archive accepted", record)
		}
	}
	rows, err := List(t.Context(), s)
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	closed := errors.New("database unavailable")
	if _, err := Archived(t.Context(), failedReadStore{Store: s, err: closed}, lifecycle.ResourceSession, "id"); !errors.Is(err, closed) {
		t.Fatal(err)
	}
}

type failedReadStore struct {
	store.Store
	err error
}

func (s failedReadStore) Get(context.Context, string, string, any) error { return s.err }
