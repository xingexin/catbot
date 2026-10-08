package bootstrap

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xingexin/catbot/internal/domain/agent"
	artifactdomain "github.com/xingexin/catbot/internal/domain/artifact"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var groupTestToolNames = []string{
	"system__task_create", "system__task_list", "system__task_update", "system__task_control",
	"system__artifact_list", "system__artifact_read", "mail__watch",
}

// The scheduler and plugin records are fixtures; these tests do not connect to QQ or IMAP.
func groupToolFixture(t *testing.T, backgroundDepth int, allow []string) (*App, conversation.Run, conversation.Session) {
	t.Helper()
	a := testApp(t)
	source := conversation.Session{
		ID: "group-room-a-user-a", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "bot",
		ChannelRoom: "room-a", Recipient: "user-a", ConfigID: "config", PersonaID: "secretary",
	}
	if err := a.Store.Put(t.Context(), "session", source.ID, source); err != nil {
		t.Fatal(err)
	}
	var config agent.Config
	var persona persona.Persona
	if err := a.Store.Get(t.Context(), "config", source.ConfigID, &config); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.Get(t.Context(), "persona", source.PersonaID, &persona); err != nil {
		t.Fatal(err)
	}
	persona.Tools = allow
	if err := a.Store.Put(t.Context(), "persona", persona.ID, persona); err != nil {
		t.Fatal(err)
	}
	sessionID := source.ID
	for range backgroundDepth {
		background := conversation.Session{ID: "background-" + sessionID, Channel: "task", OriginSessionID: sessionID}
		if err := a.Store.Put(t.Context(), "session", background.ID, background); err != nil {
			t.Fatal(err)
		}
		sessionID = background.ID
	}
	seedMailPlugin(t, a)
	versions, err := a.Plugins.Snapshots(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	run := conversation.Run{ID: "group-run", SessionID: sessionID, Status: "running", Config: config, Persona: persona, Versions: versions}
	if err := a.Store.Put(t.Context(), "run", run.ID, run); err != nil {
		t.Fatal(err)
	}
	return a, run, source
}

func seedGroupToolTask(t *testing.T, a *App, id, sessionID string) taskentity.Task {
	t.Helper()
	task := taskentity.Task{
		ID: id, Name: id, Kind: "manual", Status: "active", ConfigID: "config", PersonaID: "secretary",
		SessionID: sessionID, Steps: []taskentity.Step{{ID: "remind", Kind: "agent", Prompt: "提醒"}},
	}
	if err := a.Store.Put(t.Context(), "task", task.ID, task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestGroupTaskListIsolatedToOriginConversation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		depth int
	}{{"direct group", 0}, {"background group", 1}, {"nested background group", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, run, source := groupToolFixture(t, tc.depth, groupTestToolNames)
			seedGroupToolTask(t, a, "mine", source.ID)
			for _, sessionID := range []string{"group-room-a-user-b", "group-room-b-user-a", "private-user-a", "session", ""} {
				seedGroupToolTask(t, a, "other-"+sessionID, sessionID)
			}
			for _, want := range []int{1, 2} {
				value, err := a.Tools.Call(t.Context(), run.ID, "system__task_list", map[string]any{}, "list-reused")
				if err != nil {
					t.Fatal(err)
				}
				tasks, ok := value.([]taskentity.Task)
				if !ok || len(tasks) != want {
					t.Fatalf("got %#v, want %d own tasks", value, want)
				}
				for _, task := range tasks {
					if task.SessionID != source.ID {
						t.Fatalf("disclosed another conversation: %+v", task)
					}
				}
				seedGroupToolTask(t, a, "mine-new", source.ID)
			}
		})
	}
}

