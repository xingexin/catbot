package plugin

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xingexin/catbot/internal/domain/agent"
	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
)

type fixtureRuntime struct {
	calls atomic.Int64
	call  func(context.Context, domainplugin.Plugin, map[string]any, string, map[string]any, string) (any, error)
}

func (r *fixtureRuntime) Health(ctx context.Context, _ string, _ domainplugin.Plugin, configuration func(context.Context) (map[string]any, error)) error {
	_, err := configuration(ctx)
	return err
}
func (r *fixtureRuntime) Call(ctx context.Context, _ string, p domainplugin.Plugin, configuration func(context.Context) (map[string]any, error), name string, args map[string]any, operationID string) (any, error) {
	r.calls.Add(1)
	cfg, err := configuration(ctx)
	if err != nil {
		return nil, err
	}
	return r.call(ctx, p, cfg, name, args, operationID)
}
func (r *fixtureRuntime) Close() {}

func fixtureManager(t *testing.T, r *fixtureRuntime) (*Manager, domainplugin.Plugin, string) {
	t.Helper()
	s := store.NewMemory()
	v, err := vault.New(s, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(t.Context(), "fixture-secret", "Fixture", "original secret"); err != nil {
		t.Fatal(err)
	}
	p := domainplugin.Plugin{ID: "fixture", Enabled: true, Directory: "/frozen", Manifest: domainplugin.Manifest{ID: "fixture", Name: "Fixture", Version: "1.0.0", ConfigSchema: map[string]any{"type": "object"}, Tools: []agent.Tool{{Name: "echo", TimeoutSec: 5, InputSchema: map[string]any{"type": "object", "required": []string{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}}, Permissions: []string{"read"}}}}, Config: map[string]any{"setting": "original"}, Secrets: map[string]string{"password": "fixture-secret"}, Grants: []string{"read"}}
	if err := s.Put(t.Context(), "plugin", p.ID, p); err != nil {
		t.Fatal(err)
	}
	m := NewWithRuntime(s, v, nil, r)
	key, err := Pin(t.Context(), s, p)
	if err != nil {
		t.Fatal(err)
	}
	return m, p, key
}

func TestInjectedRuntimeReceivesPinnedConfigurationAndDeduplicatesConcurrentCalls(t *testing.T) {
	r := &fixtureRuntime{call: func(ctx context.Context, p domainplugin.Plugin, cfg map[string]any, name string, args map[string]any, operationID string) (any, error) {
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("tool timeout missing")
		}
		if p.Directory != "/frozen" || cfg["setting"] != "original" || cfg["password"] != "original secret" || name != "echo" || operationID != "stable" {
			return nil, errors.New("snapshot or execution metadata changed")
		}
		return map[string]any{"text": args["text"]}, nil
	}}
	m, p, key := fixtureManager(t, r)
	p.Enabled = false
	p.Config = map[string]any{"setting": "changed"}
	p.Grants = nil
	if err := m.Store.Put(t.Context(), "plugin", p.ID, p); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := m.CallPinned(t.Context(), key, "echo", map[string]any{"text": "hello"}, "stable")
			if err == nil && value.(map[string]any)["text"] != "hello" {
				err = errors.New("wrong runtime result")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if r.calls.Load() != 1 {
		t.Fatalf("operation executed %d times", r.calls.Load())
	}
	if _, err := m.CallPinned(t.Context(), key, "echo", map[string]any{"text": "changed"}, "stable"); err == nil || !strings.Contains(err.Error(), "different tool arguments") {
		t.Fatalf("operation ID misuse accepted: %v", err)
	}
	snapshots, err := m.Snapshots(t.Context())
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("disabled plugin in new snapshot: %v %v", snapshots, err)
	}
}

func TestApplicationValidatesToolBoundaryBeforeRuntimeAndPreservesUncertainty(t *testing.T) {
	r := &fixtureRuntime{call: func(context.Context, domainplugin.Plugin, map[string]any, string, map[string]any, string) (any, error) {
		return nil, errors.New("transport failed")
	}}
	m, p, key := fixtureManager(t, r)
	if _, err := m.CallPinned(t.Context(), key, "missing", map[string]any{}, "unknown"); err == nil {
		t.Fatal("unknown tool accepted")
	}
	if _, err := m.CallPinned(t.Context(), key, "echo", map[string]any{"text": 4}, "invalid"); err == nil {
		t.Fatal("invalid input accepted")
	}
	p.Grants = nil
	deniedKey, err := Pin(t.Context(), m.Store, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CallPinned(t.Context(), deniedKey, "echo", map[string]any{"text": "hello"}, "denied"); err == nil {
		t.Fatal("unauthorized tool accepted")
	}
	if r.calls.Load() != 0 {
		t.Fatal("runtime invoked before validation")
	}
	if _, err := m.CallPinned(t.Context(), key, "echo", map[string]any{"text": "hello"}, "unsafe"); err == nil {
		t.Fatal("runtime failure ignored")
	}
	if _, err := m.CallPinned(t.Context(), key, "echo", map[string]any{"text": "hello"}, "unsafe"); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("unsafe retry allowed: %v", err)
	}
	if r.calls.Load() != 1 {
		t.Fatal("uncertain operation retried")
	}
}
