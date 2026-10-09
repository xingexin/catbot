package plugin

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/store"
)

func (m *Manager) Register(ctx context.Context, dir string) (domainplugin.Plugin, error) {
	full, manifest, err := m.packages.Read(dir)
	if err != nil {
		return domainplugin.Plugin{}, err
	}
	manifest, err = domainplugin.NormalizeManifest(manifest)
	if err != nil {
		return domainplugin.Plugin{}, err
	}
	unlock, err := m.Store.Lock(ctx, "plugin-config:"+manifest.ID)
	if err != nil {
		return domainplugin.Plugin{}, err
	}
	defer unlock()
	referenceUnlock, err := m.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return domainplugin.Plugin{}, err
	}
	defer referenceUnlock()
	archived, err := lifecycleRepository.Archived(ctx, m.Store, lifecycle.ResourcePlugin, manifest.ID)
	if err != nil {
		return domainplugin.Plugin{}, err
	}
	if archived {
		return domainplugin.Plugin{}, lifecycleRepository.ErrArchived
	}
	frozen, err := m.packages.Freeze(ctx, full, manifest)
	if err != nil {
		return domainplugin.Plugin{}, err
	}
	p := domainplugin.Plugin{ID: manifest.ID, Manifest: manifest, Directory: frozen, Config: map[string]any{}, Secrets: map[string]string{}, Grants: []string{}}
	var old domainplugin.Plugin
	if err := m.Store.Get(ctx, "plugin", p.ID, &old); err == nil {
		if old.Manifest.Version == manifest.Version && old.Directory != frozen {
			return p, errors.New("a new package must use a new version")
		}
		p.Config = old.Config
		p.Secrets = old.Secrets
		p.Grants = old.Grants
		p.Enabled = old.Enabled
	}
	if err := m.Store.Delete(ctx, store.PurgedRecordKind, store.PurgeKey("plugin", p.ID)); err != nil {
		return p, err
	}
	err = m.Store.Put(ctx, "plugin", p.ID, p)
	return p, err
}
