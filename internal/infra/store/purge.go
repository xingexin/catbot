package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const PurgedRecordKind = "purged-record"

var (
	ErrAtomicPurgeUnsupported = errors.New("store does not support atomic purge")
	ErrPurgedRecord           = errors.New("record has been permanently deleted")
)

// RecordRef identifies a storage record without coupling persistence to domain types.
type RecordRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// Reusable skips the replay guard for indexes and identities that can be
	// explicitly recreated. Do not use it for completed side-effect operations.
	Reusable bool `json:"-"`
}

// PurgedRecord retains no content, credentials, recipient or result payload. It
// prevents an already deleted operation from being replayed as a new operation.
type PurgedRecord struct {
	Kind     string    `json:"kind"`
	ID       string    `json:"id"`
	PurgedAt time.Time `json:"purgedAt"`
}

type atomicPurger interface {
	Purge(context.Context, []RecordRef, []string) error
}

// Purge atomically deletes records and events, retaining minimal replay guards.
// A storage adapter must implement the operation itself; sequential Delete calls
// cannot safely approximate its all-or-nothing contract.
func Purge(ctx context.Context, s Store, refs []RecordRef, runIDs []string) error {
	if err := validatePurge(ctx, refs, runIDs); err != nil {
		return err
	}
	purger, ok := s.(atomicPurger)
	if !ok {
		return ErrAtomicPurgeUnsupported
	}
	return purger.Purge(ctx, refs, runIDs)
}

// PurgeKey is unambiguous even when a kind or ID contains separators.
func PurgeKey(kind, id string) string {
	payload, _ := json.Marshal([2]string{kind, id})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func Purged(ctx context.Context, s Store, kind, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var record PurgedRecord
	err := s.Get(ctx, PurgedRecordKind, PurgeKey(kind, id), &record)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *Postgres) Purge(ctx context.Context, refs []RecordRef, runIDs []string) error {
	if err := validatePurge(ctx, refs, runIDs); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin purge: %w", err)
	}
	defer rollbackRecordTransaction(ctx, tx)
	if err := lockRecordWrites(ctx, tx, refs); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, ref := range refs {
		payload, err := json.Marshal(PurgedRecord{Kind: ref.Kind, ID: ref.ID, PurgedAt: now})
		if err != nil {
			return err
		}
		if !ref.Reusable {
			if _, err := tx.Exec(ctx, `INSERT INTO records(kind,id,data) VALUES($1,$2,$3)
			    ON CONFLICT(kind,id) DO NOTHING`, PurgedRecordKind, PurgeKey(ref.Kind, ref.ID), payload); err != nil {
				return fmt.Errorf("save purge guard: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, "DELETE FROM records WHERE kind=$1 AND id=$2", ref.Kind, ref.ID); err != nil {
			return fmt.Errorf("purge record: %w", err)
		}
	}
	if len(runIDs) > 0 {
		if _, err := tx.Exec(ctx, "DELETE FROM run_events WHERE run_id=ANY($1::text[])", runIDs); err != nil {
			return fmt.Errorf("purge run events: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit purge: %w", err)
	}
	return nil
}

func (s *Memory) Purge(ctx context.Context, refs []RecordRef, runIDs []string) error {
	if err := validatePurge(ctx, refs, runIDs); err != nil {
		return err
	}
	guards := make(map[string]json.RawMessage, len(refs))
	now := time.Now().UTC()
	for _, ref := range refs {
		if ref.Reusable {
			continue
		}
		payload, err := json.Marshal(PurgedRecord{Kind: ref.Kind, ID: ref.ID, PurgedAt: now})
		if err != nil {
			return err
		}
		guards[PurgeKey(ref.Kind, ref.ID)] = payload
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.data[PurgedRecordKind] == nil {
		s.data[PurgedRecordKind] = map[string]json.RawMessage{}
	}
	for id, payload := range guards {
		if _, exists := s.data[PurgedRecordKind][id]; !exists {
			s.data[PurgedRecordKind][id] = payload
		}
	}
	for _, ref := range refs {
		delete(s.data[ref.Kind], ref.ID)
	}
	runs := make(map[string]struct{}, len(runIDs))
	for _, id := range runIDs {
		runs[id] = struct{}{}
	}
	s.events = slices.DeleteFunc(s.events, func(event EventRecord) bool {
		_, remove := runs[event.RunID]
		return remove
	})
	return nil
}

func validatePurge(ctx context.Context, refs []RecordRef, runIDs []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.Kind == "" || ref.ID == "" || strings.ContainsRune(ref.Kind+ref.ID, '\x00') {
			return errors.New("purge requires non-empty record kind and ID without null characters")
		}
		if ref.Kind == PurgedRecordKind {
			return errors.New("purge guards cannot be deleted")
		}
	}
	for _, id := range runIDs {
		if id == "" || strings.ContainsRune(id, '\x00') {
			return errors.New("purge requires non-empty run ID without null characters")
		}
	}
	return nil
}

// All record writers use the same transaction locks so a stale worker cannot
// recreate a purged row after the lifecycle check but before its final Put.
func lockRecordWrites(ctx context.Context, tx pgx.Tx, refs []RecordRef) error {
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		keys = append(keys, "record-write:"+PurgeKey(ref.Kind, ref.ID))
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	for _, key := range keys {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", key); err != nil {
			return fmt.Errorf("lock record write: %w", err)
		}
	}
	return nil
}

func rollbackRecordTransaction(ctx context.Context, tx pgx.Tx) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(rollbackCtx)
}

func guardRecordWrite(ctx context.Context, tx pgx.Tx, kind, id string) error {
	if err := lockRecordWrites(ctx, tx, []RecordRef{{Kind: kind, ID: id}}); err != nil {
		return err
	}
	var purged bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM records WHERE kind=$1 AND id=$2)", PurgedRecordKind, PurgeKey(kind, id)).Scan(&purged); err != nil {
		return err
	}
	if purged {
		return ErrPurgedRecord
	}
	return nil
}
