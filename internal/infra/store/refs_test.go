package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRecordRefsUsesStorageKeysAndIsolatesPluginNamespaces(t *testing.T) {
	s := NewMemory()
	ctx := t.Context()
	for _, ref := range []RecordRef{{Kind: "plugin-data:mail", ID: "cursor-b"}, {Kind: "plugin-data:mail", ID: "cursor-a"}, {Kind: "plugin-data:other", ID: "cursor-a"}} {
		if err := s.Put(ctx, ref.Kind, ref.ID, map[string]string{"id": "not-the-storage-key", "content": "private value"}); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := RecordRefs(ctx, s, "plugin-data:mail")
	want := []RecordRef{{Kind: "plugin-data:mail", ID: "cursor-a"}, {Kind: "plugin-data:mail", ID: "cursor-b"}}
	if err != nil || !reflect.DeepEqual(refs, want) {
		t.Fatal(refs, err)
	}
	for i := range refs {
		refs[i].Reusable = true
	}
	if err := Purge(ctx, s, refs, nil); err != nil {
		t.Fatal(err)
	}
	remaining, err := RecordRefs(ctx, s, "plugin-data:mail")
	if err != nil || remaining == nil || len(remaining) != 0 {
		t.Fatal(remaining, err)
	}
	var other map[string]string
	if err := s.Get(ctx, "plugin-data:other", "cursor-a", &other); err != nil || other["content"] != "private value" {
		t.Fatal(other, err)
	}
	if err := s.Put(ctx, "plugin-data:mail", "cursor-a", "fresh install"); err != nil {
		t.Fatal(err)
	}
}

func TestRecordRefsRejectsUnsupportedStoreAndCancelledCalls(t *testing.T) {
	s := NewMemory()
	if _, err := RecordRefs(t.Context(), struct{ Store }{Store: s}, "records"); !errors.Is(err, ErrRecordRefsUnsupported) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := RecordRefs(ctx, s, "records"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := RecordRefs(t.Context(), s, ""); err == nil {
		t.Fatal("empty namespace accepted")
	}
}
