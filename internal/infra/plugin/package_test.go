package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xingexin/catbot/internal/infra/store"
)

func TestPackagesFreezeContentAndRequireVersionBump(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "fixture")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"fixture","name":"Fixture","version":"1.0.0","entry":"index.cjs"}`
	for name, value := range map[string]string{"plugin.json": manifest, "index.cjs": "original"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	packages := NewPackages(store.NewMemory(), root, t.TempDir())
	source, m, err := packages.Read("fixture")
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := packages.Freeze(t.Context(), source, m)
	if err != nil {
		t.Fatal(err)
	}
	again, err := packages.Freeze(t.Context(), source, m)
	if err != nil || again != frozen {
		t.Fatalf("bundle not reused: %s %v", again, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.cjs"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := packages.Freeze(t.Context(), source, m); err == nil || !strings.Contains(err.Error(), "version bump") {
		t.Fatalf("changed version accepted: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(frozen, "index.cjs"))
	if err != nil || string(content) != "original" {
		t.Fatalf("pinned bundle modified: %q %v", content, err)
	}
	m.Version = "1.0.1"
	newer, err := packages.Freeze(t.Context(), source, m)
	if err != nil || newer == frozen {
		t.Fatalf("version bump failed: %s %v", newer, err)
	}
}

func TestPackagesRejectRootEscapeAndSymlinkDependencies(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	packages := NewPackages(store.NewMemory(), root, t.TempDir())
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := packages.Read("escape"); err == nil || !strings.Contains(err.Error(), "inside PLUGIN_DIR") {
		t.Fatalf("package escaped root: %v", err)
	}
	dir := filepath.Join(root, "fixture")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"plugin.json": `{"id":"fixture","name":"Fixture","version":"1","entry":"index.cjs"}`, "index.cjs": "fixture"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "dependency")); err != nil {
		t.Fatal(err)
	}
	source, m, err := packages.Read("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := packages.Freeze(t.Context(), source, m); err == nil || !strings.Contains(err.Error(), "symlinks") {
		t.Fatalf("symlink bundle accepted: %v", err)
	}
}
