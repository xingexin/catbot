package lifecycle_test

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	lifecyclebiz "github.com/xingexin/catbot/internal/biz/lifecycle"
	pluginbiz "github.com/xingexin/catbot/internal/biz/plugin"
	taskbiz "github.com/xingexin/catbot/internal/biz/task"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepo "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/messaging"
	plugindomain "github.com/xingexin/catbot/internal/domain/plugin"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
)

type cascadeHandlersScheduler struct {
	cancelled []string
	applied   int
	triggered int
}

func (s *cascadeHandlersScheduler) Apply(context.Context, taskentity.Task, *taskentity.Task) error {
	s.applied++
	return nil
}
func (s *cascadeHandlersScheduler) Trigger(context.Context, taskentity.Task, string) (string, error) {
	s.triggered++
	return "unexpected", nil
}
func (s *cascadeHandlersScheduler) Cancel(_ context.Context, task taskentity.Task) error {
	s.cancelled = append(s.cancelled, task.ID)
	return nil
}
func (*cascadeHandlersScheduler) Ping(context.Context) error { return nil }

type cascadeHandlersRuntime struct{ closed []string }

func (*cascadeHandlersRuntime) Health(context.Context, string, plugindomain.Plugin, func(context.Context) (map[string]any, error)) error {
	return errors.New("cascade must not start plugin processes")
}
func (*cascadeHandlersRuntime) Call(context.Context, string, plugindomain.Plugin, func(context.Context) (map[string]any, error), string, map[string]any, string) (any, error) {
	return nil, errors.New("cascade must not run plugin tools")
}
func (*cascadeHandlersRuntime) Close() {}
func (r *cascadeHandlersRuntime) CloseVersions(keys []string) {
	r.closed = append(r.closed, keys...)
}

type cascadeHandlersFixture struct {
	service   *lifecyclebiz.Service
	plugins   *pluginbiz.Manager
	scheduler *cascadeHandlersScheduler
	runtime   *cascadeHandlersRuntime
	vault     *vault.Vault
	plugin    plugindomain.Plugin
	version   string
}

func newCascadeHandlersFixture(t *testing.T, state store.Store) cascadeHandlersFixture {
	t.Helper()
	secrets, err := vault.New(state, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"plugin-cascade-private", "plugin-cascade-shared", "user-managed-key"} {
		if err := secrets.Set(t.Context(), id, id, "fixture secret"); err != nil {
			t.Fatal(err)
		}
	}
	plugin := plugindomain.Plugin{
		ID: "cascade", Enabled: true, Directory: "/fixture/immutable-plugin",
		Manifest: plugindomain.Manifest{ID: "cascade", Name: "Cascade fixture", Version: "1.0.0"},
		Config:   map[string]any{"summaryConfigId": "cascade-model"},
		Secrets:  map[string]string{"private": "plugin-cascade-private", "shared": "plugin-cascade-shared", "external": "user-managed-key"},
	}
	for _, record := range []struct {
		kind, id string
		value    any
	}{
		{"config", "cascade-model", agent.Config{ID: "cascade-model", Name: "cascade model"}},
		{"config", "unrelated-model", agent.Config{ID: "unrelated-model", CredentialID: "plugin-cascade-shared"}},
		{"plugin", plugin.ID, plugin},
		{"plugin-data:" + plugin.ID, "cursor", map[string]any{"lastUID": 17}},
	} {
		if err := state.Put(t.Context(), record.kind, record.id, record.value); err != nil {
			t.Fatal(err)
		}
	}
	version, err := pluginbiz.Pin(t.Context(), state, plugin)
	if err != nil {
		t.Fatal(err)
	}
	scheduler, runtime := &cascadeHandlersScheduler{}, &cascadeHandlersRuntime{}
	tasks := &taskbiz.Commands{Store: state, Scheduler: scheduler}
	plugins := pluginbiz.NewWithRuntime(state, secrets, nil, runtime)
	plugins.PauseDependent = tasks.PauseDependent
	service := &lifecyclebiz.Service{Store: state, Handlers: map[lifecycle.Resource]lifecyclebiz.Handler{
		lifecycle.ResourceTask: tasks, lifecycle.ResourcePlugin: plugins,
	}}
	return cascadeHandlersFixture{service: service, plugins: plugins, scheduler: scheduler, runtime: runtime, vault: secrets, plugin: plugin, version: version}
}

