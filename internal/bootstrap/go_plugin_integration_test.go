//go:build integration

package bootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	pluginbiz "github.com/xingexin/catbot/internal/biz/plugin"
	"github.com/xingexin/catbot/internal/config"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	plugindomain "github.com/xingexin/catbot/internal/domain/plugin"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

func TestGoAndNodePluginsUseSameHost(t *testing.T) {
	root, _ := architectureModule(t)
	dir := t.TempDir()
	pluginRoot := filepath.Join(dir, "plugins")
	fixture := filepath.Join(pluginRoot, "go-fixture")
	if err := os.MkdirAll(fixture, 0700); err != nil {
		t.Fatal(err)
	}
	buildPluginBinary(t, root, "./internal/bootstrap/testdata/go-plugin", filepath.Join(fixture, "plugin"))
	object := map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}
	manifest := plugindomain.Manifest{ID: "go-fixture", Name: "Go integration fixture", Version: "1.0.0", Runtime: "binary", Entry: "plugin", ConfigSchema: map[string]any{"type": "object"}, Tools: []agent.Tool{
		{Name: "echo", InputSchema: object, OutputSchema: object, RetrySafe: true},
		{Name: "exercise", InputSchema: object, Permissions: []string{"storage", "tasks", "notifications"}},
	}}
	writePluginManifest(t, fixture, manifest)
	// Keep both real language implementations in the same host and registry.
	for _, name := range []string{"example", "example-go"} {
		dest := filepath.Join(pluginRoot, name)
		if err := os.MkdirAll(dest, 0700); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(root, "plugins", name, "plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dest, "plugin.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Join(dest, "dist"), 0700); err != nil {
			t.Fatal(err)
		}
		if name == "example-go" {
			buildPluginBinary(t, root, "./plugins/example-go", filepath.Join(dest, "dist", "plugin"))
		} else {
			data, err = os.ReadFile(filepath.Join(root, "plugins", name, "dist", "index.cjs"))
			if err != nil {
				t.Fatalf("build TypeScript example first: %v", err)
			}
			if err = os.WriteFile(filepath.Join(dest, "dist", "index.cjs"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	httpServer := httptest.NewUnstartedServer(nil)
	a, err := New(store.NewMemory(), config.Options{DataDir: filepath.Join(dir, "data"), PluginDir: pluginRoot, InternalURL: "http://" + httpServer.Listener.Addr().String(), MasterKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), AdminPassword: "test", RuntimeToken: "test"})
	if err != nil {
		httpServer.Close()
		t.Fatal(err)
	}
	httpServer.Config.Handler = a.Handler()
	httpServer.Start()
	t.Cleanup(func() {
		a.Close()
		httpServer.Close()
	})
	if err = a.Bootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	scheduler := &fakeScheduler{}
	a.Tasks.Scheduler = scheduler
	if err = a.Store.Put(t.Context(), "config", "config", agent.Config{ID: "config", Kind: "api", Capabilities: agent.Capabilities{Tools: true}}); err != nil {
		t.Fatal(err)
	}
	if err = a.Store.Put(t.Context(), "session", "session", conversation.Session{ID: "session", Channel: "web", PersonaID: "secretary", ConfigID: "config"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"example", "example-go", "go-fixture"} {
		if _, err = a.Plugins.Register(t.Context(), name); err != nil {
			t.Fatal(err)
		}
		if _, err = a.Plugins.Configure(t.Context(), name, map[string]any{}, []string{"storage", "tasks", "notifications"}); err != nil {
			t.Fatal(err)
		}
		if err = a.Plugins.SetPluginEnabled(t.Context(), name, true); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := a.Plugins.Snapshots(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"example", "example-go"} {
		t.Run(name, func(t *testing.T) {
			value, err := a.Plugins.CallPinned(t.Context(), versions[name], "echo", map[string]any{"text": "你好 Go 与 TS"}, name+":echo")
			if err != nil {
				t.Fatal(err)
			}
			if value.(map[string]any)["text"] != "你好 Go 与 TS" {
				t.Fatal(value)
			}
			value, err = a.Plugins.CallPinned(t.Context(), versions[name], "note", map[string]any{"name": "shared-interface", "text": "stored"}, name+":note")
			if err != nil {
				t.Fatal(err)
			}
			var saved any
			if err = a.Store.Get(t.Context(), "plugin-data:"+name, "note-shared-interface", &saved); err != nil {
				t.Fatalf("host KV not used: %v; result=%v", err, value)
			}
		})
	}
	args := map[string]any{"text": "native host integration"}
	value, err := a.Plugins.CallPinned(t.Context(), versions["go-fixture"], "exercise", args, "go:exercise")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := a.Plugins.CallPinned(t.Context(), versions["go-fixture"], "exercise", args, "go:exercise")
	if err != nil || !reflect.DeepEqual(value, repeated) {
		t.Fatal("cached operation changed", err, value, repeated)
	}
	for _, kind := range []string{"task", "notification", "artifact"} {
		records, err := a.Store.List(t.Context(), kind)
		if err != nil || len(records) != 1 {
			t.Fatalf("%s not persisted once: count=%d err=%v", kind, len(records), err)
		}
	}
	scheduler.mu.Lock()
	applied := len(scheduler.applied)
	scheduler.mu.Unlock()
	if applied != 1 {
		t.Fatalf("task scheduled %d times", applied)
	}
	var stored string
	if err = a.Store.Get(t.Context(), "plugin-data:go-fixture", "integration-note", &stored); err != nil || stored != args["text"] {
		t.Fatal(stored, err)
	}
	// Same runtime must still enforce grants before a native tool can run.
	p, err := a.Plugins.Configure(t.Context(), "go-fixture", map[string]any{}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	key, err := pluginbiz.Pin(t.Context(), a.Store, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Plugins.CallPinned(t.Context(), key, "exercise", args, "go:denied"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatal("native runtime bypassed permissions", err)
	}
	if err = a.Plugins.SetPluginEnabled(t.Context(), "go-fixture", false); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.All[taskentity.Task](t.Context(), a.Store, "task")
	if err != nil || len(tasks) != 1 || !tasks[0].Paused {
		t.Fatal("dependent task was not paused", err, tasks)
	}
	latest, err := a.Plugins.Snapshots(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := latest["go-fixture"]; exists {
		t.Fatal("disabled native plugin exposed to new runs")
	}
}

func buildPluginBinary(t *testing.T, root, pkg, output string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", output, pkg)
	cmd.Dir = root
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, data)
	}
}
func writePluginManifest(t *testing.T, dir string, manifest plugindomain.Manifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
