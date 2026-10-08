package system

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/infra/health"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
	"time"
)

type CredentialInput struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (a *Service) Credentials(ctx context.Context) ([]map[string]string, error) {
	rows, err := store.All[vault.Record](ctx, a.Store, "secret")
	if err != nil {
		return nil, err
	}
	out := []map[string]string{}
	for _, v := range rows {
		out = append(out, map[string]string{"id": v.ID, "name": v.Name})
	}
	return out, nil
}
func (a *Service) SaveCredential(ctx context.Context, in CredentialInput) (map[string]string, error) {
	if in.Name == "" || in.Value == "" {
		return nil, errors.New("name and value are required")
	}
	if in.ID == "" {
		in.ID = idgen.New()
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
