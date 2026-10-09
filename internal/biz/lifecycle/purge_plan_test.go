package lifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	biz "github.com/xingexin/catbot/internal/biz/lifecycle"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	domain "github.com/xingexin/catbot/internal/domain/lifecycle"
	repository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/domain/persona"
	"github.com/xingexin/catbot/internal/domain/plugin"
	task "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
)

type previewOnlyHandler struct{ t *testing.T }

func (h previewOnlyHandler) Archive(context.Context, string) error {
	h.t.Fatal("preview archived data")
	return nil
}
func (h previewOnlyHandler) Restore(context.Context, string) error {
	h.t.Fatal("preview restored data")
	return nil
}
func (h previewOnlyHandler) Purge(context.Context, string) error {
	h.t.Fatal("preview purged data")
	return nil
}

type previewOnlyStore struct {
	*store.Memory
	t *testing.T
}

func (s previewOnlyStore) Put(context.Context, string, string, any) error {
	s.t.Fatal("preview wrote data")
	return nil
}
func (s previewOnlyStore) Delete(context.Context, string, string) error {
	s.t.Fatal("preview deleted data")
	return nil
}
func (s previewOnlyStore) Append(context.Context, store.EventRecord) error {
	s.t.Fatal("preview appended event")
	return nil
}

func markPreview(t *testing.T, s store.Store, resource domain.Resource, id string) {
	t.Helper()
	if err := repository.Mark(t.Context(), s, domain.ArchiveRecord{Resource: resource, RecordID: id, ArchivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}
func preview(t *testing.T, svc *biz.Service, resource domain.Resource, id string) biz.PurgePlan {
	t.Helper()
	plan, err := svc.PreviewPurge(t.Context(), biz.PurgeRequest{Items: []biz.PurgeTarget{{Resource: resource, ID: id}}})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func planIndex(plan biz.PurgePlan, resource domain.Resource, id string) int {
	for i, item := range plan.Items {
		if item.Resource == resource && item.ID == id {
			return i
		}
	}
	return -1
}

func TestPurgePreviewIncludesArchivedAndNestedDependenciesWithoutWrites(t *testing.T) {
	t.Parallel()
	svc, s := fixture(t)
	svc.Handlers = map[domain.Resource]biz.Handler{domain.ResourceTask: previewOnlyHandler{t}}
	config := agent.Config{ID: "config", Name: "model", CredentialID: "shared"}
	put(t, s, "config", config.ID, config)
	put(t, s, "secret", "shared", vault.Record{ID: "shared"})
	put(t, s, "persona", "persona", persona.Persona{ID: "persona"})
	put(t, s, "config", "unrelated", agent.Config{ID: "unrelated", CredentialID: "shared"})
	put(t, s, "session", "session", conversation.Session{ID: "session", ConfigID: config.ID, PersonaID: "persona"})
	put(t, s, "session", "unrelated", conversation.Session{ID: "unrelated", ConfigID: "unrelated"})
	put(t, s, "session", "child", conversation.Session{ID: "child", OriginSessionID: "session"})
	definition := task.Task{ID: "task", Name: "task", ConfigID: config.ID, PersonaID: "persona", SessionID: "session", Status: "active"}
	put(t, s, "task", definition.ID, definition)
	put(t, s, "run", "run", conversation.Run{ID: "run", SessionID: "session", Status: "completed", Config: config})
	put(t, s, "execution", "exec", task.TaskExecution{ID: "exec", TaskID: definition.ID, Status: "completed"})
	put(t, s, "execution-snapshot", "exec", task.Snapshot{Task: definition, Config: config})
	put(t, s, "session", "task-session-exec", conversation.Session{ID: "task-session-exec", ConfigID: "unrelated"})
	put(t, s, "run", "background", conversation.Run{ID: "background", SessionID: "task-session-exec", Status: "completed"})
	put(t, s, "notification", "notification-reply:run", messaging.Notification{ID: "notification-reply:run", SessionID: "session", Status: "sent", OperationID: "reply:run"})
	put(t, s, "delivery", "reply:run", messaging.Delivery{ID: "reply:run", SessionID: "session", Status: "sent"})
	markPreview(t, s, domain.ResourceConfig, config.ID)
	markPreview(t, s, domain.ResourceSession, "session")
	svc.Store = previewOnlyStore{Memory: s, t: t}
	plan := preview(t, svc, domain.ResourceConfig, config.ID)
	if len(plan.Blockers) != 0 {
		t.Fatalf("unexpected blockers: %+v", plan.Blockers)
	}
	for _, target := range []biz.PurgeTarget{
		{Resource: domain.ResourceSession, ID: "session"}, {Resource: domain.ResourceSession, ID: "child"},
		{Resource: domain.ResourceTask, ID: "task"}, {Resource: domain.ResourceRun, ID: "run"},
		{Resource: domain.ResourceExecution, ID: "exec"}, {Resource: domain.ResourceSession, ID: "task-session-exec"},
		{Resource: domain.ResourceRun, ID: "background"}, {Resource: domain.ResourceNotification, ID: "notification-reply:run"},
		{Resource: domain.ResourceDelivery, ID: "reply:run"},
	} {
		if planIndex(plan, target.Resource, target.ID) < 0 {
			t.Fatalf("dependency omitted: %+v", target)
		}
	}
	if len(plan.Items) != 10 {
		t.Fatalf("unexpected closure: %+v", plan.Items)
	}
	if planIndex(plan, domain.ResourceSecret, "shared") >= 0 || planIndex(plan, domain.ResourceConfig, "unrelated") >= 0 || planIndex(plan, domain.ResourcePersona, "persona") >= 0 {
		t.Fatal("forward/shared dependency was selected")
	}
	if index := planIndex(plan, domain.ResourceSession, "session"); !plan.Items[index].Archived {
		t.Fatal("archived session state lost")
	}
	for _, edge := range [][2]biz.PurgeTarget{
		{{Resource: domain.ResourceRun, ID: "background"}, {Resource: domain.ResourceSession, ID: "task-session-exec"}},
		{{Resource: domain.ResourceSession, ID: "task-session-exec"}, {Resource: domain.ResourceExecution, ID: "exec"}},
		{{Resource: domain.ResourceExecution, ID: "exec"}, {Resource: domain.ResourceTask, ID: "task"}},
		{{Resource: domain.ResourceTask, ID: "task"}, {Resource: domain.ResourceSession, ID: "session"}},
		{{Resource: domain.ResourceSession, ID: "session"}, {Resource: domain.ResourceConfig, ID: "config"}},
	} {
		if planIndex(plan, edge[0].Resource, edge[0].ID) >= planIndex(plan, edge[1].Resource, edge[1].ID) {
			t.Fatalf("dependent ordered after referenced record: %+v", edge)
		}
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := svc.PreviewPurge(cancelled, biz.PurgeRequest{Items: []biz.PurgeTarget{{Resource: domain.ResourceConfig, ID: "config"}}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPurgePreviewTokenTracksClosureContentsAndArchiveState(t *testing.T) {
	t.Parallel()
	svc, s := fixture(t)
	put(t, s, "config", "model", agent.Config{ID: "model"})
	markPreview(t, s, domain.ResourceConfig, "model")
	before := preview(t, svc, domain.ResourceConfig, "model")
	if got := preview(t, svc, domain.ResourceConfig, "model"); got.Token != before.Token {
		t.Fatal("unchanged preview is not stable")
	}
	put(t, s, "session", "new", conversation.Session{ID: "new", ConfigID: "model", Title: "first"})
	added := preview(t, svc, domain.ResourceConfig, "model")
	if added.Token == before.Token {
		t.Fatal("added reference did not invalidate confirmation")
	}
	put(t, s, "session", "new", conversation.Session{ID: "new", ConfigID: "model", Title: "changed"})
	changed := preview(t, svc, domain.ResourceConfig, "model")
	if changed.Token == added.Token {
		t.Fatal("changed content did not invalidate confirmation")
	}
	markPreview(t, s, domain.ResourceSession, "new")
	archived := preview(t, svc, domain.ResourceConfig, "model")
	if archived.Token == changed.Token {
		t.Fatal("changed archive state did not invalidate confirmation")
	}
	put(t, s, "run", "run", conversation.Run{ID: "run", SessionID: "new", Status: "completed"})
	withRun := preview(t, svc, domain.ResourceConfig, "model")
	if err := s.Append(t.Context(), store.EventRecord{RunID: "run", Type: "text.delta", Data: map[string]any{"text": "sensitive event"}}); err != nil {
		t.Fatal(err)
	}
	withEvent := preview(t, svc, domain.ResourceConfig, "model")
	if withEvent.Token == withRun.Token {
		t.Fatal("owned event content did not invalidate confirmation")
	}
	duplicate, err := svc.PreviewPurge(t.Context(), biz.PurgeRequest{Items: []biz.PurgeTarget{{Resource: domain.ResourceConfig, ID: "model"}, {Resource: domain.ResourceConfig, ID: "model"}}})
	if err != nil || !reflect.DeepEqual(duplicate, withEvent) {
		t.Fatalf("duplicate root changed plan: %+v %v", duplicate, err)
	}
}

func TestPurgePreviewReportsOperationalBlockers(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"running", "queued", "uncertain", "unknown"} {
		t.Run(status, func(t *testing.T) {
			svc, s := fixture(t)
			put(t, s, "config", "model", agent.Config{ID: "model"})
			put(t, s, "run", "run", conversation.Run{ID: "run", Config: agent.Config{ID: "model"}, Status: status})
			markPreview(t, s, domain.ResourceConfig, "model")
			plan := preview(t, svc, domain.ResourceConfig, "model")
			if len(plan.Blockers) != 1 || plan.Blockers[0].ID != "run" {
				t.Fatalf("unsafe dependency not blocked: %+v", plan)
			}
		})
	}
	svc, s := fixture(t)
	put(t, s, "persona", "default", persona.Persona{ID: "default", Default: true})
	markPreview(t, s, domain.ResourcePersona, "default")
	plan := preview(t, svc, domain.ResourcePersona, "default")
	if len(plan.Blockers) != 1 || !strings.Contains(plan.Blockers[0].Reason, "默认人格") {
		t.Fatal(plan)
	}
	put(t, s, "config", "bound", agent.Config{ID: "bound"})
	markPreview(t, s, domain.ResourceConfig, "bound")
	svc.CheckBindings = func(context.Context, string, string) error { return errors.New("仍用于 QQ 绑定") }
	if plan := preview(t, svc, domain.ResourceConfig, "bound"); len(plan.Blockers) != 1 || !strings.Contains(plan.Blockers[0].Reason, "QQ") {
		t.Fatal(plan)
	}
}

func TestPurgePreviewPluginVersionsAndPrivateCredentials(t *testing.T) {
	t.Parallel()
	svc, s := fixture(t)
	svc.Handlers = map[domain.Resource]biz.Handler{domain.ResourcePlugin: previewOnlyHandler{t}}
	put(t, s, "config", "old", agent.Config{ID: "old"})
	put(t, s, "config", "shared", agent.Config{ID: "shared", CredentialID: "plugin-mail-shared"})
	current := plugin.Plugin{ID: "mail", Manifest: plugin.Manifest{Name: "邮箱"}, Secrets: map[string]string{"private": "plugin-mail-private", "shared": "plugin-mail-shared"}}
	put(t, s, "plugin", "mail", current)
	put(t, s, "plugin-version", "mail:old", plugin.Plugin{ID: "mail", Config: map[string]any{"summaryConfigId": "old"}})
	put(t, s, "plugin-data:mail", "cursor", 123)
	put(t, s, "secret", "plugin-mail-private", vault.Record{ID: "plugin-mail-private"})
	put(t, s, "secret", "plugin-mail-shared", vault.Record{ID: "plugin-mail-shared"})
	markPreview(t, s, domain.ResourceConfig, "old")
	plan := preview(t, svc, domain.ResourceConfig, "old")
	if len(plan.Blockers) != 0 || planIndex(plan, domain.ResourcePlugin, "mail") < 0 || planIndex(plan, domain.ResourceSecret, "plugin-mail-private") < 0 || planIndex(plan, domain.ResourceSecret, "plugin-mail-shared") >= 0 {
		t.Fatalf("bad private/shared closure: %+v", plan)
	}
	if planIndex(plan, domain.ResourcePlugin, "mail") >= planIndex(plan, domain.ResourceSecret, "plugin-mail-private") {
		t.Fatal("plugin must precede privately owned credential cleanup")
	}
	put(t, s, "plugin-data:mail", "cursor", 124)
	if changed := preview(t, svc, domain.ResourceConfig, "old"); changed.Token == plan.Token {
		t.Fatal("owned plugin data change not fingerprinted")
	}
}

func TestPurgePreviewIgnoresTextMentionsAndRejectsCycles(t *testing.T) {
	t.Parallel()
	svc, s := fixture(t)
	svc.Handlers = map[domain.Resource]biz.Handler{domain.ResourceTask: previewOnlyHandler{t}}
	put(t, s, "artifact", "file", map[string]any{"id": "file", "name": "video"})
	put(t, s, "task", "uses", task.Task{ID: "uses", Status: "active", Steps: []task.Step{{Arguments: map[string]any{"artifactId": "file"}}}})
	put(t, s, "task", "mentions", task.Task{ID: "mentions", Name: "file", Status: "active", Steps: []task.Step{{Prompt: "file", Arguments: map[string]any{"title": "file"}}}})
	markPreview(t, s, domain.ResourceArtifact, "file")
	plan := preview(t, svc, domain.ResourceArtifact, "file")
	if len(plan.Items) != 2 || planIndex(plan, domain.ResourceTask, "uses") < 0 || planIndex(plan, domain.ResourceTask, "mentions") >= 0 {
		t.Fatalf("text mention triggered cascade: %+v", plan)
	}
	put(t, s, "session", "a", conversation.Session{ID: "a", OriginSessionID: "b"})
	put(t, s, "session", "b", conversation.Session{ID: "b", OriginSessionID: "a"})
	markPreview(t, s, domain.ResourceSession, "a")
	plan = preview(t, svc, domain.ResourceSession, "a")
	if len(plan.Blockers) != 2 || !strings.Contains(plan.Blockers[0].Reason, "循环") {
		t.Fatalf("cycle did not block: %+v", plan)
	}
}

func TestPurgePreviewRequiresArchivedExistingRoots(t *testing.T) {
	t.Parallel()
	svc, s := fixture(t)
	put(t, s, "config", "active", agent.Config{ID: "active"})
	for _, in := range []biz.PurgeRequest{
		{}, {Items: []biz.PurgeTarget{{Resource: domain.ResourceUnknown, ID: "active"}}},
		{Items: []biz.PurgeTarget{{Resource: domain.ResourceConfig, ID: "active"}}},
		{Items: []biz.PurgeTarget{{Resource: domain.ResourceConfig, ID: "missing"}}},
	} {
		if _, err := svc.PreviewPurge(t.Context(), in); err == nil {
			t.Fatalf("invalid root accepted: %+v", in)
		}
	}
}

func TestPurgePreviewNotificationIncludesItsOperationDelivery(t *testing.T) {
	t.Parallel()
	svc, s := fixture(t)
	put(t, s, "notification", "notice", messaging.Notification{ID: "notice", OperationID: "retry-send", Status: "sent"})
	put(t, s, "delivery", "retry-send", messaging.Delivery{ID: "retry-send", Status: "sent"})
	put(t, s, "delivery", "unrelated", messaging.Delivery{ID: "unrelated", Status: "sent"})
	markPreview(t, s, domain.ResourceNotification, "notice")
	plan := preview(t, svc, domain.ResourceNotification, "notice")
	if len(plan.Items) != 2 || plan.Items[0].Resource != domain.ResourceDelivery || plan.Items[0].ID != "retry-send" {
		t.Fatalf("notification delivery omitted or unrelated delivery included: %+v", plan)
	}
}

func TestPurgePreviewIncludesPrivateKeyBecomingUnsharedWithinPlan(t *testing.T) {
	t.Parallel()
	svc, s := fixture(t)
	svc.Handlers = map[domain.Resource]biz.Handler{domain.ResourcePlugin: previewOnlyHandler{t}}
	put(t, s, "plugin", "mail", plugin.Plugin{ID: "mail", Secrets: map[string]string{"password": "plugin-mail-key"}})
	put(t, s, "secret", "plugin-mail-key", vault.Record{ID: "plugin-mail-key"})
	put(t, s, "run", "run", conversation.Run{ID: "run", Status: "completed", Config: agent.Config{CredentialID: "plugin-mail-key"}, Versions: map[string]string{"mail": "mail:v1"}})
	markPreview(t, s, domain.ResourcePlugin, "mail")
	plan := preview(t, svc, domain.ResourcePlugin, "mail")
	if len(plan.Items) != 3 || planIndex(plan, domain.ResourceSecret, "plugin-mail-key") < 0 {
		t.Fatalf("owned key could be deleted without appearing in preview: %+v", plan)
	}
	if planIndex(plan, domain.ResourceRun, "run") >= planIndex(plan, domain.ResourcePlugin, "mail") || planIndex(plan, domain.ResourcePlugin, "mail") >= planIndex(plan, domain.ResourceSecret, "plugin-mail-key") {
		t.Fatalf("wrong private-key dependency order: %+v", plan.Items)
	}
}