func TestGroupTaskMutationsRejectOtherConversationsBeforeCache(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		depth int
	}{{"direct group", 0}, {"background group", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, run, source := groupToolFixture(t, tc.depth, groupTestToolNames)
			seedGroupToolTask(t, a, "mine", source.ID)
			// Populate the same operation's cache using an authorized task first.
			if _, err := a.Tools.Call(t.Context(), run.ID, "system__task_update", map[string]any{"id": "mine", "name": "updated"}, "reused-update"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Tools.Call(t.Context(), run.ID, "system__task_control", map[string]any{"id": "mine", "action": "trigger"}, "reused-control"); err != nil {
				t.Fatal(err)
			}
			for _, sessionID := range []string{"group-room-a-user-b", "group-room-b-user-a", "private-user-a", "session", ""} {
				task := seedGroupToolTask(t, a, "other-"+sessionID, sessionID)
				if _, err := a.Tools.Call(t.Context(), run.ID, "system__task_update", map[string]any{"id": task.ID, "name": "stolen"}, "reused-update"); err == nil || !strings.Contains(err.Error(), "not authorized") {
					t.Fatalf("foreign update returned %v", err)
				}
				for _, action := range []string{"pause", "resume", "cancel", "trigger"} {
					if _, err := a.Tools.Call(t.Context(), run.ID, "system__task_control", map[string]any{"id": task.ID, "action": action}, "reused-control"); err == nil || !strings.Contains(err.Error(), "not authorized") {
						t.Fatalf("foreign %s returned %v", action, err)
					}
				}
				var saved taskentity.Task
				if err := a.Store.Get(t.Context(), "task", task.ID, &saved); err != nil {
					t.Fatal(err)
				}
				before, _ := json.Marshal(task)
				after, _ := json.Marshal(saved)
				if string(before) != string(after) {
					t.Fatalf("foreign task changed: %s", after)
				}
			}
			scheduler := a.Tasks.Scheduler.(*fakeScheduler)
			if len(scheduler.applied) != 1 || len(scheduler.operations) != 1 {
				t.Fatalf("unauthorized mutations reached scheduler: %+v", scheduler)
			}
		})
	}
}

func TestGroupTaskOwnerCanUpdateAndControlFromBackground(t *testing.T) {
	t.Parallel()
	a, run, source := groupToolFixture(t, 1, groupTestToolNames)
	seedGroupToolTask(t, a, "mine", source.ID)
	if _, err := a.Tools.Call(t.Context(), run.ID, "system__task_update", map[string]any{"id": "mine", "name": "新的提醒"}, "rename"); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"pause", "resume", "trigger", "cancel"} {
		if _, err := a.Tools.Call(t.Context(), run.ID, "system__task_control", map[string]any{"id": "mine", "action": action}, action); err != nil {
			t.Fatalf("own %s: %v", action, err)
		}
		var saved taskentity.Task
		if err := a.Store.Get(t.Context(), "task", "mine", &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Name != "新的提醒" || saved.SessionID != source.ID {
			t.Fatal("update lost task ownership or name", saved)
		}
		if action == "pause" && !saved.Paused || action == "resume" && saved.Paused || action == "cancel" && saved.Status != "cancelled" {
			t.Fatalf("%s did not update state: %+v", action, saved)
		}
	}
}

func TestGroupTaskUpdateWithoutSchedulerReturnsError(t *testing.T) {
	t.Parallel()
	a, run, source := groupToolFixture(t, 0, groupTestToolNames)
	seedGroupToolTask(t, a, "mine", source.ID)
	a.Tasks.Scheduler = nil
	if _, err := a.Tools.Call(t.Context(), run.ID, "system__task_update", map[string]any{"id": "mine", "name": "new name"}, "no-scheduler"); err == nil || !strings.Contains(err.Error(), "Temporal is not connected") {
		t.Fatalf("missing scheduler: %v", err)
	}
}

type groupTaskLockGate struct {
	store.Store
	entered chan struct{}
	release chan struct{}
	used    atomic.Bool
}

