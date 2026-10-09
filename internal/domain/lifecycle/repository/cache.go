package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	"github.com/xingexin/catbot/internal/infra/store"
)

const CacheOwnerStorageKind = "cache-owner"
const CacheOperationStorageKind = "operation"
const CacheResultStorageKind = "tool-result"
const CacheStartedStorageKind = "tool-started"

type CacheOwner struct {
	Resource lifecycle.Resource `json:"resource"`
	RecordID string             `json:"recordId"`
}
type CacheOwnership struct {
	ID         string            `json:"id"`
	Owner      CacheOwner        `json:"owner"`
	References []store.RecordRef `json:"references"`
}

// OwnCaches records the final storage identities, after provider/group rewriting.
// A parent must exist; new cache references cannot revive an already deleted run.
func OwnCaches(ctx context.Context, s store.Store, owner CacheOwner, refs ...store.RecordRef) error {
	if owner.RecordID == "" || (owner.Resource != lifecycle.ResourceRun && owner.Resource != lifecycle.ResourceExecution) {
		return errors.New("cache owner must be a run or execution")
	}
	for _, ref := range refs {
		if ref.ID == "" {
			return errors.New("cache operation ID is required")
		}
		switch ref.Kind {
		case CacheOperationStorageKind, CacheResultStorageKind, CacheStartedStorageKind:
		default:
			return fmt.Errorf("unsupported cache storage kind %q", ref.Kind)
		}
	}
	unlock, err := s.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return err
	}
	defer unlock()
	if err := RequireActive(ctx, s, owner.Resource, owner.RecordID); err != nil {
		return err
	}
	var parent json.RawMessage
	if err := s.Get(ctx, owner.Resource.StorageKind(), owner.RecordID, &parent); err != nil {
		return err
	}
	key := lifecycle.ArchiveKey(owner.Resource, owner.RecordID)
	record := CacheOwnership{ID: key, Owner: owner, References: []store.RecordRef{}}
	if err := s.Get(ctx, CacheOwnerStorageKind, key, &record); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if record.Owner != owner {
		return errors.New("cache owner identity mismatch")
	}
	seen := map[store.RecordRef]bool{}
	for _, ref := range record.References {
		ref.Reusable = false
		seen[ref] = true
	}
	for _, ref := range refs {
		ref.Reusable = false
		purged, err := store.Purged(ctx, s, ref.Kind, ref.ID)
		if err != nil {
			return err
		}
		if purged {
			return store.ErrPurgedRecord
		}
		if !seen[ref] {
			record.References = append(record.References, ref)
			seen[ref] = true
		}
	}
	return s.Put(ctx, CacheOwnerStorageKind, key, record)
}
