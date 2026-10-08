package plugin

import (
	"strings"
	"testing"

	"github.com/xingexin/catbot/internal/domain/agent"
)

func TestNormalizeManifestEnforcesToolContract(t *testing.T) {
	base := Manifest{ID: "example", Name: "Example", Version: "1.0.0", Entry: "index.cjs", Tools: []agent.Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}}
	normalized, err := NormalizeManifest(base)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Tools[0].PluginID != "example" || normalized.Tools[0].Version != "1.0.0" || normalized.Tools[0].TimeoutSec != 60 {
		t.Fatalf("defaults missing: %+v", normalized.Tools[0])
	}
	if base.Tools[0].TimeoutSec != 0 {
		t.Fatal("normalization mutated the caller's manifest")
	}
	if normalized.ConfigSchema["type"] != "object" {
		t.Fatal("missing default config schema")
	}
	for _, test := range []struct {
		name   string
		change func(*Manifest)
	}{
		{"invalid ID", func(m *Manifest) { m.ID = "../outside" }},
		{"invalid version", func(m *Manifest) { m.Version = "../outside" }},
		{"missing entry", func(m *Manifest) { m.Entry = "" }},
		{"duplicate tool", func(m *Manifest) { m.Tools = append(m.Tools, m.Tools[0]) }},
		{"long public name", func(m *Manifest) { m.ID = strings.Repeat("a", 48); m.Tools[0].Name = strings.Repeat("b", 16) }},
		{"long timeout", func(m *Manifest) { m.Tools[0].TimeoutSec = 3601 }},
		{"invalid schema", func(m *Manifest) { m.Tools[0].InputSchema = map[string]any{"type": "string"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := base
			value.Tools = append([]agent.Tool(nil), base.Tools...)
			test.change(&value)
			if _, err := NormalizeManifest(value); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestRulesPreserveSchemaPermissionsAndSnapshotIdentity(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []string{"name"}, "properties": map[string]any{"name": map[string]any{"type": "string"}}}
	if err := Validate(schema, map[string]any{"name": "catbot"}); err != nil {
		t.Fatal(err)
	}
	if err := Validate(schema, map[string]any{"name": 3}); err == nil {
		t.Fatal("invalid input accepted")
	}
	if !Granted([]string{"read", "write"}, []string{"write"}) || Granted([]string{"read"}, []string{"write"}) || !Granted(nil, nil) {
		t.Fatal("permission containment changed")
	}
	p := Plugin{ID: "example", Manifest: Manifest{ID: "example", Name: "Example", Version: "1.0.0", Entry: "index.cjs"}, Directory: "/bundle", Enabled: true, Config: map[string]any{"name": "catbot"}, Secrets: map[string]string{"token": "secret-1"}, Grants: []string{"read"}}
	const expected = "example@1.0.0-95b3ae50c289cb2f"
	if key := SnapshotKey(p); key != expected {
		t.Fatalf("snapshot identity changed: %s", key)
	}
	p.Grants = append(p.Grants, "write")
	if SnapshotKey(p) == expected {
		t.Fatal("permission changes did not create a new snapshot")
	}
}
