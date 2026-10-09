package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)
var versionIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

func ValidateManifestIdentity(manifest Manifest) error {
	if !identifier.MatchString(manifest.ID) || !versionIdentifier.MatchString(manifest.Version) || manifest.Name == "" || manifest.Entry == "" {
		return errors.New("invalid plugin manifest")
	}
	switch manifest.Runtime {
	case "", "node", "binary":
		return nil
	default:
		return fmt.Errorf("unsupported plugin runtime %q: use node or binary", manifest.Runtime)
	}
}

// NormalizeManifest validates public tool names and fills execution defaults.
func NormalizeManifest(manifest Manifest) (Manifest, error) {
	if err := ValidateManifestIdentity(manifest); err != nil {
		return manifest, err
	}
	manifest.Tools = slices.Clone(manifest.Tools)
	seen := map[string]bool{}
	for i := range manifest.Tools {
		tool := &manifest.Tools[i]
		if !identifier.MatchString(tool.Name) || seen[tool.Name] || len(manifest.ID+"__"+tool.Name) > 64 {
			return manifest, errors.New("invalid or duplicate tool name")
		}
		seen[tool.Name] = true
		tool.PluginID = manifest.ID
		tool.Version = manifest.Version
		if tool.TimeoutSec <= 0 {
			tool.TimeoutSec = 60
		}
		if tool.TimeoutSec > 3600 {
			return manifest, errors.New("tool timeout exceeds 3600 seconds")
		}
		if len(tool.InputSchema) == 0 || tool.InputSchema["type"] != "object" {
			return manifest, errors.New("tool inputSchema is required")
		}
	}
	if manifest.ConfigSchema == nil {
		manifest.ConfigSchema = map[string]any{"type": "object"}
	}
	return manifest, nil
}

func Validate(schema map[string]any, value any) error {
	b, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	var doc any
	if err = json.Unmarshal(b, &doc); err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	if err = c.AddResource("urn:secretary:input", doc); err != nil {
		return err
	}
	s, err := c.Compile("urn:secretary:input")
	if err != nil {
		return fmt.Errorf("invalid JSON schema: %w", err)
	}
	b, err = json.Marshal(value)
	if err != nil {
		return err
	}
	var input any
	if err = json.Unmarshal(b, &input); err != nil {
		return err
	}
	if err = s.Validate(input); err != nil {
		return fmt.Errorf("input validation failed: %w", err)
	}
	return nil
}

func SnapshotKey(p Plugin) string {
	b, _ := json.Marshal(p)
	hash := sha256.Sum256(b)
	return p.ID + "@" + p.Manifest.Version + "-" + hex.EncodeToString(hash[:8])
}

func Granted(grants, permissions []string) bool {
	for _, p := range permissions {
		if !slices.Contains(grants, p) {
			return false
		}
	}
	return true
}
