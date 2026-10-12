package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepo "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/infra/store"
)

type catalogLister func(context.Context, agent.Config, string) (agent.ModelCatalog, error)

func (f catalogLister) ListModels(ctx context.Context, c agent.Config, key string) (agent.ModelCatalog, error) {
	return f(ctx, c, key)
}

func validCatalog() agent.ModelCatalog {
	return agent.ModelCatalog{Source: agent.ModelCatalogLive, Models: []agent.ModelOption{{ID: "glm-5.3", Name: "GLM 5.3"}}}
}

func TestHTTPModelCatalogAcceptsUnsavedDraftWithoutWriting(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, body string
		env        agent.CodeBuddyEnvironment
	}{
		{name: "CodeBuddy iOA draft", body: `{"kind":"sdk","provider":"codebuddy","codebuddyEnvironment":2,"credentialId":"catalog-key"}`, env: agent.CodeBuddyEnvironmentIOA},
		{name: "CodeBuddy legacy draft inherits runtime", body: `{"kind":"sdk","provider":"codebuddy","credentialId":"catalog-key"}`, env: agent.CodeBuddyEnvironmentInherit},
		{name: "Claude SDK draft", body: `{"kind":"sdk","provider":"claude","credentialId":"catalog-key"}`},
		{name: "API draft", body: `{"kind":"api","protocol":"openai-chat","baseUrl":"https://fixture.invalid/v1","credentialId":"catalog-key"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if err := f.agent.Vault.Set(t.Context(), "catalog-key", "fixture key", "server-test-key"); err != nil {
				t.Fatal(err)
			}
			before := catalogStoreSnapshot(t, f.store)
			readOnly := &catalogNoMutationStore{Store: f.store}
			f.agent.Store = readOnly
			calls := 0
			list := catalogLister(func(ctx context.Context, c agent.Config, key string) (agent.ModelCatalog, error) {
				calls++
				if c.ID != "" || c.Name != "" || c.Model != "" || c.CodeBuddyEnvironment != tt.env || key != "server-test-key" {
					t.Errorf("draft or credential contract changed: config=%#v key present=%t", c, key != "")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("discovery has no bounded context")
				}
				return validCatalog(), nil
			})
			f.agent.APIModels, f.agent.SDKModels = list, list
			response := f.request(http.MethodPost, "/api/configs/models", tt.body)
			if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
			}
			var catalog agent.ModelCatalog
			if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil || !reflect.DeepEqual(catalog, validCatalog()) {
				t.Fatalf("response=%s error=%v", response.Body, err)
			}
			if strings.Contains(response.Body.String(), "server-test-key") || readOnly.writes.Load() != 0 || !reflect.DeepEqual(before, catalogStoreSnapshot(t, f.store)) {
				t.Fatal("discovery leaked a key or changed stored data")
			}
		})
	}
}

func TestHTTPModelCatalogRequiresAuthenticationAndSameOrigin(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var calls atomic.Int32
	f.agent.SDKModels = catalogLister(func(context.Context, agent.Config, string) (agent.ModelCatalog, error) {
		calls.Add(1)
		return validCatalog(), nil
	})
	body := `{"kind":"sdk","provider":"codebuddy","codebuddyEnvironment":2}`
	noCookie := f
	noCookie.cookie = nil
	if response := noCookie.request(http.MethodPost, "/api/configs/models", body); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated discovery: %d %s", response.Code, response.Body)
	}
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/configs/models", strings.NewReader(body))
	req.AddCookie(f.cookie)
	req.Header.Set("Origin", "https://unrelated.invalid")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, req)
	if response.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("cross-origin discovery=%d, lister calls=%d", response.Code, calls.Load())
	}
}

func TestHTTPModelCatalogRejectsInvalidDraftBeforeCallingService(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, body string }{
		{name: "unknown strategy", body: `{"kind":"other"}`},
		{name: "unknown provider", body: `{"kind":"sdk","provider":"other"}`},
		{name: "unknown protocol", body: `{"kind":"api","protocol":"other","baseUrl":"https://fixture.invalid"}`},
		{name: "missing base URL", body: `{"kind":"api","protocol":"openai-chat"}`},
		{name: "invalid environment", body: `{"kind":"sdk","provider":"codebuddy","codebuddyEnvironment":99}`},
		{name: "string environment", body: `{"kind":"sdk","provider":"codebuddy","codebuddyEnvironment":"ioa"}`},
		{name: "environment on API", body: `{"kind":"api","protocol":"anthropic","baseUrl":"https://fixture.invalid","codebuddyEnvironment":2}`},
		{name: "embedded URL credentials", body: `{"kind":"api","protocol":"openai-chat","baseUrl":"https://draft-secret@fixture.invalid"}`},
		{name: "unavailable SDK catalog", body: `{"kind":"sdk","provider":"codex"}`},
		{name: "multiple JSON values", body: `{"kind":"sdk","provider":"codebuddy"} {}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			calls := 0
			list := catalogLister(func(context.Context, agent.Config, string) (agent.ModelCatalog, error) {
				calls++
				return validCatalog(), nil
			})
			f.agent.APIModels, f.agent.SDKModels = list, list
			before := catalogStoreSnapshot(t, f.store)
			response := f.request(http.MethodPost, "/api/configs/models", tt.body)
			if response.Code != http.StatusBadRequest || calls != 0 || strings.Contains(response.Body.String(), "draft-secret") || !reflect.DeepEqual(before, catalogStoreSnapshot(t, f.store)) {
				t.Fatalf("invalid draft accepted, leaked or mutated: code=%d calls=%d response=%s", response.Code, calls, response.Body)
			}
		})
	}
}

