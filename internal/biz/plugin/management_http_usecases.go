package plugin

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/domain/agent"
	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	"strings"
)

func (m *Manager) ConfigureManaged(ctx context.Context, id string, cfg map[string]any, grants []string) (domainplugin.Plugin, error) {
	err := domainplugin.ValidateModels(ctx, id, cfg, func(ctx context.Context, id string) (agent.Config, error) {
		var c agent.Config
		err := m.Store.Get(ctx, "config", id, &c)
		return c, err
	})
	if err != nil {
		return domainplugin.Plugin{}, err
	}
	return m.Configure(ctx, id, cfg, grants)
}

func (m *Manager) Logs(ctx context.Context, id string) (string, error) {
	var p domainplugin.Plugin
	if err := m.Store.Get(ctx, "plugin", id, &p); err != nil {
		return "", err
	}
	reader, ok := m.runtime.(interface{ Logs(string) (string, error) })
	if !ok {
		return "", errors.New("plugin logs unavailable")
	}
	text, err := reader.Logs(p.ID)
	if err != nil {
		return "", err
	}
	for _, id := range p.Secrets {
		value, err := m.Vault.Get(ctx, id)
		if err == nil && value != "" {
			text = strings.ReplaceAll(text, value, "[REDACTED]")
		}
	}
	return text, nil
}

func (m *Manager) AvailableTools(ctx context.Context) ([]agent.Tool, error) {
	versions, err := m.Snapshots(ctx)
	if err != nil {
		return nil, err
	}
	tools, err := m.Tools(ctx, versions, nil)
	if err != nil {
		return nil, err
	}
	return append(tools, agent.BuiltinTools()...), nil
}
