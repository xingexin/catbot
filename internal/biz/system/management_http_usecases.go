package system

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepo "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/infra/health"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/vault"
	"time"
)

type CredentialInput struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (a *Service) Credentials(ctx context.Context) ([]map[string]string, error) {
	raw, err := a.List(ctx, "secret")
	if err != nil {
		return nil, err
	}
	out := []map[string]string{}
	for _, item := range raw {
		var v vault.Record
		if err := json.Unmarshal(item, &v); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"id": v.ID, "name": v.Name})
	}
	return out, nil
}
func (a *Service) SaveCredential(ctx context.Context, in CredentialInput) (map[string]string, error) {
	if in.Name == "" || in.Value == "" {
		return nil, errors.New("name and value are required")
	}
	referencesUnlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return nil, err
	}
	defer referencesUnlock()
	if in.ID == "" {
		in.ID = idgen.New()
	}
	if err := lifecycleRepo.RequireActive(ctx, a.Store, lifecycle.ResourceSecret, in.ID); err != nil {
		return nil, err
	}
	if err := a.Vault.Set(ctx, in.ID, in.Name, in.Value); err != nil {
		return nil, err
	}
	return map[string]string{"id": in.ID, "name": in.Name}, nil
}
func (a *Service) Status(ctx context.Context, temporalPing func(context.Context) error, qqConfigured bool) map[string]any {
	result := map[string]any{"database": "ok", "temporal": "unavailable", "runtime": "unavailable", "qqConfigured": qqConfigured}
	if err := a.Ping(ctx); err != nil {
		result["database"] = "unavailable"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if temporalPing != nil && temporalPing(ctx) == nil {
		result["temporal"] = "ok"
	}
	if v, err := health.Runtime(ctx, a.Options.RuntimeURL, a.Options.RuntimeToken); err == nil {
		result["runtime"] = v
	}
	return result
}
