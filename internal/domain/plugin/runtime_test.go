package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManifestRuntimeKeepsLegacySnapshotsAndSelectsExplicitStrategy(t *testing.T) {
	base := Manifest{ID: "fixture", Name: "Fixture", Version: "1", Entry: "index.cjs"}
	for _, runtime := range []string{"", "node", "binary"} {
		t.Run("runtime="+runtime, func(t *testing.T) {
			manifest := base
			manifest.Runtime = runtime
			got, err := NormalizeManifest(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if got.Runtime != runtime {
				t.Fatalf("normalization changed persisted runtime from %q to %q", runtime, got.Runtime)
			}
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if runtime == "" && strings.Contains(string(data), `"runtime"`) {
				t.Fatalf("legacy manifest gained a runtime field: %s", data)
			}
		})
	}
	base.Runtime = "shell"
	if _, err := NormalizeManifest(base); err == nil || !strings.Contains(err.Error(), "unsupported plugin runtime") {
		t.Fatalf("unsupported runtime accepted: %v", err)
	}
	base.Runtime = "node"
	p := Plugin{ID: base.ID, Manifest: base}
	node := SnapshotKey(p)
	p.Manifest.Runtime = "binary"
	if node == SnapshotKey(p) {
		t.Fatal("changing launch strategy did not change snapshot identity")
	}
}
