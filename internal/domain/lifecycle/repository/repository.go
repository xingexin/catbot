package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/xingexin/catbot/internal/domain/lifecycle"
	"github.com/xingexin/catbot/internal/infra/store"
)

func Archived(ctx context.Context, s store.Store, resource lifecycle.Resource, id string) (bool, error) {
	if err := validateIdentity(resource, id); err != nil {
		return false, err
	}
	var record lifecycle.ArchiveRecord
	err := s.Get(ctx, lifecycle.ArchiveStorageKind, lifecycle.ArchiveKey(resource, id), &record)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Mark preserves the first archive timestamp when the operation is repeated.
// Callers hold the lifecycle lock while changing record state and this index.
func Mark(ctx context.Context, s store.Store, record lifecycle.ArchiveRecord) error {
	if err := validateIdentity(record.Resource, record.RecordID); err != nil {
		return err
	}
	if record.ArchivedAt.IsZero() {
		return errors.New("archive timestamp is required")
	}
	record.ID = lifecycle.ArchiveKey(record.Resource, record.RecordID)
	record.ArchivedAt = record.ArchivedAt.UTC()
	archived, err := Archived(ctx, s, record.Resource, record.RecordID)
	if err != nil || archived {
		return err
	}
	return s.Put(ctx, lifecycle.ArchiveStorageKind, record.ID, record)
}

func Unmark(ctx context.Context, s store.Store, resource lifecycle.Resource, id string) error {
	if err := validateIdentity(resource, id); err != nil {
		return err
	}
	return s.Delete(ctx, lifecycle.ArchiveStorageKind, lifecycle.ArchiveKey(resource, id))
}

func List(ctx context.Context, s store.Store) ([]lifecycle.ArchiveRecord, error) {
	records, err := store.All[lifecycle.ArchiveRecord](ctx, s, lifecycle.ArchiveStorageKind)
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].ArchivedAt.Equal(records[j].ArchivedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].ArchivedAt.After(records[j].ArchivedAt)
	})
	return records, nil
}

func validateIdentity(resource lifecycle.Resource, id string) error {
	if !resource.Valid() {
		return fmt.Errorf("invalid archive resource %d", resource)
	}
	if id == "" {
		return errors.New("archive record ID is required")
	}
	return nil
}
