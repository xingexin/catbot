package pluginhost

import (
	"errors"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/store"
)

func TestHostPutAllowsArchivedSnapshotButNeverRecreatesPurgedData(t *testing.T) {
	s := store.NewMemory()
	host := &Service{Store: s}
	p := plugin.Plugin{ID: "fixture", Manifest: plugin.Manifest{ID: "fixture", Version: "1.0.0"}, Config: map[string]any{}, Directory: "/frozen"}
	key := plugin.SnapshotKey(p)
	for _, ref := range []store.RecordRef{{Kind: "plugin", ID: p.ID}, {Kind: "plugin-version", ID: key}} {
		if err := s.Put(t.Context(), ref.Kind, ref.ID, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := lifecycleRepository.Mark(t.Context(), s, lifecycle.ArchiveRecord{Resource: lifecycle.ResourcePlugin, RecordID: p.ID, Name: "fixture", ArchivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := host.Put(t.Context(), p, "cursor", 17); err != nil {
		t.Fatal("archive interrupted an existing plugin operation", err)
	}
	if err := store.Purge(t.Context(), s, []store.RecordRef{{Kind: "plugin", ID: p.ID}, {Kind: "plugin-version", ID: key, Reusable: true}, {Kind: "plugin-data:" + p.ID, ID: "cursor", Reusable: true}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := host.Put(t.Context(), p, "cursor", 18); !errors.Is(err, lifecycleRepository.ErrPurged) {
		t.Fatal("late host request revived deleted KV", err)
	}
	var value any
	if err := s.Get(t.Context(), "plugin-data:"+p.ID, "cursor", &value); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("deleted data recreated", err)
	}
	// Even after explicit reinstall, an earlier request must still own an
	// existing pinned version before it can populate the new namespace.
	if err := s.Delete(t.Context(), store.PurgedRecordKind, store.PurgeKey("plugin", p.ID)); err != nil {
		t.Fatal(err)
	}
	if err := host.Put(t.Context(), p, "cursor", 19); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("missing old snapshot accepted", err)
	}
}
