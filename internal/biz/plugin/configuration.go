package plugin

import (
	"context"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/idgen"
)

func (m *Manager) Configure(ctx context.Context, id string, cfg map[string]any, grants []string) (domainplugin.Plugin, error) {
	unlock, err := m.Store.Lock(ctx, "plugin-config:"+id)
	if err != nil {
		return domainplugin.Plugin{}, err
	}
	defer unlock()
	referenceUnlock, err := m.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return domainplugin.Plugin{}, err
	}
	defer referenceUnlock()
	if err := lifecycleRepository.RequireActive(ctx, m.Store, lifecycle.ResourcePlugin, id); err != nil {
		return domainplugin.Plugin{}, err
	}
	var p domainplugin.Plugin
	if err := m.Store.Get(ctx, "plugin", id, &p); err != nil {
		return p, err
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	if err := domainplugin.ValidateModels(ctx, id, cfg, func(ctx context.Context, configID string) (agent.Config, error) {
		var config agent.Config
		if err := lifecycleRepository.RequireActive(ctx, m.Store, lifecycle.ResourceConfig, configID); err != nil {
			return config, err
		}
		err := m.Store.Get(ctx, "config", configID, &config)
		return config, err
	}); err != nil {
		return p, err
	}
	if p.Secrets == nil {
		p.Secrets = map[string]string{}
	}
	properties, _ := p.Manifest.ConfigSchema["properties"].(map[string]any)
	for name, prop := range properties {
		obj, _ := prop.(map[string]any)
		if obj["format"] != "password" {
			continue
		}
		if value, ok := cfg[name].(string); ok && value != "" {
			id := "plugin-" + p.ID + "-" + name + "-" + idgen.New()
			if err := m.Vault.Set(ctx, id, p.Manifest.Name+" / "+name, value); err != nil {
				return p, err
			}
			p.Secrets[name] = id
		}
		delete(cfg, name)
	}
	p.Config = cfg
	p.Grants = grants
	merged, err := m.configuration(ctx, p)
	if err != nil {
		return p, err
	}
	if err = domainplugin.Validate(p.Manifest.ConfigSchema, merged); err != nil {
		return p, err
	}
	err = m.Store.Put(ctx, "plugin", p.ID, p)
	return p, err
}
func (m *Manager) configuration(ctx context.Context, p domainplugin.Plugin) (map[string]any, error) {
	cfg := map[string]any{}
	for k, v := range p.Config {
		cfg[k] = v
	}
	for k, id := range p.Secrets {
		value, err := m.Vault.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		cfg[k] = value
	}
	return cfg, nil
}
