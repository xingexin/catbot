package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/store"
)

func binaryFixture(t *testing.T) (string, domainplugin.Manifest) {
	t.Helper()
	dir := t.TempDir()
	manifest := domainplugin.Manifest{ID: "native", Name: "Native", Version: "1.0.0", Runtime: "binary", Entry: "dist/plugin"}
	if err := os.Mkdir(filepath.Join(dir, "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"plugin.json": data, "dist/plugin": []byte("invalid native executable"), "data.txt": []byte("data")} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, manifest
}

func TestBinaryPackageFreezingSetsOnlyEntryExecutableAndRepairsReuse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable permission check")
	}
	dir, manifest := binaryFixture(t)
	packages := NewPackages(store.NewMemory(), filepath.Dir(dir), t.TempDir())
	frozen, err := packages.Freeze(t.Context(), dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{manifest.Entry: 0700, "data.txt": 0600, "plugin.json": 0600} {
		info, err := os.Stat(filepath.Join(frozen, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode: got %o, want %o", name, info.Mode().Perm(), want)
		}
	}
	if err := os.Chmod(filepath.Join(frozen, manifest.Entry), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, manifest.Entry), 0755); err != nil {
		t.Fatal(err)
	}
	again, err := packages.Freeze(t.Context(), dir, manifest)
	if err != nil || again != frozen {
		t.Fatalf("mode-only change altered frozen identity: %q %v", again, err)
	}
	info, err := os.Stat(filepath.Join(again, manifest.Entry))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("reuse did not restore executable mode: %v %v", info, err)
	}
}

func TestLegacyPackageDigestRemainsContentOnly(t *testing.T) {
	dir := t.TempDir()
	manifestJSON := `{"id":"fixture","name":"Fixture","version":"1","entry":"index.cjs"}`
	for name, content := range map[string]string{"index.cjs": "original", "plugin.json": manifestJSON} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	packages := NewPackages(store.NewMemory(), filepath.Dir(dir), t.TempDir())
	source, manifest, err := packages.Read(filepath.Base(dir))
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := packages.Freeze(t.Context(), source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	// This is the pre-runtime package digest: sorted relative names and bytes,
	// separated by NUL, with no file mode or normalized manifest data added.
	digest := sha256.Sum256([]byte("index.cjs\x00original\x00plugin.json\x00" + manifestJSON + "\x00"))
	want := "fixture@1-" + hex.EncodeToString(digest[:])[:16]
	if filepath.Base(frozen) != want {
		t.Fatalf("legacy bundle identity changed: %s, want %s", frozen, want)
	}
}

func TestPackagesRejectEntriesThatCannotBeFrozen(t *testing.T) {
	for _, test := range []struct {
		name, entry, want string
	}{
		{"outside", "../outside", "inside plugin package"},
		{"absolute", "/tmp/plugin", "inside plugin package"},
		{"directory", "dist", "regular file"},
		{"root directory", ".", "inside plugin package"},
		{"hidden file", ".plugin", "hidden path"},
		{"hidden parent", ".bin/plugin", "hidden path"},
		{"node_modules", "node_modules/plugin", "node_modules"},
		{"missing file", "dist/missing", "read plugin entry"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, manifest := binaryFixture(t)
			manifest.Entry = test.entry
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			packages := NewPackages(store.NewMemory(), filepath.Dir(dir), t.TempDir())
			if _, _, err := packages.Read(filepath.Base(dir)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Read accepted unusable entry or returned unclear error: %v", err)
			}
			if _, err := packages.Freeze(t.Context(), dir, manifest); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Freeze accepted unusable entry or returned unclear error: %v", err)
			}
		})
	}
}

func TestPackagesRejectEntrySymlinkAndUnknownRuntime(t *testing.T) {
	dir, manifest := binaryFixture(t)
	if err := os.Symlink(filepath.Join(dir, manifest.Entry), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := packageEntry(dir, "link"); err == nil || !strings.Contains(err.Error(), "symlinks") {
		t.Fatalf("symlink entry accepted: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "dist"), filepath.Join(dir, "linked-parent")); err != nil {
		t.Fatal(err)
	}
	if _, err := packageEntry(dir, "linked-parent/plugin"); err == nil || !strings.Contains(err.Error(), "symlinks") {
		t.Fatalf("symlink entry parent accepted: %v", err)
	}
	manifest.Runtime = "sh"
	packages := NewPackages(store.NewMemory(), filepath.Dir(dir), t.TempDir())
	if _, err := packages.Freeze(t.Context(), dir, manifest); err == nil || !strings.Contains(err.Error(), "unsupported plugin runtime") {
		t.Fatalf("unknown runtime accepted: %v", err)
	}
}

func TestPluginCommandUsesExplicitRuntimeWithoutShell(t *testing.T) {
	dir, manifest := binaryFixture(t)
	for _, kind := range []string{"", "node", "binary"} {
		manifest.Runtime = kind
		p := domainplugin.Plugin{ID: manifest.ID, Manifest: manifest, Directory: dir}
		cmd, err := pluginCommand(p)
		if err != nil {
			t.Fatal(err)
		}
		entry := filepath.Join(dir, manifest.Entry)
		if kind == "binary" {
			if cmd.Path != entry || len(cmd.Args) != 1 || cmd.Args[0] != entry {
				t.Fatalf("binary did not run directly: %q %v", cmd.Path, cmd.Args)
			}
		} else if len(cmd.Args) != 2 || cmd.Args[0] != "node" || cmd.Args[1] != entry {
			t.Fatalf("legacy Node command changed: %v", cmd.Args)
		}
	}
}

func TestInvalidBinaryReportsHostPlatformAndDoesNotStayRunning(t *testing.T) {
	dir, manifest := binaryFixture(t)
	packages := NewPackages(store.NewMemory(), filepath.Dir(dir), t.TempDir())
	frozen, err := packages.Freeze(t.Context(), dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(t.TempDir(), "http://127.0.0.1", func(string) string { return "fixture-token" })
	t.Cleanup(r.Close)
	p := domainplugin.Plugin{ID: manifest.ID, Manifest: manifest, Directory: frozen}
	err = r.Health(t.Context(), "fixture", p, func(context.Context) (map[string]any, error) { return map[string]any{}, nil })
	for _, want := range []string{"start binary plugin native", runtime.GOOS + "/" + runtime.GOARCH, "GOOS=" + runtime.GOOS, "GOARCH=" + runtime.GOARCH} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("binary launch error missing %q: %v", want, err)
		}
	}
	if len(r.processes) != 0 {
		t.Fatal("failed binary left a cached process")
	}
}
