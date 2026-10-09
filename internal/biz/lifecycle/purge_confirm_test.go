package lifecycle_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	biz "github.com/xingexin/catbot/internal/biz/lifecycle"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	domain "github.com/xingexin/catbot/internal/domain/lifecycle"
	repository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/infra/store"
)

func confirmedRequest(t *testing.T, svc *biz.Service, targets ...biz.PurgeTarget) biz.PurgeRequest {
	t.Helper()
	in := biz.PurgeRequest{Items: targets}
	plan, err := svc.PreviewPurge(t.Context(), in)
	if err != nil || len(plan.Blockers) != 0 {
		t.Fatalf("preview: %+v, %v", plan, err)
	}
	in.Token = plan.Token
	return in
}

func TestConfirmPurgeReferencedModelKeepsSharedDependencies(t *testing.T) {
	svc, s := fixture(t)
	put(t, s, "config", "model", agent.Config{ID: "model", CredentialID: "shared"})
	put(t, s, "config", "other", agent.Config{ID: "other", CredentialID: "shared"})
	put(t, s, "secret", "shared", map[string]any{"id": "shared"})
	put(t, s, "session", "chat", conversation.Session{ID: "chat", ConfigID: "model"})
	put(t, s, "run", "run", conversation.Run{ID: "run", SessionID: "chat", Config: agent.Config{ID: "model"}, Status: "completed"})
	succeeded(t, perform(t, svc, domain.ResourceConfig, domain.ActionArchive, "model"), 1)
	request := confirmedRequest(t, svc, biz.PurgeTarget{Resource: domain.ResourceConfig, ID: "model"})
	result, err := svc.ConfirmPurge(t.Context(), request)
	if err != nil || len(result.Failed) != 0 || len(result.Deleted) != 3 {
		t.Fatalf("cascade result: %+v, %v", result, err)
	}
	for _, item := range result.Deleted {
		var record any
		if err := s.Get(t.Context(), item.Resource.StorageKind(), item.ID, &record); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("retained deleted record: %+v, %v", item, err)
		}
	}
	for _, ref := range []store.RecordRef{{Kind: "config", ID: "other"}, {Kind: "secret", ID: "shared"}} {
		var record any
		if err := s.Get(t.Context(), ref.Kind, ref.ID, &record); err != nil {
			t.Fatalf("shared dependency deleted: %+v, %v", ref, err)
		}
	}
}

func TestConfirmPurgeRejectsStalePreviewBeforeMutations(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*testing.T, store.Store)
	}{
		{"new reference", func(t *testing.T, s store.Store) {
			put(t, s, "session", "new", conversation.Session{ID: "new", ConfigID: "model"})
		}},
		{"changed content", func(t *testing.T, s store.Store) {
			put(t, s, "session", "chat", conversation.Session{ID: "chat", ConfigID: "model", Title: "new title"})
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			svc, s := fixture(t)
			put(t, s, "config", "model", agent.Config{ID: "model"})
			put(t, s, "session", "chat", conversation.Session{ID: "chat", ConfigID: "model"})
			succeeded(t, perform(t, svc, domain.ResourceConfig, domain.ActionArchive, "model"), 1)
			request := confirmedRequest(t, svc, biz.PurgeTarget{Resource: domain.ResourceConfig, ID: "model"})
			change.apply(t, s)
			result, err := svc.ConfirmPurge(t.Context(), request)
			if !errors.Is(err, repository.ErrPurgeScopeChanged) || len(result.Deleted) != 0 {
				t.Fatalf("stale preview applied: %+v, %v", result, err)
			}
			archived, err := repository.Archived(t.Context(), s, domain.ResourceSession, "chat")
			if err != nil || archived {
				t.Fatalf("validation changed dependent archive state: %v, %v", archived, err)
			}
		})
	}
}

type cascadeStore struct {
	*store.Memory
	beforeLock  func(string)
	failPurgeID string
}

func (s *cascadeStore) Lock(ctx context.Context, key string) (func(), error) {
	if s.beforeLock != nil {
		s.beforeLock(key)
	}
	return s.Memory.Lock(ctx, key)
}
func (s *cascadeStore) Purge(ctx context.Context, refs []store.RecordRef, runs []string) error {
	for _, ref := range refs {
		if ref.ID == s.failPurgeID {
			return errors.New("simulated storage failure")
		}
	}
	return s.Memory.Purge(ctx, refs, runs)
}

func TestCascadeNeverDeletesUnconfirmedOwnedRun(t *testing.T) {
	svc, memory := fixture(t)
	s := &cascadeStore{Memory: memory}
	svc.Store = s
	put(t, s, "config", "model", agent.Config{ID: "model"})
	put(t, s, "session", "chat", conversation.Session{ID: "chat", ConfigID: "model"})
	succeeded(t, perform(t, svc, domain.ResourceConfig, domain.ActionArchive, "model"), 1)
	request := confirmedRequest(t, svc, biz.PurgeTarget{Resource: domain.ResourceConfig, ID: "model"})
	locks := 0
	s.beforeLock = func(key string) {
		if key == "session-meta:chat" {
			locks++
			if locks == 2 { // After both previews, immediately before session purge locks.
				put(t, s, "run", "new-run", conversation.Run{ID: "new-run", SessionID: "chat", Status: "completed"})
			}
		}
	}
	result, err := svc.ConfirmPurge(t.Context(), request)
	if err != nil || len(result.Deleted) != 0 || len(result.Failed) != 2 || !strings.Contains(result.Failed[0].Reason, "重新预览") {
		t.Fatalf("unexpected scope-change result: %+v, %v", result, err)
	}
	for _, ref := range []store.RecordRef{{Kind: "run", ID: "new-run"}, {Kind: "session", ID: "chat"}, {Kind: "config", ID: "model"}} {
		var record any
		if err := s.Get(t.Context(), ref.Kind, ref.ID, &record); err != nil {
			t.Fatalf("scope change removed %+v: %v", ref, err)
		}
	}
}

func TestCascadeReturnsCompletedPrefixOnStorageFailure(t *testing.T) {
	svc, memory := fixture(t)
	s := &cascadeStore{Memory: memory, failPurgeID: "chat"}
	svc.Store = s
	put(t, s, "config", "model", agent.Config{ID: "model"})
	put(t, s, "session", "chat", conversation.Session{ID: "chat", ConfigID: "model"})
	put(t, s, "run", "run", conversation.Run{ID: "run", SessionID: "chat", Status: "completed"})
	succeeded(t, perform(t, svc, domain.ResourceConfig, domain.ActionArchive, "model"), 1)
	request := confirmedRequest(t, svc, biz.PurgeTarget{Resource: domain.ResourceConfig, ID: "model"})
	result, err := svc.ConfirmPurge(t.Context(), request)
	if err != nil || len(result.Deleted) != 1 || result.Deleted[0].ID != "run" || len(result.Failed) != 2 {
		t.Fatalf("partial result: %+v, %v", result, err)
	}
	for _, ref := range []store.RecordRef{{Kind: "session", ID: "chat"}, {Kind: "config", ID: "model"}} {
		var record any
		if err := s.Get(t.Context(), ref.Kind, ref.ID, &record); err != nil {
			t.Fatalf("failure cascade continued: %+v, %v", ref, err)
		}
	}
}
