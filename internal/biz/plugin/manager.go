package plugin

import (
	"context"

	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	infraplugin "github.com/xingexin/catbot/internal/infra/plugin"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
)

// PackageLoader reads and retains a self-contained, immutable plugin bundle.
type PackageLoader interface {
	Read(string) (string, domainplugin.Manifest, error)
	Freeze(context.Context, string, domainplugin.Manifest) (string, error)
}

// Runtime owns process lifecycle; configuration is resolved only on process start.
type Runtime interface {
	Health(context.Context, string, domainplugin.Plugin, func(context.Context) (map[string]any, error)) error
	Call(context.Context, string, domainplugin.Plugin, func(context.Context) (map[string]any, error), string, map[string]any, string) (any, error)
	Close()
}

type Manager struct {
	PauseDependent func(context.Context, string, string) error
	Store          store.Store
	Vault          *vault.Vault
	packages       PackageLoader
	runtime        Runtime
}

var _ PackageLoader = (*infraplugin.Packages)(nil)
var _ Runtime = (*infraplugin.Runtime)(nil)

func New(s store.Store, v *vault.Vault, root, data, host string, token func(string) string) *Manager {
	return NewWithRuntime(s, v, infraplugin.NewPackages(s, root, data), infraplugin.NewRuntime(data, host, token))
}

func NewWithRuntime(s store.Store, v *vault.Vault, packages PackageLoader, runtime Runtime) *Manager {
	return &Manager{Store: s, Vault: v, packages: packages, runtime: runtime}
}

func (m *Manager) configurationFor(p domainplugin.Plugin) func(context.Context) (map[string]any, error) {
	return func(ctx context.Context) (map[string]any, error) {
		cfg, err := m.configuration(ctx, p)
		if err != nil {
			return nil, err
		}
		if err := domainplugin.Validate(p.Manifest.ConfigSchema, cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}
}

func (m *Manager) Close() { m.runtime.Close() }