func TestCascadePurgeUsesRealPluginAndTaskLifecycleHandlers(t *testing.T) {
	t.Parallel()
	state := store.NewMemory()
	f := newCascadeHandlersFixture(t, state)
	task := taskentity.Task{
		ID: "cascade-task", Name: "Plugin dependent schedule", Kind: "recurring", Status: "active", Revision: 2,
		Versions: map[string]string{f.plugin.ID: f.version},
		Steps:    []taskentity.Step{{ID: "inspect", Kind: "tool", Tool: f.plugin.ID + "__inspect"}},
	}
	execution := taskentity.TaskExecution{ID: "cascade-execution", TaskID: task.ID, Status: "completed", Versions: task.Versions, Results: map[string]any{"inspect": "private analysis"}}
	notification := messaging.Notification{ID: "notification-task-notify:" + execution.ID, TaskID: task.ID, PluginID: f.plugin.ID, OperationID: "task-notify:" + execution.ID, Status: "sent", Text: "private notification"}
	for _, record := range []struct {
		kind, id string
		value    any
	}{
		{"task", task.ID, task},
		{"execution", execution.ID, execution},
		{"execution-snapshot", execution.ID, taskentity.Snapshot{Task: task}},
		{"step-result", execution.ID + ":inspect", map[string]any{"text": "private step result"}},
		{"notification", notification.ID, notification},
		{"delivery", notification.OperationID, messaging.Delivery{ID: notification.OperationID, Status: "sent"}},
	} {
		if err := state.Put(t.Context(), record.kind, record.id, record.value); err != nil {
			t.Fatal(err)
		}
	}
	archived, err := f.service.Batch(t.Context(), lifecyclebiz.Request{Resource: lifecycle.ResourceConfig, Action: lifecycle.ActionArchive, IDs: []string{"cascade-model"}})
	if err != nil || len(archived.Failed) != 0 {
		t.Fatalf("archive model: %+v, %v", archived, err)
	}
	request := lifecyclebiz.PurgeRequest{Items: []lifecyclebiz.PurgeTarget{{Resource: lifecycle.ResourceConfig, ID: "cascade-model"}}}
	plan, err := f.service.PreviewPurge(t.Context(), request)
	if err != nil || len(plan.Blockers) != 0 {
		t.Fatalf("preview chain: %+v, %v", plan, err)
	}
	planned := make(map[lifecyclebiz.PurgeTarget]bool)
	for _, item := range plan.Items {
		planned[lifecyclebiz.PurgeTarget{Resource: item.Resource, ID: item.ID}] = true
	}
	for _, expected := range []lifecyclebiz.PurgeTarget{
		{Resource: lifecycle.ResourceConfig, ID: "cascade-model"},
		{Resource: lifecycle.ResourcePlugin, ID: f.plugin.ID},
		{Resource: lifecycle.ResourceTask, ID: task.ID},
		{Resource: lifecycle.ResourceExecution, ID: execution.ID},
		{Resource: lifecycle.ResourceNotification, ID: notification.ID},
		{Resource: lifecycle.ResourceDelivery, ID: notification.OperationID},
		{Resource: lifecycle.ResourceSecret, ID: "plugin-cascade-private"},
	} {
		if !planned[expected] {
			t.Fatalf("dependent record missing from confirmation: %+v; plan=%+v", expected, plan.Items)
		}
	}
	for _, retained := range []lifecyclebiz.PurgeTarget{
		{Resource: lifecycle.ResourceSecret, ID: "plugin-cascade-shared"},
		{Resource: lifecycle.ResourceSecret, ID: "user-managed-key"},
		{Resource: lifecycle.ResourceConfig, ID: "unrelated-model"},
	} {
		if planned[retained] {
			t.Fatalf("shared or user-managed record incorrectly added to cascade: %+v", retained)
		}
	}
	if len(f.scheduler.cancelled) != 0 || len(f.runtime.closed) != 0 {
		t.Fatal("preview mutated scheduler or plugin runtime")
	}
	request.Token = plan.Token
	result, err := f.service.ConfirmPurge(t.Context(), request)
	if err != nil || len(result.Failed) != 0 || len(result.Deleted) != len(plan.Items) {
		t.Fatalf("real-handler cascade did not complete: %+v, %v", result, err)
	}
	if !reflect.DeepEqual(f.scheduler.cancelled, []string{task.ID, task.ID}) || f.scheduler.applied != 0 || f.scheduler.triggered != 0 {
		t.Fatalf("task archive/purge did not cancel its schedule safely: %+v", f.scheduler)
	}
	if !slices.Contains(f.runtime.closed, f.version) {
		t.Fatalf("plugin pinned process was not closed: %+v", f.runtime.closed)
	}
	for target := range planned {
		var value any
		if err := state.Get(t.Context(), target.Resource.StorageKind(), target.ID, &value); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("planned record retained after success: %+v, %v", target, err)
		}
	}
	for _, ref := range []store.RecordRef{
		{Kind: "execution-snapshot", ID: execution.ID}, {Kind: "step-result", ID: execution.ID + ":inspect"},
		{Kind: "plugin-version", ID: f.version}, {Kind: "plugin-data:" + f.plugin.ID, ID: "cursor"},
		{Kind: "schedule-intent", ID: task.ID},
	} {
		var value any
		if err := state.Get(t.Context(), ref.Kind, ref.ID, &value); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("owned snapshot or private data retained: %+v, %v", ref, err)
		}
	}
	for _, secret := range []string{"plugin-cascade-shared", "user-managed-key"} {
		if value, err := f.vault.Get(t.Context(), secret); err != nil || value != "fixture secret" {
			t.Fatalf("shared secret removed: %s, %v", secret, err)
		}
	}
	var unrelated agent.Config
	if err := state.Get(t.Context(), "config", "unrelated-model", &unrelated); err != nil {
		t.Fatalf("unrelated shared-key owner deleted: %v", err)
	}
	if archives, err := f.service.Archives(t.Context()); err != nil || len(archives) != 0 {
		t.Fatalf("successful cascade retained archive indexes: %+v, %v", archives, err)
	}
}

