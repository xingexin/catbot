package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/config"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepo "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/infra/agent/modelapi"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
	secret "github.com/xingexin/catbot/internal/infra/vault"
	"time"
)

type Service struct {
	Store   store.Store
	Vault   *secret.Vault
	Options config.Options
}

func (a *Service) SaveConfig(ctx context.Context, c agent.Config) (agent.Config, error) {
	if err := agent.ValidateConfig(&c); err != nil {
		return c, err
	}
	if c.ID == "" {
		c.ID = idgen.New()
	}
	referencesUnlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return c, err
	}
	defer referencesUnlock()
	if err := lifecycleRepo.RequireActive(ctx, a.Store, lifecycle.ResourceConfig, c.ID); err != nil {
		return c, err
	}
	if c.CredentialID != "" {
		if err := lifecycleRepo.RequireActive(ctx, a.Store, lifecycle.ResourceSecret, c.CredentialID); err != nil {
			return c, err
		}
		if _, err := a.Vault.Get(ctx, c.CredentialID); err != nil {
			return c, errors.New("credential does not exist")
		}
	}
	if err := a.Store.Put(ctx, "config", c.ID, c); err != nil {
		return c, err
	}
	return c, nil
}
func (a *Service) TestConfig(ctx context.Context, id string) (any, error) {
	c, err := a.activeConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Kind == "sdk" {
		return map[string]string{"status": "requires_conversation_test", "message": "Use the conversation page to test this SDK and its authentication."}, nil
	}
	key, err := a.Vault.Get(ctx, c.CredentialID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	t, err := (&modelapi.Model{}).Step(ctx, c, key, "Reply briefly.", []agent.Entry{{Role: "user", Text: "Reply with OK."}}, nil, func(string, map[string]any) error { return nil })
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "reply": t.Text, "usage": t.Usage}, nil
}

// activeConfig snapshots a configuration under the same lock used by archive.
// The caller releases this admission boundary before any external model I/O.
func (a *Service) activeConfig(ctx context.Context, id string) (agent.Config, error) {
	unlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return agent.Config{}, err
	}
	defer unlock()
	if err := a.requireActiveConfig(ctx, id); err != nil {
		return agent.Config{}, err
	}
	var c agent.Config
	err = a.Store.Get(ctx, "config", id, &c)
	return c, err
}

// requireActiveConfig is called while holding lifecycle.ReferenceLock.
func (a *Service) requireActiveConfig(ctx context.Context, id string) error {
	err := lifecycleRepo.RequireActive(ctx, a.Store, lifecycle.ResourceConfig, id)
	if errors.Is(err, lifecycleRepo.ErrArchived) {
		return fmt.Errorf("模型配置已归档，请在归档栏恢复或切换其他模型配置: %w", err)
	}
	return err
}