func (s *groupTaskLockGate) Lock(ctx context.Context, key string) (func(), error) {
	if key == "task:mine" && s.used.CompareAndSwap(false, true) {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.Store.Lock(ctx, key)
}

func TestGroupTaskMutationRechecksOwnershipUnderTaskLock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
	}{
		{"update", "system__task_update", map[string]any{"id": "mine", "name": "group update"}},
		{"pause", "system__task_control", map[string]any{"id": "mine", "action": "pause"}},
		{"trigger", "system__task_control", map[string]any{"id": "mine", "action": "trigger"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, run, source := groupToolFixture(t, 1, groupTestToolNames)
			task := seedGroupToolTask(t, a, "mine", source.ID)
			gate := &groupTaskLockGate{Store: a.Store, entered: make(chan struct{}), release: make(chan struct{})}
			setTestStore(a, gate)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := a.Tools.Call(ctx, run.ID, tc.tool, tc.args, "ownership-race")
				done <- err
			}()
			select {
			case <-gate.entered:
			case err := <-done:
				t.Fatalf("mutation completed without waiting for task lock: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// An admin reassigns the task after the initial authorization check
			// but before the group's mutation acquires the task lock.
			task.SessionID, task.Name = "session", "admin reassigned"
			if _, err := a.Tasks.SaveTask(ctx, task); err != nil {
				close(gate.release)
				t.Fatal(err)
			}
			close(gate.release)
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "not authorized") {
					t.Fatalf("ownership change was ignored: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var saved taskentity.Task
			if err := a.Store.Get(t.Context(), "task", task.ID, &saved); err != nil {
				t.Fatal(err)
			}
			scheduler := a.Tasks.Scheduler.(*fakeScheduler)
			if saved.SessionID != "session" || saved.Name != "admin reassigned" || saved.Paused || len(scheduler.applied) != 1 || len(scheduler.operations) != 0 {
				t.Fatalf("reassigned task was mutated or triggered: task=%+v scheduler=%+v", saved, scheduler)
			}
		})
	}
}

func TestGroupToolsRequireExplicitAllowlistAndNeverExposeArtifacts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		depth int
		allow []string
		want  []string
	}{
		{"group without allowlist", 0, nil, nil},
		{"background without allowlist", 1, nil, nil},
		{"explicit empty allowlist", 0, []string{}, nil},
		{"allowed group tools", 0, []string{"mail__watch", "system__task_list", "system__artifact_list", "system__artifact_read"}, []string{"mail__watch", "system__task_list"}},
		{"allowed background tools", 1, []string{"mail__watch", "system__task_list", "system__artifact_list", "system__artifact_read"}, []string{"mail__watch", "system__task_list"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, run, _ := groupToolFixture(t, tc.depth, tc.allow)
			if err := a.Store.Put(t.Context(), "artifact", "private", artifactdomain.Artifact{ID: "private"}); err != nil {
				t.Fatal(err)
			}
			tools, err := a.Tools.Tools(t.Context(), run)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(tools))
			for _, tool := range tools {
				names = append(names, tool.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, tc.want) {
				t.Fatalf("tools = %v, want %v", names, tc.want)
			}
			for _, name := range []string{"system__artifact_list", "system__artifact_read"} {
				args := map[string]any{}
				if name == "system__artifact_read" {
					args["id"] = "private"
				}
				if _, err := a.Tools.Call(t.Context(), run.ID, name, args, "guess-artifact"); err == nil || !strings.Contains(err.Error(), "not authorized") {
					t.Fatalf("artifact call accepted or wrong error: %v", err)
				}
			}
			if len(tc.want) == 0 {
				if _, err := a.Tools.Call(t.Context(), run.ID, "mail__watch", map[string]any{}, "guess-plugin"); err == nil || !strings.Contains(err.Error(), "not authorized") {
					t.Fatalf("plugin call without explicit grant returned %v", err)
				}
			}
		})
	}
}

func TestGroupTaskCreateKeepsOriginAndCannotBypassPersonaTools(t *testing.T) {
	t.Parallel()
	a, run, source := groupToolFixture(t, 2, []string{"system__task_create"})
	args := map[string]any{"name": "群提醒", "kind": "manual", "notify": false,
		"steps": []any{map[string]any{"id": "watch", "kind": "tool", "tool": "mail__watch", "arguments": map[string]any{}}},
	}
	if _, err := a.Tools.Call(t.Context(), run.ID, "system__task_create", args, "denied-plugin-step"); err == nil || !strings.Contains(err.Error(), "tool not available") {
		t.Fatalf("task_create bypassed persona restrictions: %v", err)
	}
	tasks, err := store.All[taskentity.Task](t.Context(), a.Store, "task")
	if err != nil || len(tasks) != 0 {
		t.Fatal("rejected task persisted", tasks, err)
	}
	args["steps"] = []any{map[string]any{"id": "remind", "kind": "agent", "prompt": "提醒群成员"}}
	value, err := a.Tools.Call(t.Context(), run.ID, "system__task_create", args, "allowed-agent-step")
	if err != nil {
		t.Fatal(err)
	}
	task, ok := value.(taskentity.Task)
	if !ok || task.SessionID != source.ID || task.ConfigID != run.Config.ID || task.PersonaID != run.Persona.ID {
		t.Fatalf("created task lost original context: %#v", value)
	}
}

