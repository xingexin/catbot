package plugin

import (
	"encoding/base64"
	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
	"os"
	"path/filepath"
	"testing"
)

func TestRealMCPBundleAndPinnedVersions(t *testing.T) {
	s := store.NewMemory()
	v, err := vault.New(s, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../../plugins")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "example/dist/index.cjs")); err != nil {
		t.Skip("build plugins first with npm run build")
	}
	m := New(s, v, root, t.TempDir(), "http://127.0.0.1:1", func(string) string { return "fixture" })
	defer m.Close()
	p, err := m.Register(t.Context(), "example")
	if err != nil {
		t.Fatal(err)
	}
	p.Enabled = true
	if err := s.Put(t.Context(), "plugin", p.ID, p); err != nil {
		t.Fatal(err)
	}
	versions, err := m.Snapshots(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.CallPinned(t.Context(), versions["example"], "echo", map[string]any{"text": "你好"}, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["text"] != "你好" {
		t.Fatal(result)
	}
	if _, err := m.CallPinned(t.Context(), versions["example"], "echo", map[string]any{"text": 4}, "op-invalid"); err == nil {
		t.Fatal("schema did not validate")
	}
	if _, err := m.CallPinned(t.Context(), versions["example"], "note", map[string]any{"name": "x"}, "op-denied"); err == nil {
		t.Fatal("missing permission accepted")
	}
	p.Enabled = false
	if err := s.Put(t.Context(), "plugin", p.ID, p); err != nil {
		t.Fatal(err)
	}
	newVersions, err := m.Snapshots(t.Context())
	if err != nil || len(newVersions) != 0 {
		t.Fatalf("disabled plugin offered to new runs: %v %v", newVersions, err)
	}
	if _, err := m.CallPinned(t.Context(), versions["example"], "echo", map[string]any{"text": "in-flight"}, "op-2"); err != nil {
		t.Fatal("in-flight snapshot broken:", err)
	}
	var frozen domainplugin.Plugin
	if err := s.Get(t.Context(), "plugin-version", versions["example"], &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.Directory == filepath.Join(root, "example") {
		t.Fatal("package was not frozen")
	}
}
