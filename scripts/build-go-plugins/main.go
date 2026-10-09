// Command build-go-plugins compiles Go plugin packages without linking them into catbot.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

type manifest struct {
	ID      string `json:"id"`
	Runtime string `json:"runtime"`
	Entry   string `json:"entry"`
}

var pluginID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,30}$`)

func main() {
	goos := flag.String("os", runtime.GOOS, "plugin target operating system")
	goarch := flag.String("arch", runtime.GOARCH, "plugin target architecture")
	flag.Parse()
	if *goos == "" {
		*goos = runtime.GOOS
	}
	if *goarch == "" {
		*goarch = runtime.GOARCH
	}
	if err := build(flag.Args(), *goos, *goarch); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build(ids []string, goos, goarch string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return errors.New("run the builder from the catbot repository root")
	}
	explicit := len(ids) > 0
	if !explicit {
		entries, err := os.ReadDir(filepath.Join(root, "plugins"))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				ids = append(ids, entry.Name())
			}
		}
	}
	for _, id := range ids {
		if !pluginID.MatchString(id) {
			return fmt.Errorf("invalid plugin ID %q", id)
		}
		dir := filepath.Join(root, "plugins", id)
		data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
		if err != nil {
			return fmt.Errorf("read %s manifest: %w", id, err)
		}
		var config manifest
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("read %s manifest: %w", id, err)
		}
		if config.Runtime != "binary" {
			if explicit {
				return fmt.Errorf("plugin %s does not declare runtime binary", id)
			}
			continue
		}
		if config.ID != id {
			return fmt.Errorf("plugin %s manifest ID does not match its directory", id)
		}
		if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
			if !explicit && errors.Is(err, os.ErrNotExist) {
				continue // Binary-only third-party packages have no local Go build step.
			}
			return fmt.Errorf("find Go entry for %s: %w", id, err)
		}
		output, err := outputPath(dir, config.Entry)
		if err != nil {
			return fmt.Errorf("plugin %s: %w", id, err)
		}
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			return err
		}
		command := exec.Command("go", "build", "-trimpath", "-o", output, "./plugins/"+id)
		command.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("build %s: %w", id, err)
		}
		fmt.Printf("Built %s (%s/%s): plugins/%s/%s\n", id, goos, goarch, id, config.Entry)
	}
	return nil
}

func outputPath(dir, entry string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(entry))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("entry must be a relative file within the plugin directory")
	}
	// Build outputs stay under dist so a manifest cannot overwrite its own source.
	if !strings.HasPrefix(clean, "dist"+string(filepath.Separator)) {
		return "", errors.New("Go build entry must be under dist/")
	}
	return filepath.Join(dir, clean), nil
}
