package lifecycle_test

import (
	"errors"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	domain "github.com/xingexin/catbot/internal/domain/lifecycle"
	repository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
	"testing"
	"time"
)

func TestSecretLifecycleProtectsCredentialInQueuedRunSnapshot(t *testing.T) {
	for _, action := range []domain.Action{domain.ActionArchive, domain.ActionPurge} {
		name := "archive"
		if action == domain.ActionPurge {
			name = "purge"
		}
		t.Run(name, func(t *testing.T) {
			service, s := fixture(t)
			ctx := t.Context()
			old := vault.Record{ID: "key-a", Name: "previous key", Ciphertext: "fixture-encrypted-a"}
			put(t, s, "secret", old.ID, old)
			put(t, s, "secret", "key-b", vault.Record{ID: "key-b", Name: "new key", Ciphertext: "fixture-encrypted-b"})
			if action == domain.ActionPurge {
				// Even an existing archive marker cannot skip the dependency recheck.
				if err := repository.Mark(ctx, s, domain.ArchiveRecord{Resource: domain.ResourceSecret, RecordID: old.ID, Name: old.Name, ArchivedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			original := agent.Config{ID: "model-config", CredentialID: old.ID}
			run := conversation.Run{ID: "queued-with-old-key", Status: "queued", Config: original}
			put(t, s, "run", run.ID, run)
			current := original
			current.CredentialID = "key-b"
			put(t, s, "config", current.ID, current)
			result := perform(t, service, domain.ResourceSecret, action, old.ID)
			if len(result.Succeeded) != 0 || len(result.Failed) != 1 {
				t.Fatal("queued run lost fixed credential", result)
			}
			var retained vault.Record
			if err := s.Get(ctx, "secret", old.ID, &retained); err != nil || retained.Ciphertext != old.Ciphertext {
				t.Fatal("rejected action altered credential", retained, err)
			}
			archived, err := repository.Archived(ctx, s, domain.ResourceSecret, old.ID)
			if err != nil || archived != (action == domain.ActionPurge) {
				t.Fatal("rejected action changed archive marker", archived, err)
			}
			// The obsolete key becomes removable only after its retained run is cleared.
			run.Status = "completed"
			put(t, s, "run", run.ID, run)
			succeeded(t, perform(t, service, domain.ResourceRun, domain.ActionArchive, run.ID), 1)
			succeeded(t, perform(t, service, domain.ResourceRun, domain.ActionPurge, run.ID), 1)
			succeeded(t, perform(t, service, domain.ResourceSecret, domain.ActionArchive, old.ID), 1)
			succeeded(t, perform(t, service, domain.ResourceSecret, domain.ActionPurge, old.ID), 1)
			if err := s.Get(ctx, "secret", old.ID, &retained); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("obsolete credential survived", err)
			}
			if err := s.Get(ctx, "secret", "key-b", &retained); err != nil || retained.ID != "key-b" {
				t.Fatal("current credential affected", retained, err)
			}
		})
	}
}
