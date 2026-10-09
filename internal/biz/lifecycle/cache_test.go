package lifecycle_test

import (
	"errors"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	domain "github.com/xingexin/catbot/internal/domain/lifecycle"
	repo "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	task "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
	"testing"
)

func TestPurgeOwnedCachesKeepsSharedResultsUntilLastOwnerIsDeleted(t *testing.T) {
	svc, s := fixture(t)
	ctx := t.Context()
	own := store.RecordRef{Kind: repo.CacheOperationStorageKind, ID: "arbitrary-own-key"}
	shared := store.RecordRef{Kind: repo.CacheResultStorageKind, ID: "group-hashed-shared"}
	unknown := store.RecordRef{Kind: repo.CacheOperationStorageKind, ID: "run-a:looks-owned-but-is-unknown"}
	for _, id := range []string{"run-a", "run-b"} {
		put(t, s, "run", id, conversation.Run{ID: id, Status: "completed"})
	}
	for _, ref := range []store.RecordRef{own, shared, unknown} {
		put(t, s, ref.Kind, ref.ID, map[string]string{"result": "private cache body"})
	}
	if err := repo.OwnCaches(ctx, s, repo.CacheOwner{Resource: domain.ResourceRun, RecordID: "run-a"}, own, shared); err != nil {
		t.Fatal(err)
	}
	if err := repo.OwnCaches(ctx, s, repo.CacheOwner{Resource: domain.ResourceRun, RecordID: "run-b"}, shared); err != nil {
		t.Fatal(err)
	}
	succeeded(t, perform(t, svc, domain.ResourceRun, domain.ActionArchive, "run-a"), 1)
	succeeded(t, perform(t, svc, domain.ResourceRun, domain.ActionPurge, "run-a"), 1)
	var value any
	if err := s.Get(ctx, own.Kind, own.ID, &value); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("owned content survived", err)
	}
	for _, ref := range []store.RecordRef{shared, unknown} {
		if err := s.Get(ctx, ref.Kind, ref.ID, &value); err != nil {
			t.Fatal("shared/unknown cache was deleted", ref, err)
		}
	}
	if err := s.Put(ctx, own.Kind, own.ID, "late private result"); !errors.Is(err, store.ErrPurgedRecord) {
		t.Fatal("late cache write resurrected content", err)
	}
	if err := repo.OwnCaches(ctx, s, repo.CacheOwner{Resource: domain.ResourceRun, RecordID: "run-a"}, own); err == nil {
		t.Fatal("deleted owner was recreated")
	}
	succeeded(t, perform(t, svc, domain.ResourceRun, domain.ActionArchive, "run-b"), 1)
	succeeded(t, perform(t, svc, domain.ResourceRun, domain.ActionPurge, "run-b"), 1)
	if err := s.Get(ctx, shared.Kind, shared.ID, &value); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("last owner's content survived", err)
	}
	owners, err := s.List(ctx, repo.CacheOwnerStorageKind)
	if err != nil || len(owners) != 0 {
		t.Fatal("orphan owner indexes", len(owners), err)
	}
}
func TestLegacyCacheCleanupUsesExactExecutionSnapshotAndSDKEvents(t *testing.T) {
	svc, s := fixture(t)
	ctx := t.Context()
	execution := task.TaskExecution{ID: "execution", Status: "completed"}
	put(t, s, "execution", execution.ID, execution)
	put(t, s, "execution-snapshot", execution.ID, task.Snapshot{Task: task.Task{Steps: []task.Step{{ID: "tool", Kind: "tool", Tool: "example__echo"}, {ID: "agent", Kind: "agent"}}}})
	put(t, s, repo.CacheOperationStorageKind, "execution:tool", map[string]string{"result": "private exact step"})
	put(t, s, repo.CacheOperationStorageKind, "execution:agent", map[string]string{"result": "unknown unrelated key"})
	succeeded(t, perform(t, svc, domain.ResourceExecution, domain.ActionArchive, execution.ID), 1)
	succeeded(t, perform(t, svc, domain.ResourceExecution, domain.ActionPurge, execution.ID), 1)
	var value any
	if err := s.Get(ctx, repo.CacheOperationStorageKind, "execution:tool", &value); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Get(ctx, repo.CacheOperationStorageKind, "execution:agent", &value); err != nil {
		t.Fatal("non-tool step inferred as cache owner", err)
	}
	put(t, s, "run", "sdk-run", conversation.Run{ID: "sdk-run", Status: "completed", Config: agent.Config{Kind: "sdk"}})
	key := `sdk-run:mcp:42`
	put(t, s, repo.CacheOperationStorageKind, key, map[string]string{"result": "private SDK tool"})
	if err := s.Append(ctx, store.EventRecord{RunID: "sdk-run", Type: "tool.started", Data: map[string]any{"name": "example__echo", "callId": key}}); err != nil {
		t.Fatal(err)
	}
	succeeded(t, perform(t, svc, domain.ResourceRun, domain.ActionArchive, "sdk-run"), 1)
	succeeded(t, perform(t, svc, domain.ResourceRun, domain.ActionPurge, "sdk-run"), 1)
	if err := s.Get(ctx, repo.CacheOperationStorageKind, key, &value); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("SDK traced cache survived", err)
	}
}
