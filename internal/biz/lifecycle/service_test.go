package lifecycle_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	biz "github.com/xingexin/catbot/internal/biz/lifecycle"
	"github.com/xingexin/catbot/internal/biz/system"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/artifact"
	"github.com/xingexin/catbot/internal/domain/conversation"
	domain "github.com/xingexin/catbot/internal/domain/lifecycle"
	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/domain/persona"
	task "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/filestore"
	"github.com/xingexin/catbot/internal/infra/store"
)

func fixture(t *testing.T) (*biz.Service, *store.Memory) {
	t.Helper()
	s := store.NewMemory()
	files := filestore.Store{Root: t.TempDir()}
	if err := files.Prepare(); err != nil {
		t.Fatal(err)
	}
	return &biz.Service{Store: s, Files: files}, s
}
func put(t *testing.T, s store.Store, kind, id string, value any) {
	t.Helper()
	if err := s.Put(t.Context(), kind, id, value); err != nil {
		t.Fatal(err)
	}
}
func perform(t *testing.T, s *biz.Service, r domain.Resource, a domain.Action, ids ...string) biz.Result {
	t.Helper()
	result, err := s.Batch(t.Context(), biz.Request{Resource: r, Action: a, IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func succeeded(t *testing.T, r biz.Result, n int) {
	t.Helper()
	if len(r.Succeeded) != n || len(r.Failed) != 0 {
		t.Fatalf("unexpected result: %+v", r)
	}
}

func TestSessionArchiveRestoreAndHardPurge(t *testing.T) {
	svc, s := fixture(t)
	put(t, s, "config", "config", agent.Config{ID: "config"})
	put(t, s, "persona", "persona", persona.Persona{ID: "persona"})
	original := conversation.Session{ID: "s", Title: "保留上下文", Channel: "web", ConfigID: "config", PersonaID: "persona", Messages: []agent.Message{{Role: "user", Content: "private message"}}, Summary: "summary", Native: map[string]string{"sdk": "native-session"}}
	put(t, s, "session", "s", original)
	put(t, s, "run", "run-1", conversation.Run{ID: "run-1", SessionID: "s", Status: "completed", Prompt: "private prompt", Result: "private answer"})
	put(t, s, "notification", "n", messaging.Notification{ID: "n", SessionID: "s", Status: "saved", Text: "private notification"})
	if err := s.Append(t.Context(), store.EventRecord{RunID: "run-1", Type: "text", Data: map[string]any{"text": "private stream"}}); err != nil {
		t.Fatal(err)
	}
	if r := perform(t, svc, domain.ResourceSession, domain.ActionPurge, "s"); len(r.Failed) != 1 {
		t.Fatal("active record purged", r)
	}
	succeeded(t, perform(t, svc, domain.ResourceSession, domain.ActionArchive, "s", "s"), 1)
	list, err := (&system.Service{Store: s}).List(t.Context(), "session")
	if err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
	var preserved conversation.Session
	if err := s.Get(t.Context(), "session", "s", &preserved); err != nil || len(preserved.Messages) != 1 || preserved.Native["sdk"] != "native-session" {
		t.Fatal(preserved, err)
	}
	archives, err := svc.Archives(t.Context())
	if err != nil || len(archives) != 1 || archives[0].Name != original.Title {
		t.Fatal(archives, err)
	}
	succeeded(t, perform(t, svc, domain.ResourceSession, domain.ActionRestore, "s"), 1)
	list, err = (&system.Service{Store: s}).List(t.Context(), "session")
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	succeeded(t, perform(t, svc, domain.ResourceSession, domain.ActionArchive, "s"), 1)
	succeeded(t, perform(t, svc, domain.ResourceSession, domain.ActionPurge, "s"), 1)
	for kind, id := range map[string]string{"session": "s", "run": "run-1", "notification": "n"} {
		var v any
		if err := s.Get(t.Context(), kind, id, &v); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s payload retained: %v", kind, err)
		}
	}
	events, err := s.Events(t.Context(), "run-1", 0)
	if err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
	guard, err := store.Purged(t.Context(), s, "run", "run-1")
	if err != nil || !guard {
		t.Fatal("run replay guard missing", err)
	}
	archives, err = svc.Archives(t.Context())
	if err != nil || len(archives) != 0 {
		t.Fatal(archives, err)
	}
	// A new QQ message may create a fresh conversation with the stable channel ID.
	put(t, s, "session", "s", conversation.Session{ID: "s", Messages: []agent.Message{}})
}

func TestArchiveBatchReportsBusyMissingAndSuccessfulRecords(t *testing.T) {
	svc, s := fixture(t)
	put(t, s, "run", "busy", conversation.Run{ID: "busy", Status: "running"})
	put(t, s, "run", "waiting", conversation.Run{ID: "waiting", Status: "completed", ReplyPending: true})
	put(t, s, "run", "done", conversation.Run{ID: "done", Status: "completed"})
	result := perform(t, svc, domain.ResourceRun, domain.ActionArchive, "busy", "waiting", "done", "missing")
	if len(result.Succeeded) != 1 || result.Succeeded[0] != "done" || len(result.Failed) != 3 {
		t.Fatal(result)
	}
	archives, err := svc.Archives(t.Context())
	if err != nil || len(archives) != 1 {
		t.Fatal(archives, err)
	}
}

func TestArchiveAndPurgeProtectReferencesAndDefaultPersona(t *testing.T) {
	svc, s := fixture(t)
	put(t, s, "persona", "p", persona.Persona{ID: "p", Default: true})
	put(t, s, "config", "c", agent.Config{ID: "c"})
	put(t, s, "session", "s", conversation.Session{ID: "s", ConfigID: "c", PersonaID: "p"})
	for _, item := range []struct {
		r  domain.Resource
		id string
	}{{domain.ResourcePersona, "p"}, {domain.ResourceConfig, "c"}} {
		result := perform(t, svc, item.r, domain.ActionArchive, item.id)
		if len(result.Failed) != 1 {
			t.Fatal(result)
		}
	}
	succeeded(t, perform(t, svc, domain.ResourceSession, domain.ActionArchive, "s"), 1)
	put(t, s, "task", "task", task.Task{ID: "task", SessionID: "s", Name: "保留通知目标"})
	result := perform(t, svc, domain.ResourceSession, domain.ActionPurge, "s")
	if len(result.Failed) != 1 {
		t.Fatal(result)
	}
	var session conversation.Session
	if err := s.Get(t.Context(), "session", "s", &session); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactHardPurgeRemovesFileAndRejectsReferencedFiles(t *testing.T) {
	svc, s := fixture(t)
	for _, id := range []string{"unused", "referenced"} {
		put(t, s, "artifact", id, artifact.Artifact{ID: id, Name: id})
		if err := os.WriteFile(svc.Files.Path(id), []byte("private bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(t, s, "task", "t", task.Task{ID: "t", Steps: []task.Step{{ID: "video", Arguments: map[string]any{"artifactId": "referenced"}}}})
	succeeded(t, perform(t, svc, domain.ResourceArtifact, domain.ActionArchive, "unused", "referenced"), 2)
	if _, err := os.Stat(svc.Files.Path("unused")); err != nil {
		t.Fatal("archive removed file", err)
	}
	result := perform(t, svc, domain.ResourceArtifact, domain.ActionPurge, "unused", "referenced")
	if len(result.Succeeded) != 1 || result.Succeeded[0] != "unused" || len(result.Failed) != 1 {
		t.Fatal(result)
	}
	if _, err := os.Stat(svc.Files.Path("unused")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file not removed", err)
	}
	if _, err := os.Stat(svc.Files.Path("referenced")); err != nil {
		t.Fatal("referenced file removed", err)
	}
}

func TestExecutionPurgeRemovesFrozenSnapshotAndStepResults(t *testing.T) {
	svc, s := fixture(t)
	now := time.Now()
	put(t, s, "execution", "e", task.TaskExecution{ID: "e", Status: "completed", Results: map[string]any{"step": map[string]any{"text": "private"}}, FinishedAt: &now})
	put(t, s, "execution-snapshot", "e", task.Snapshot{Task: task.Task{Steps: []task.Step{{ID: "step"}}}})
	put(t, s, "step-result", "e:step", map[string]any{"text": "private"})
	succeeded(t, perform(t, svc, domain.ResourceExecution, domain.ActionArchive, "e"), 1)
	succeeded(t, perform(t, svc, domain.ResourceExecution, domain.ActionPurge, "e"), 1)
	for kind, id := range map[string]string{"execution": "e", "execution-snapshot": "e", "step-result": "e:step"} {
		var v any
		if err := s.Get(t.Context(), kind, id, &v); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(kind, err)
		}
	}
}

func TestArchiveCannotRestoreAfterDependencyWasRemoved(t *testing.T) {
	svc, s := fixture(t)
	put(t, s, "session", "s", conversation.Session{ID: "s", ConfigID: "missing", PersonaID: "missing"})
	succeeded(t, perform(t, svc, domain.ResourceSession, domain.ActionArchive, "s"), 1)
	result := perform(t, svc, domain.ResourceSession, domain.ActionRestore, "s")
	if len(result.Failed) != 1 {
		t.Fatal(result)
	}
}

func TestCancelledBatchDoesNotRemoveRecords(t *testing.T) {
	svc, s := fixture(t)
	put(t, s, "artifact", "a", artifact.Artifact{ID: "a"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := svc.Batch(ctx, biz.Request{Resource: domain.ResourceArtifact, Action: domain.ActionArchive, IDs: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 1 {
		t.Fatal(result)
	}
}
