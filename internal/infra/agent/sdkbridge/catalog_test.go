package sdkbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xingexin/catbot/internal/domain/agent"
)

func TestCatalogBridgePreservesDraftEnvironmentAndAuthentication(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		key  string
	}{
		{name: "credential supplied", key: "catalog-test-key"},
		{name: "runtime authentication"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer internal-test-token" {
					t.Error("model discovery route or internal authentication changed")
				}
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var config agent.Config
				if err := json.Unmarshal(payload["config"], &config); err != nil {
					t.Error(err)
				}
				if config.CodeBuddyEnvironment != agent.CodeBuddyEnvironmentIOA || config.Name != "" || config.Model != "" || config.Provider != "codebuddy" {
					t.Errorf("draft changed: %#v", config)
				}
				var wireConfig map[string]json.RawMessage
				if err := json.Unmarshal(payload["config"], &wireConfig); err != nil || string(wireConfig["codebuddyEnvironment"]) != "2" {
					t.Error("environment must remain a numeric wire enum")
				}
				var key string
				if tt.key != "" {
					if err := json.Unmarshal(payload["apiKey"], &key); err != nil || key != tt.key {
						t.Error("resolved server credential was not forwarded")
					}
				} else if _, exists := payload["apiKey"]; exists {
					t.Error("empty key should be omitted for runtime authentication")
				}
				for _, field := range []string{"runId", "sessionId", "prompt", "gatewayToken"} {
					if _, exists := payload[field]; exists {
						t.Errorf("discovery unexpectedly sends execution field %s", field)
					}
				}
				fmt.Fprint(w, `{"source":1,"models":[{"id":"glm-5.3","name":"GLM 5.3"},{"id":"glm-5.3"},{"id":"other-model"}]}`)
			}))
			defer server.Close()
			bridge := Bridge{URL: server.URL + "/", Token: "internal-test-token"}
			catalog, err := bridge.ListModels(t.Context(), agent.Config{Kind: "sdk", Provider: "codebuddy", CodeBuddyEnvironment: agent.CodeBuddyEnvironmentIOA}, tt.key)
			if err != nil || catalog.Source != agent.ModelCatalogLive || len(catalog.Models) != 2 || catalog.Models[1].Name != "other-model" {
				t.Fatalf("catalog=%#v err=%v", catalog, err)
			}
		})
	}
}

func TestCatalogBridgeRejectsInvalidOrSensitiveErrorResponses(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, body, want string
		status           int
	}{
		{name: "error credentials redacted", status: 401, body: `{"error":"failed catalog-test-key and internal-test-token"}`, want: "[redacted]"},
		{name: "unstructured error hidden", status: 500, body: "catalog-test-key internal-test-token", want: "HTTP 500"},
		{name: "malformed JSON hidden", status: 200, body: "catalog-test-key internal-test-token", want: "格式无效"},
		{name: "empty catalog", status: 200, body: `{"source":1,"models":[]}`, want: "未取得有效"},
		{name: "unknown source", status: 200, body: `{"source":0,"models":[{"id":"a"}]}`, want: "未取得有效"},
		{name: "missing ID", status: 200, body: `{"source":1,"models":[{"name":"A"}]}`, want: "格式异常"},
		{name: "oversized response", status: 200, body: strings.Repeat("a", (2<<20)+1), want: "过大"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			bridge := Bridge{URL: server.URL, Token: "internal-test-token"}
			catalog, err := bridge.ListModels(t.Context(), agent.Config{}, "catalog-test-key")
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "catalog-test-key") || strings.Contains(err.Error(), "internal-test-token") || len(catalog.Models) != 0 {
				t.Fatalf("unsafe catalog failure: catalog=%#v err=%v", catalog, err)
			}
		})
	}
}

func TestCatalogBridgeRedirectDoesNotForwardCredentials(t *testing.T) {
	t.Parallel()
	var contacted atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted.Store(true)
		fmt.Fprint(w, `{"source":1,"models":[{"id":"a"}]}`)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	var redirects atomic.Int32
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { redirects.Add(1); return nil }}
	bridge := Bridge{URL: server.URL, Token: "internal-test-token", HTTP: client}
	_, err := bridge.ListModels(t.Context(), agent.Config{}, "catalog-test-key")
	if err == nil || contacted.Load() || redirects.Load() != 0 {
		t.Fatalf("redirect was followed: contacted=%t redirects=%d err=%v", contacted.Load(), redirects.Load(), err)
	}
	if err := client.CheckRedirect(nil, nil); err != nil || redirects.Load() != 1 {
		t.Fatal("injected client was modified")
	}
}

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCatalogBridgeCancellationAndTransportErrors(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		cancelled bool
	}{
		{name: "transport error hides details"},
		{name: "cancellation preserved", cancelled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			client := &http.Client{Transport: catalogTransport(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("transport leaked catalog-test-key internal-test-token")
			})}
			bridge := Bridge{URL: "https://fixture.invalid", Token: "internal-test-token", HTTP: client}
			_, err := bridge.ListModels(ctx, agent.Config{}, "catalog-test-key")
			if err == nil || strings.Contains(err.Error(), "catalog-test-key") || strings.Contains(err.Error(), "internal-test-token") {
				t.Fatalf("unsafe error: %v", err)
			}
			if tt.cancelled && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}
