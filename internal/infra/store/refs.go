package store

import (
	"context"
	"errors"
	"sort"
	"strings"
)

var ErrRecordRefsUnsupported = errors.New("store does not support record identity listing")

type recordRefReader interface {
	RecordRefs(context.Context, string) ([]RecordRef, error)
}

// RecordRefs lists storage keys even when their value has no embedded ID. It
// does not load payloads, and never guesses an ID from user-controlled content.
func RecordRefs(ctx context.Context, s Store, kind string) ([]RecordRef, error) {
	if err := validateRecordKind(ctx, kind); err != nil {
		return nil, err
	}
	reader, ok := s.(recordRefReader)
	if !ok {
		return nil, ErrRecordRefsUnsupported
	}
	return reader.RecordRefs(ctx, kind)
}

func (s *Postgres) RecordRefs(ctx context.Context, kind string) ([]RecordRef, error) {
	if err := validateRecordKind(ctx, kind); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT id FROM records WHERE kind=$1 ORDER BY id", kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []RecordRef{}
	for rows.Next() {
		ref := RecordRef{Kind: kind}
		if err := rows.Scan(&ref.ID); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (s *Memory) RecordRefs(ctx context.Context, kind string) ([]RecordRef, error) {
	if err := validateRecordKind(ctx, kind); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := make([]RecordRef, 0, len(s.data[kind]))
	for id := range s.data[kind] {
		refs = append(refs, RecordRef{Kind: kind, ID: id})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs, nil
}

func validateRecordKind(ctx context.Context, kind string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if kind == "" || strings.ContainsRune(kind, '\x00') {
		return errors.New("record kind must be non-empty without null characters")
	}
	return nil
}
