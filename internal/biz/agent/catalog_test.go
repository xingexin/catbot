package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	domain "github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepo "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
)

type catalogFunc func(context.Context, domain.Config, string) (domain.ModelCatalog, error)

func (f catalogFunc) ListModels(ctx context.Context, c domain.Config, key string) (domain.ModelCatalog, error) {
	return f(ctx, c, key)
}

func TestCatalogUsesDraftEnvironmentAndServerCredentialWithoutWrites(t *testing.T) {
	ctx := context.Background()
	state := store.NewMemory()
	secrets, err := vault.New(state, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	const key = "fixture-only-catalog-key"
	if err := secrets.Set(ctx, "key-id", "fixture", key); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := Service{Store: state, Vault: secrets, SDKModels: catalogFunc(func(ctx context.Context, c domain.Config, actual string) (domain.ModelCatalog, error) {
		calls++
		if actual != key || c.CodeBuddyEnvironment != domain.CodeBuddyEnvironmentIOA || c.Model != "" || c.Name != "" {
			t.Fatal("draft environment or server-side credential not forwarded correctly")
		}
		return domain.ModelCatalog{Source: domain.ModelCatalogLive, Models: []domain.ModelOption{{ID: "fixture-model"}}}, nil
	})}
	result, err := service.ListModels(ctx, domain.Config{Kind: "sdk", Provider: "codebuddy", CodeBuddyEnvironment: domain.CodeBuddyEnvironmentIOA, CredentialID: "key-id"})
	if err != nil || calls != 1 || len(result.Models) != 1 {
		t.Fatalf("result=%+v, calls=%d, err=%v", result, calls, err)
	}
	for _, kind := range []string{"config", "run", "session", "model_call"} {
		records, err := store.All[map[string]any](ctx, state, kind)
		if err != nil || len(records) != 0 {
			t.Fatalf("discovery wrote %s: %v", kind, err)
		}
	}
}

func TestCatalogRejectsUnavailableReferencesAndUnsupportedSDK(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider string
		archive  lifecycle.Resource
		missing  bool
	}{
		{name: "archived credential", provider: "codebuddy", archive: lifecycle.ResourceSecret},
		{name: "archived config", provider: "codebuddy", archive: lifecycle.ResourceConfig},
		{name: "missing credential", provider: "codebuddy", missing: true},
		{name: "Codex SDK unsupported", provider: "codex"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			state := store.NewMemory()
			secrets, err := vault.New(state, base64.StdEncoding.EncodeToString(make([]byte, 32)))
			if err != nil {
				t.Fatal(err)
			}
			if !tt.missing {
				if err := secrets.Set(ctx, "key-id", "fixture", "fixture-key"); err != nil {
					t.Fatal(err)
				}
			}
			c := domain.Config{ID: "config-id", Kind: "sdk", Provider: tt.provider, CredentialID: "key-id"}
			if tt.archive != lifecycle.ResourceUnknown {
				id := "key-id"
				if tt.archive == lifecycle.ResourceConfig {
					id = "config-id"
				}
				if err := lifecycleRepo.Mark(ctx, state, lifecycle.ArchiveRecord{Resource: tt.archive, RecordID: id, ArchivedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			lister := catalogFunc(func(context.Context, domain.Config, string) (domain.ModelCatalog, error) {
				called = true
				return domain.ModelCatalog{}, nil
			})
			s := Service{Store: state, Vault: secrets, SDKModels: lister, APIModels: lister}
			if _, err := s.ListModels(ctx, c); err == nil || called {
				t.Fatalf("expected rejection before provider call: err=%v called=%v", err, called)
			}
		})
	}
}

func TestCatalogProtectsCredentialInErrorAndResponse(t *testing.T) {
	for _, tt := range []struct {
		name string
		fail bool
	}{{"error", true}, {"response", false}} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			state := store.NewMemory()
			secrets, err := vault.New(state, base64.StdEncoding.EncodeToString(make([]byte, 32)))
			if err != nil {
				t.Fatal(err)
			}
			const key = "fixture-sensitive-key"
			if err := secrets.Set(ctx, "key-id", "fixture", key); err != nil {
				t.Fatal(err)
			}
			s := Service{Store: state, Vault: secrets, APIModels: catalogFunc(func(context.Context, domain.Config, string) (domain.ModelCatalog, error) {
				if tt.fail {
					return domain.ModelCatalog{}, errors.New("upstream echoed " + key)
				}
				return domain.ModelCatalog{Source: domain.ModelCatalogLive, Models: []domain.ModelOption{{ID: "model", Description: key}}}, nil
			})}
			_, err = s.ListModels(ctx, domain.Config{Kind: "api", Protocol: "openai-chat", BaseURL: "https://example.invalid/v1", CredentialID: "key-id"})
			if err == nil || strings.Contains(err.Error(), key) {
				t.Fatalf("unsafe error %v", err)
			}
		})
	}
}

func TestCatalogClaudeUsesSDKDirectoryWithoutAPIFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := store.NewMemory()
	secrets, err := vault.New(state, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	const key = "fixture-claude-directory-key"
	if err := secrets.Set(ctx, "claude-key", "fixture", key); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := Service{
		Store: state,
		Vault: secrets,
		SDKModels: catalogFunc(func(ctx context.Context, c domain.Config, actual string) (domain.ModelCatalog, error) {
			calls++
			if domain.ParseSDKProvider(c.Provider) != domain.SDKClaude || actual != key || c.BaseURL != "https://claude.invalid" || c.Model != "" {
				t.Fatal("Claude draft connection or server credential not forwarded")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("metadata request has no deadline")
			}
			return domain.ModelCatalog{Source: domain.ModelCatalogLive, Models: []domain.ModelOption{{ID: "fixture-claude", Name: "Fixture Claude"}}}, nil
		}),
		APIModels: catalogFunc(func(context.Context, domain.Config, string) (domain.ModelCatalog, error) {
			t.Fatal("SDK directory must not silently use API directory")
			return domain.ModelCatalog{}, nil
		}),
	}
	result, err := s.ListModels(ctx, domain.Config{Kind: "sdk", Provider: "claude", BaseURL: "https://claude.invalid", CredentialID: "claude-key"})
	if err != nil || calls != 1 || len(result.Models) != 1 || result.Models[0].ID != "fixture-claude" {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
	for _, kind := range []string{"config", "run", "session", "model_call"} {
		records, err := store.All[map[string]any](ctx, state, kind)
		if err != nil || len(records) != 0 {
			t.Fatalf("discovery wrote %s: %v", kind, err)
		}
	}
}