func TestGroupBuiltinOperationCacheDoesNotCrossConversations(t *testing.T) {
	t.Parallel()
	a, run, source := groupToolFixture(t, 0, groupTestToolNames)
	args := map[string]any{"name": "提醒", "kind": "manual", "notify": false,
		"steps": []any{map[string]any{"id": "remind", "kind": "agent", "prompt": "提醒"}},
	}
	other := source
	other.ID, other.Recipient = "group-room-a-user-b", "user-b"
	if err := a.Store.Put(t.Context(), "session", other.ID, other); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, sessionID := range []string{"session", source.ID, other.ID} {
		run.SessionID = sessionID
		if err := a.Store.Put(t.Context(), "run", run.ID, run); err != nil {
			t.Fatal(err)
		}
		value, err := a.Tools.Call(t.Context(), run.ID, "system__task_create", args, "shared-operation-id")
		if err != nil {
			t.Fatal(err)
		}
		task, ok := value.(taskentity.Task)
		if !ok || task.SessionID != sessionID || ids[task.ID] {
			t.Fatalf("operation cache crossed conversation: %#v", value)
		}
		ids[task.ID] = true
	}
}

func TestNonGroupToolsRetainExistingAccess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		session conversation.Session
		persist bool
	}{
		{"Web", conversation.Session{ID: "web", Channel: "web"}, true},
		{"private QQ", conversation.Session{ID: "private", Channel: "qq", Recipient: "user-a"}, true},
		{"legacy fixture without session", conversation.Session{ID: "missing"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, run, _ := groupToolFixture(t, 0, nil)
			if tc.persist {
				if err := a.Store.Put(t.Context(), "session", tc.session.ID, tc.session); err != nil {
					t.Fatal(err)
				}
			}
			run.SessionID = tc.session.ID
			if err := a.Store.Put(t.Context(), "run", run.ID, run); err != nil {
				t.Fatal(err)
			}
			seedGroupToolTask(t, a, "private-task", "private")
			seedGroupToolTask(t, a, "web-task", "web")
			value, err := a.Tools.Call(t.Context(), run.ID, "system__task_list", map[string]any{}, "list")
			if err != nil {
				t.Fatal(err)
			}
			if tasks, ok := value.([]taskentity.Task); !ok || len(tasks) != 2 {
				t.Fatalf("non-group task visibility changed: %#v", value)
			}
			tools, err := a.Tools.Tools(t.Context(), run)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"mail__watch", "system__artifact_list", "system__artifact_read"} {
				if !slices.ContainsFunc(tools, func(tool agent.Tool) bool { return tool.Name == name }) {
					t.Fatalf("non-group tool %s disappeared", name)
				}
			}
		})
	}
}

type groupToolSessionErrorStore struct {
	store.Store
	err error
}

func (s groupToolSessionErrorStore) Get(ctx context.Context, kind, id string, target any) error {
	if kind == "session" {
		return s.err
	}
	return s.Store.Get(ctx, kind, id, target)
}

func TestGroupToolSourceFailureDoesNotExpandPermissions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *App, conversation.Run)
	}{
		{"missing declared origin", func(t *testing.T, a *App, run conversation.Run) {
			if err := a.Store.Put(t.Context(), "session", run.SessionID, conversation.Session{ID: run.SessionID, Channel: "task", OriginSessionID: "missing-origin"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"origin cycle", func(t *testing.T, a *App, run conversation.Run) {
			if err := a.Store.Put(t.Context(), "session", run.SessionID, conversation.Session{ID: run.SessionID, Channel: "task", OriginSessionID: run.SessionID}); err != nil {
				t.Fatal(err)
			}
		}},
		{"store error", func(_ *testing.T, a *App, _ conversation.Run) {
			setTestStore(a, groupToolSessionErrorStore{Store: a.Store, err: errors.New("session storage unavailable")})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, run, _ := groupToolFixture(t, 1, nil)
			tc.edit(t, a, run)
			if _, err := a.Tools.Tools(t.Context(), run); err == nil {
				t.Fatal("source error exposed tools")
			}
			if _, err := a.Tools.Call(t.Context(), run.ID, "system__artifact_list", map[string]any{}, "source-failure"); err == nil {
				t.Fatal("source error allowed artifact access")
			}
		})
	}
}