func TestHTTPModelCatalogRejectsArchivedAndMissingCredentials(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name          string
		archiveSecret bool
		archiveConfig bool
		missingSecret bool
	}{
		{name: "archived credential", archiveSecret: true},
		{name: "missing credential", missingSecret: true},
		{name: "archived saved configuration", archiveConfig: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if !tt.missingSecret {
				if err := f.agent.Vault.Set(t.Context(), "catalog-key", "fixture key", "server-test-key"); err != nil {
					t.Fatal(err)
				}
			}
			c := agent.Config{ID: "saved-config", Kind: "sdk", Provider: "codebuddy", CredentialID: "catalog-key"}
			if err := f.store.Put(t.Context(), "config", c.ID, c); err != nil {
				t.Fatal(err)
			}
			if tt.archiveSecret || tt.archiveConfig {
				resource, id := lifecycle.ResourceSecret, "catalog-key"
				if tt.archiveConfig {
					resource, id = lifecycle.ResourceConfig, c.ID
				}
				if err := lifecycleRepo.Mark(t.Context(), f.store, lifecycle.ArchiveRecord{Resource: resource, RecordID: id, ArchivedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			f.agent.SDKModels = catalogLister(func(context.Context, agent.Config, string) (agent.ModelCatalog, error) {
				calls++
				return validCatalog(), nil
			})
			before := catalogStoreSnapshot(t, f.store)
			body, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			response := f.request(http.MethodPost, "/api/configs/models", string(body))
			if response.Code != http.StatusBadRequest || calls != 0 || strings.Contains(response.Body.String(), "server-test-key") || !reflect.DeepEqual(before, catalogStoreSnapshot(t, f.store)) {
				t.Fatalf("credential guard failed: status=%d calls=%d body=%s", response.Code, calls, response.Body)
			}
		})
	}
}

func TestHTTPModelCatalogDoesNotExposeUpstreamCredentialEcho(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		result agent.ModelCatalog
		err    error
	}{
		{name: "error", err: errors.New("upstream rejected server-test-key")},
		{name: "model ID", result: agent.ModelCatalog{Source: agent.ModelCatalogLive, Models: []agent.ModelOption{{ID: "server-test-key", Name: "a"}}}},
		{name: "model name", result: agent.ModelCatalog{Source: agent.ModelCatalogLive, Models: []agent.ModelOption{{ID: "a", Name: "server-test-key"}}}},
		{name: "description", result: agent.ModelCatalog{Source: agent.ModelCatalogLive, Models: []agent.ModelOption{{ID: "a", Name: "A", Description: "server-test-key"}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if err := f.agent.Vault.Set(t.Context(), "catalog-key", "fixture key", "server-test-key"); err != nil {
				t.Fatal(err)
			}
			f.agent.SDKModels = catalogLister(func(context.Context, agent.Config, string) (agent.ModelCatalog, error) { return tt.result, tt.err })
			response := f.request(http.MethodPost, "/api/configs/models", `{"kind":"sdk","provider":"codebuddy","credentialId":"catalog-key"}`)
			if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "server-test-key") {
				t.Fatalf("upstream key echoed: status=%d response=%s", response.Code, response.Body)
			}
		})
	}
}

type catalogNoMutationStore struct {
	store.Store
	writes atomic.Int32
}

func (s *catalogNoMutationStore) Put(context.Context, string, string, any) error {
	s.writes.Add(1)
	return errors.New("unexpected write during discovery")
}
func (s *catalogNoMutationStore) Delete(context.Context, string, string) error {
	s.writes.Add(1)
	return errors.New("unexpected delete during discovery")
}
func (s *catalogNoMutationStore) Append(context.Context, store.EventRecord) error {
	s.writes.Add(1)
	return errors.New("unexpected event during discovery")
}

func catalogStoreSnapshot(t *testing.T, state store.Store) map[string][]json.RawMessage {
	t.Helper()
	kinds := []string{lifecycle.ArchiveStorageKind, "purged", "snapshot", "step-result", "schedule-intent", "plugin-version", "login"}
	for resource := lifecycle.ResourceSession; resource <= lifecycle.ResourceSecret; resource++ {
		kinds = append(kinds, resource.StorageKind())
	}
	out := make(map[string][]json.RawMessage, len(kinds))
	for _, kind := range kinds {
		rows, err := state.List(t.Context(), kind)
		if err != nil {
			t.Fatal(err)
		}
		out[kind] = rows
	}
	return out
}
