package bootstrap

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestProductionLayerImports checks actual imports, including files behind build
// tags. Test fixtures are intentionally excluded from production dependencies.
func TestProductionLayerImports(t *testing.T) {
	root, module := architectureModule(t)
	internal := filepath.Join(root, "internal")
	files := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir(internal, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(internal, filepath.Dir(path))
		if err != nil {
			return err
		}
		directory := filepath.ToSlash(relative)
		parts := strings.Split(directory, "/")
		layer := parts[0]
		if layer != "domain" && layer != "infra" && layer != "biz" {
			return nil
		}
		file, err := parser.ParseFile(files, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		checked++
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			reason := productionImportViolation(directory, importPath, module)
			if reason != "" {
				position := files.Position(spec.Pos())
				t.Errorf("internal/%s/%s:%d imports %q: %s", directory, entry.Name(), position.Line, importPath, reason)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("architecture test did not find production packages")
	}
}

func productionImportViolation(directory, importPath, module string) string {
	parts := strings.Split(directory, "/")
	layer := parts[0]
	prefix := module + "/internal/"
	dependsOn := func(other string) bool {
		return importPath == prefix+other || strings.HasPrefix(importPath, prefix+other+"/")
	}
	switch layer {
	case "domain":
		for _, forbidden := range []string{"biz", "transport", "worker", "bootstrap"} {
			if dependsOn(forbidden) {
				return "domain rules must not depend on application, transport or assembly packages"
			}
		}
		if dependsOn("infra") {
			// The selected tokenhub layout keeps typed repositories under each
			// domain. Only those repositories may use the generic store adapter.
			isRepository := len(parts) >= 3 && parts[2] == "repository"
			if !isRepository || importPath != prefix+"infra/store" {
				return "only domain/*/repository may import infra/store; domain rules are infrastructure independent"
			}
		}
	case "infra":
		for _, forbidden := range []string{"biz", "transport", "worker", "bootstrap"} {
			if dependsOn(forbidden) {
				return "infrastructure must not depend on application, transport or assembly packages"
			}
		}
		if len(parts) >= 2 && parts[1] == "store" && dependsOn("domain") {
			return "the generic store must not depend on domain types"
		}
	case "biz":
		for _, forbidden := range []string{"transport", "worker", "bootstrap"} {
			if dependsOn(forbidden) {
				return "application use cases must not depend on transport, worker or assembly packages"
			}
		}
		if importPath == "go.temporal.io/sdk" || strings.HasPrefix(importPath, "go.temporal.io/sdk/") {
			return "Temporal SDK calls belong to infrastructure or workers, not use cases"
		}
	}
	return ""
}

func architectureModule(t *testing.T) (string, string) {
	t.Helper()
	current, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		raw, err := os.ReadFile(filepath.Join(current, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[0] == "module" {
					return current, fields[1]
				}
			}
			t.Fatal("go.mod has no module directive")
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatal("cannot locate module root for architecture test")
		}
		current = parent
	}
}