type cascadeHandlersStore struct {
	*store.Memory
	beforePluginPurge func(context.Context) error
}

func (s *cascadeHandlersStore) Lock(ctx context.Context, key string) (func(), error) {
	unlock, err := s.Memory.Lock(ctx, key)
	if err != nil {
		return nil, err
	}
	if key == "plugin-calls:cascade" && s.beforePluginPurge != nil {
		hook := s.beforePluginPurge
		s.beforePluginPurge = nil
		if err := hook(ctx); err != nil {
			unlock()
			return nil, err
		}
	}
	return unlock, nil
}

func TestCascadePluginPurgeRejectsPrivateSecretAbsentFromConfirmation(t *testing.T) {
	t.Parallel()
	state := &cascadeHandlersStore{Memory: store.NewMemory()}
	f := newCascadeHandlersFixture(t, state)
	if err := f.plugins.Archive(t.Context(), f.plugin.ID); err != nil {
		t.Fatal(err)
	}
	request := lifecyclebiz.PurgeRequest{Items: []lifecyclebiz.PurgeTarget{{Resource: lifecycle.ResourcePlugin, ID: f.plugin.ID}}}
	plan, err := f.service.PreviewPurge(t.Context(), request)
	if err != nil || len(plan.Blockers) != 0 {
		t.Fatalf("preview plugin: %+v, %v", plan, err)
	}
	const lateSecret = "plugin-cascade-new-private"
	for _, item := range plan.Items {
		if item.ID == lateSecret {
			t.Fatal("late secret existed in original preview")
		}
	}
	// Inject a newly discovered owned credential only after both the original
	// token validation and the per-item preview. The real Manager.Purge must
	// enforce the approved scope when it computes its final deletion set.
	state.beforePluginPurge = func(ctx context.Context) error {
		if err := f.vault.Set(ctx, lateSecret, "new private key", "preserve me"); err != nil {
			return err
		}
		var current plugindomain.Plugin
		if err := state.Get(ctx, "plugin", f.plugin.ID, &current); err != nil {
			return err
		}
		current.Secrets["late"] = lateSecret
		return state.Put(ctx, "plugin", current.ID, current)
	}
	request.Token = plan.Token
	result, err := f.service.ConfirmPurge(t.Context(), request)
	if err != nil || len(result.Deleted) != 0 || len(result.Failed) == 0 || !strings.Contains(result.Failed[0].Reason, lifecycleRepo.ErrPurgeScopeChanged.Error()) {
		t.Fatalf("new private credential bypassed confirmation: %+v, %v", result, err)
	}
	if value, err := f.vault.Get(t.Context(), lateSecret); err != nil || value != "preserve me" {
		t.Fatalf("unapproved credential was destroyed: %q, %v", value, err)
	}
	for _, ref := range []store.RecordRef{{Kind: "plugin", ID: f.plugin.ID}, {Kind: "plugin-version", ID: f.version}, {Kind: "plugin-data:" + f.plugin.ID, ID: "cursor"}, {Kind: "secret", ID: "plugin-cascade-private"}} {
		var value any
		if err := state.Get(t.Context(), ref.Kind, ref.ID, &value); err != nil {
			t.Fatalf("scope failure partially deleted plugin payload: %+v, %v", ref, err)
		}
	}
	if len(f.runtime.closed) != 0 {
		t.Fatal("scope failure closed pinned plugin runtime")
	}
}
