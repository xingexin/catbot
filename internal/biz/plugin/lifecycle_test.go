package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

func TestPluginArchiveRetainsPinnedCallsAndRestoreStaysDisabled(t *testing.T) {
	runtime := &fixtureRuntime{call: func(_ context.Context, _ domainplugin.Plugin, _ map[string]any, _ string, args map[string]any, _ string) (any, error) {
		return args, nil
	}}
	m, p, key := fixtureManager(t, runtime)
	if err := m.Archive(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CallPinned(t.Context(), key, "echo", map[string]any{"text": "in flight"}, "pinned-call"); err != nil {
		t.Fatal("archive broke existing run", err)
	}
	if err := m.SetPluginEnabled(t.Context(), p.ID, true); !errors.Is(err, lifecycleRepository.ErrArchived) {
		t.Fatal("archived plugin enabled", err)
	}
	if _, err := m.Configure(t.Context(), p.ID, nil, nil); !errors.Is(err, lifecycleRepository.ErrArchived) {
		t.Fatal("archived plugin configured", err)
	}
	if err := m.Restore(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	var restored domainplugin.Plugin
	if err := m.Store.Get(t.Context(), "plugin", p.ID, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Enabled {
		t.Fatal("restore automatically enabled plugin")
	}
	snapshots, err := m.Snapshots(t.Context())
	if err != nil || len(snapshots) != 0 {
		t.Fatal("restored plugin exposed new tools", snapshots, err)
	}
}

func TestPluginArchiveWaitsForDependentSchedulePause(t *testing.T) {
	m, p, _ := fixtureManager(t, &fixtureRuntime{})
	task := taskentity.Task{ID: "dependent", Versions: map[string]string{p.ID: "pinned"}}
	if err := m.Store.Put(t.Context(), "task", task.ID, task); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("Temporal unavailable")
	m.PauseDependent = func(context.Context, string, string) error { return failure }
	if err := m.Archive(t.Context(), p.ID); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	archived, _ := lifecycleRepository.Archived(t.Context(), m.Store, lifecycle.ResourcePlugin, p.ID)
	if archived {
		t.Fatal("plugin archived while dependent pause failed")
	}
	m.PauseDependent = func(context.Context, string, string) error { return nil }
	if err := m.Archive(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPluginPurgeRejectsAllSnapshotReferences(t *testing.T) {
	for _, kind := range []string{"task", "run", "execution", "execution-snapshot"} {
		t.Run(kind, func(t *testing.T) {
			m, p, key := fixtureManager(t, &fixtureRuntime{})
			m.PauseDependent = func(context.Context, string, string) error { return nil }
			versions := map[string]string{p.ID: key}
			records := map[string]any{
				"task":               taskentity.Task{ID: "reference", Versions: versions},
				"run":                conversation.Run{ID: "reference", Status: "completed", Versions: versions},
				"execution":          taskentity.TaskExecution{ID: "reference", Status: "completed", Versions: versions},
				"execution-snapshot": taskentity.Snapshot{Task: taskentity.Task{Versions: versions}},
			}
			if err := m.Store.Put(t.Context(), kind, "reference", records[kind]); err != nil {
				t.Fatal(err)
			}
			if err := m.Archive(t.Context(), p.ID); err != nil {
				t.Fatal(err)
			}
			if err := m.Purge(t.Context(), p.ID); err == nil {
				t.Fatal("referenced plugin purged")
			}
			var retained domainplugin.Plugin
			if err := m.Store.Get(t.Context(), "plugin-version", key, &retained); err != nil {
				t.Fatal("pinned configuration lost", err)
			}
		})
	}
}

func TestPluginPurgeDeletesPrivateStateButPreservesOtherPluginsAndDedup(t *testing.T) {
	m, p, key := fixtureManager(t, &fixtureRuntime{})
	for _, record := range []struct {
		kind, id string
		value    any
	}{
		{"plugin-data:" + p.ID, "primitive", 17}, {"plugin-data:" + p.ID, "unrelated-id", map[string]any{"id": "other-key"}},
		{"plugin-data:other", "primitive", 42}, {"operation", "side-effect", map[string]any{"status": "completed"}},
	} {
		if err := m.Store.Put(t.Context(), record.kind, record.id, record.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Archive(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := m.Purge(t.Context(), p.ID); err != nil {
			t.Fatal(err)
		}
	}
	var value any
	for _, ref := range []store.RecordRef{{Kind: "plugin", ID: p.ID}, {Kind: "plugin-version", ID: key}, {Kind: "plugin-data:" + p.ID, ID: "primitive"}, {Kind: "plugin-data:" + p.ID, ID: "unrelated-id"}} {
		if err := m.Store.Get(t.Context(), ref.Kind, ref.ID, &value); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("private plugin data retained", ref, err)
		}
	}
	for _, ref := range []store.RecordRef{{Kind: "plugin-data:other", ID: "primitive"}, {Kind: "operation", ID: "side-effect"}, {Kind: "secret", ID: "fixture-secret"}} {
		if err := m.Store.Get(t.Context(), ref.Kind, ref.ID, &value); err != nil {
			t.Fatal("unrelated data deleted", ref, err)
		}
	}
	if _, err := m.CallPinned(t.Context(), key, "echo", map[string]any{"text": "late"}, "late-call"); err == nil {
		t.Fatal("purged plugin replayed")
	}
	purged, err := store.Purged(t.Context(), m.Store, "plugin", p.ID)
	if err != nil || !purged {
		t.Fatal("bootstrap resurrection guard missing", err)
	}
	// An explicit reinstall is allowed and does not inherit the deleted settings.
	p.Manifest.Entry = "index.js"
	m.packages = lifecyclePackages{manifest: p.Manifest}
	fresh, err := m.Register(t.Context(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Enabled || len(fresh.Config) != 0 || len(fresh.Secrets) != 0 {
		t.Fatal("reinstalled plugin inherited deleted configuration")
	}
	if err := m.Store.Put(t.Context(), "plugin-data:"+p.ID, "primitive", "new"); err != nil {
		t.Fatal("reinstall could not reuse deleted KV key", err)
	}
}

type lifecyclePackages struct{ manifest domainplugin.Manifest }

func (p lifecyclePackages) Read(string) (string, domainplugin.Manifest, error) {
	return "/source", p.manifest, nil
}
func (p lifecyclePackages) Freeze(context.Context, string, domainplugin.Manifest) (string, error) {
	return "/new-frozen", nil
}

func TestPluginPurgeClearsArchiveIndexOfOwnedSecret(t *testing.T) {
	m, p, _ := fixtureManager(t, &fixtureRuntime{})
	secret := "plugin-" + p.ID + "-password-owned"
	if err := m.Vault.Set(t.Context(), secret, "owned", "secret-value"); err != nil {
		t.Fatal(err)
	}
	p.Secrets = map[string]string{"password": secret}
	if err := m.Store.Put(t.Context(), "plugin", p.ID, p); err != nil {
		t.Fatal(err)
	}
	if err := lifecycleRepository.Mark(t.Context(), m.Store, lifecycle.ArchiveRecord{Resource: lifecycle.ResourceSecret, RecordID: secret, Name: "owned", ArchivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := m.Archive(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Purge(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	archived, err := lifecycleRepository.Archived(t.Context(), m.Store, lifecycle.ResourceSecret, secret)
	if err != nil || archived {
		t.Fatal("orphan secret archive index retained", err)
	}
	if _, err := m.Vault.Get(t.Context(), secret); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("owned secret retained", err)
	}
}

func TestPluginConfigureCannotBindArchivedModel(t *testing.T) {
	m, p, _ := fixtureManager(t, &fixtureRuntime{})
	p.ID, p.Manifest.ID = "mail", "mail"
	if err := m.Store.Put(t.Context(), "plugin", p.ID, p); err != nil {
		t.Fatal(err)
	}
	if err := m.Store.Put(t.Context(), "config", "model", agent.Config{ID: "model", Kind: "api"}); err != nil {
		t.Fatal(err)
	}
	if err := lifecycleRepository.Mark(t.Context(), m.Store, lifecycle.ArchiveRecord{Resource: lifecycle.ResourceConfig, RecordID: "model", Name: "model", ArchivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Configure(t.Context(), p.ID, map[string]any{"summaryConfigId": "model"}, nil); err == nil {
		t.Fatal("new plugin reference bound archived model")
	}
}
