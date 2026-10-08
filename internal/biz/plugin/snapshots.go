package plugin

import (
	"context"
	"github.com/xingexin/catbot/internal/domain/agent"
	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/store"
	"slices"
)

// Pin records the currently configured package and permissions for one execution.
func Pin(ctx context.Context, s store.Store, p domainplugin.Plugin) (string, error) {
	key := domainplugin.SnapshotKey(p)
	return key, s.Put(ctx, "plugin-version", key, p)
}
func (m *Manager) Snapshots(ctx context.Context) (map[string]string, error) {
	ps, err := store.All[domainplugin.Plugin](ctx, m.Store, "plugin")
	if err != nil {
		return nil, err
	}
	versions := map[string]string{}
	for _, p := range ps {
		if !p.Enabled {
			continue
		}
		key := domainplugin.SnapshotKey(p)
		if err := m.Store.Put(ctx, "plugin-version", key, p); err != nil {
			return nil, err
		}
		versions[p.ID] = key
	}
	return versions, nil
}
func (m *Manager) Tools(ctx context.Context, versions map[string]string, allow []string) ([]agent.Tool, error) {
	out := []agent.Tool{}
	ids := []string{}
	for id := range versions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		var p domainplugin.Plugin
		if err := m.Store.Get(ctx, "plugin-version", versions[id], &p); err != nil {
			return nil, err
		}
		for _, t := range p.Manifest.Tools {
			t.Name = p.ID + "__" + t.Name
			if allow != nil && !slices.Contains(allow, t.Name) {
				continue
			}
			if !domainplugin.Granted(p.Grants, t.Permissions) {
				continue
			}
			out = append(out, t)
		}
	}
	return out, nil
}
func (m *Manager) Health(ctx context.Context, id string) error {
	var p domainplugin.Plugin
	if err := m.Store.Get(ctx, "plugin", id, &p); err != nil {
		return err
	}
	key := domainplugin.SnapshotKey(p)
	if err := m.Store.Put(ctx, "plugin-version", key, p); err != nil {
		return err
	}
	return m.runtime.Health(ctx, key, p, m.configurationFor(p))
}
