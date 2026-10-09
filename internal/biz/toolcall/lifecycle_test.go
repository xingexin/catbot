package toolcall

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/biz/plugin"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/persona"
	"github.com/xingexin/catbot/internal/infra/store"
)

func seedListToolRun(t *testing.T, s store.Store, group bool) *Service {
	t.Helper()
	session := conversation.Session{ID: "source", Channel: "web"}
	if group {
		session.Channel = "qq"
		session.ChannelRoom = "group"
	}
	run := conversation.Run{ID: "run", SessionID: session.ID, Status: "running", Config: agent.Config{Capabilities: agent.Capabilities{Tools: true}}, Persona: persona.Persona{Tools: []string{"system__task_list", "system__artifact_list"}}}
	for _, record := range []struct {
		kind, id string
		value    any
	}{{"session", session.ID, session}, {"run", run.ID, run}} {
		if err := s.Put(t.Context(), record.kind, record.id, record.value); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{Store: s, Plugins: &plugin.Manager{Store: s}}
}

func toolResultIDs(t *testing.T, value any) []string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var records []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

func TestDefaultAgentListsHideArchivedRecordsAndReflectRestore(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource lifecycle.Resource
	}{
		{name: "system__task_list", resource: lifecycle.ResourceTask},
		{name: "system__artifact_list", resource: lifecycle.ResourceArtifact},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := store.NewMemory()
			service := seedListToolRun(t, s, false)
			for _, id := range []string{"active", "hidden"} {
				if err := s.Put(t.Context(), tc.resource.StorageKind(), id, map[string]any{"id": id, "name": id}); err != nil {
					t.Fatal(err)
				}
			}
			if err := lifecycleRepository.Mark(t.Context(), s, lifecycle.ArchiveRecord{Resource: tc.resource, RecordID: "hidden", Name: "hidden", ArchivedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			// Another resource with the same ID must not hide the active record.
			if err := lifecycleRepository.Mark(t.Context(), s, lifecycle.ArchiveRecord{Resource: lifecycle.ResourceRun, RecordID: "active", Name: "different resource", ArchivedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			value, err := service.Call(t.Context(), "run", tc.name, map[string]any{}, "same-read")
			if err != nil {
				t.Fatal(err)
			}
			ids := toolResultIDs(t, value)
			if len(ids) != 1 || ids[0] != "active" {
				t.Fatal("archived items disclosed or resource identity mixed", ids)
			}
			if err := lifecycleRepository.Unmark(t.Context(), s, tc.resource, "hidden"); err != nil {
				t.Fatal(err)
			}
			value, err = service.Call(t.Context(), "run", tc.name, map[string]any{}, "same-read")
			if err != nil {
				t.Fatal(err)
			}
			if ids := toolResultIDs(t, value); len(ids) != 2 {
				t.Fatal("restored record omitted from fresh tool result", ids)
			}
		})
	}
}

func TestGroupTaskListKeepsConversationAuthorizationWhenFilteringArchive(t *testing.T) {
	s := store.NewMemory()
	service := seedListToolRun(t, s, true)
	for _, record := range []struct{ id, session string }{{"own", "source"}, {"archived", "source"}, {"other", "different"}} {
		if err := s.Put(t.Context(), "task", record.id, map[string]any{"id": record.id, "sessionId": record.session}); err != nil {
			t.Fatal(err)
		}
	}
	if err := lifecycleRepository.Mark(t.Context(), s, lifecycle.ArchiveRecord{Resource: lifecycle.ResourceTask, RecordID: "archived", Name: "archived", ArchivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	value, err := service.Call(t.Context(), "run", "system__task_list", map[string]any{}, "group-list")
	if err != nil {
		t.Fatal(err)
	}
	ids := toolResultIDs(t, value)
	if len(ids) != 1 || ids[0] != "own" {
		t.Fatal("group scope or archive scope lost", ids)
	}
}

type failingArchiveRead struct{ store.Store }

var errArchiveUnavailable = errors.New("archive index unavailable")

func (s failingArchiveRead) Get(ctx context.Context, kind, id string, value any) error {
	if kind == lifecycle.ArchiveStorageKind {
		return errArchiveUnavailable
	}
	return s.Store.Get(ctx, kind, id, value)
}
func TestAgentListFailsClosedWhenArchiveIndexCannotBeRead(t *testing.T) {
	memory := store.NewMemory()
	s := failingArchiveRead{Store: memory}
	service := seedListToolRun(t, s, false)
	if err := s.Put(t.Context(), "task", "hidden", map[string]any{"id": "hidden"}); err != nil {
		t.Fatal(err)
	}
	value, err := service.Call(t.Context(), "run", "system__task_list", map[string]any{}, "list")
	if !errors.Is(err, errArchiveUnavailable) || value != nil {
		t.Fatal("tool exposed records despite archive lookup failure", value, err)
	}
}
