package agent

import (
	"context"
	"errors"
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
	var c agent.Config
	if err := a.Store.Get(ctx, "config", id, &c); err != nil {
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
