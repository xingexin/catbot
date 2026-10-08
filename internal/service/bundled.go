package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"agentTest/internal/domain"
	"agentTest/internal/store"
)

func newerBundle(next, previous string) bool {
	a, b := strings.Split(next, "."), strings.Split(previous, ".")
	if len(a) != 3 || len(b) != 3 {
		return false
	}
	for i := range a {
		av, ea := strconv.Atoi(a[i])
		bv, eb := strconv.Atoi(b[i])
		if ea != nil || eb != nil {
			return false
		}
		if av != bv {
			return av > bv
		}
	}
	return false
}

// Upgrade shipped packages when their declared version increases. Register
// preserves configured credentials and grants; active runs retain frozen files.
func (a *App) registerBundled(ctx context.Context) error {
	for _, dir := range []string{"example", "mail", "video"} {
		var old domain.Plugin
		err := a.Store.Get(ctx, "plugin", dir, &old)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		missing := errors.Is(err, store.ErrNotFound)
		bytes, err := os.ReadFile(filepath.Join(a.Options.PluginDir, dir, "plugin.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var manifest domain.Manifest
		if err := json.Unmarshal(bytes, &manifest); err != nil {
			return err
		}
		if !missing && !newerBundle(manifest.Version, old.Manifest.Version) {
			continue
		}
		p, err := a.Plugins.Register(ctx, dir)
		if err != nil {
			return fmt.Errorf("register bundled %s: %w", dir, err)
		}
		if missing && dir == "example" {
			p.Enabled = true
			if err := a.Store.Put(ctx, "plugin", p.ID, p); err != nil {
				return err
			}
		}
	}
	return nil
}
