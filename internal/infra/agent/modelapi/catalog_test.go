package modelapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain/agent"
)

func TestListModelsProtocolURLAndAuthentication(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, protocol, basePath, path string
		anthropic                      bool
	}{
		{name: "chat preserves v1", protocol: "openai-chat", basePath: "/v1/", path: "/v1/models"},
		{name: "responses shares list endpoint", protocol: "openai-responses", basePath: "/gateway/v1", path: "/gateway/v1/models"},
		{name: "custom OpenAI base is literal", protocol: "openai-chat", basePath: "/gateway", path: "/gateway/models"},
		{name: "anthropic adds v1", protocol: "anthropic", path: "/v1/models", anthropic: true},
		{name: "anthropic keeps existing v1", protocol: "anthropic", basePath: "/proxy/v1/", path: "/proxy/v1/models", anthropic: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != tt.path {
					t.Errorf("request %s %s, want GET %s", r.Method, r.URL.Path, tt.path)
				}
				if tt.anthropic {
					if r.Header.Get("x-api-key") != "test-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
						t.Error("incorrect Anthropic authentication")
					}
					if r.URL.Query().Get("limit") != "1000" {
						t.Error("missing Anthropic page limit")
					}
				} else if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("x-api-key") != "" || r.URL.RawQuery != "" {
					t.Error("incorrect OpenAI authentication or query")
				}
				fmt.Fprint(w, `{"data":[{"id":"vendor/model-A:latest","display_name":"Display name","name":"Ignored","description":"Details"},{"id":"vendor/model-A:latest"},{"id":""},{"name":"no id"},{"id":"model-B","name":"B"},{"id":"model-b"}]}`)
			}))
			defer server.Close()
			got, err := (&Model{}).ListModels(t.Context(), agent.Config{Protocol: tt.protocol, BaseURL: server.URL + tt.basePath}, "test-key")
			if err != nil {
				t.Fatal(err)
			}
			want := []agent.ModelOption{{ID: "vendor/model-A:latest", Name: "Display name", Description: "Details"}, {ID: "model-B", Name: "B"}, {ID: "model-b", Name: "model-b"}}
			if got.Source != agent.ModelCatalogLive || !reflect.DeepEqual(got.Models, want) {
				t.Fatalf("catalog=%#v, want models=%#v and live source", got, want)
			}
		})
	}
}

func TestListModelsAnthropicPagination(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("after_id") == "" {
			fmt.Fprint(w, `{"data":[{"id":"model-A"}],"has_more":true,"last_id":"cursor/+ ?"}`)
			return
		}
		if r.URL.Query().Get("after_id") != "cursor/+ ?" || r.Header.Get("x-api-key") != "test-key" {
			t.Error("cursor or authentication lost on second page")
		}
		fmt.Fprint(w, `{"data":[{"id":"model-A"},{"id":"model-B"}],"has_more":false}`)
	}))
	defer server.Close()
	got, err := (&Model{}).ListModels(t.Context(), agent.Config{Protocol: "anthropic", BaseURL: server.URL}, "test-key")
	if err != nil || requests.Load() != 2 || len(got.Models) != 2 || got.Models[1].ID != "model-B" {
		t.Fatalf("catalog=%#v requests=%d error=%v", got, requests.Load(), err)
	}
}

func TestListModelsFailuresAreSafeAndSuggestManualInput(t *testing.T) {
	t.Parallel()
	manyModels := make([]map[string]string, catalogMaxModels+1)
	for i := range manyModels {
		manyModels[i] = map[string]string{"id": fmt.Sprintf("model-%d", i)}
	}
	tooMany, err := json.Marshal(map[string]any{"data": manyModels})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, protocol, response, want string
		status                         int
	}{
		{name: "unauthorized", protocol: "openai-chat", status: 401, response: `{"error":"test-secret-key"}`, want: "HTTP 401"},
		{name: "not supported", protocol: "openai-chat", status: 404, response: "test-secret-key", want: "HTTP 404"},
		{name: "empty list", protocol: "openai-chat", response: `{"data":[]}`, want: "未返回可用"},
		{name: "only missing IDs", protocol: "openai-chat", response: `{"data":[{"name":"test-secret-key"}]}`, want: "未返回可用"},
		{name: "malformed JSON", protocol: "openai-chat", response: `{"test-secret-key`, want: "格式无效"},
		{name: "wrong schema", protocol: "openai-chat", response: `{"data":"test-secret-key"}`, want: "格式无效"},
		{name: "too large", protocol: "openai-chat", response: strings.Repeat("a", catalogMaxPageBytes+1), want: "大小限制"},
		{name: "too many models", protocol: "openai-chat", response: string(tooMany), want: "数量限制"},
		{name: "missing cursor", protocol: "anthropic", response: `{"data":[{"id":"a"}],"has_more":true}`, want: "游标无效"},
		{name: "repeated cursor", protocol: "anthropic", response: `{"data":[{"id":"a"}],"has_more":true,"last_id":"a"}`, want: "游标无效"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				fmt.Fprint(w, tt.response)
			}))
			defer server.Close()
			got, err := (&Model{}).ListModels(t.Context(), agent.Config{Protocol: tt.protocol, BaseURL: server.URL}, "test-secret-key")
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "手动填写") || strings.Contains(err.Error(), "test-secret-key") || len(got.Models) != 0 {
				t.Fatalf("unsafe or incomplete failure: catalog=%#v err=%v", got, err)
			}
		})
	}
}

func TestListModelsLimitsPagination(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := requests.Add(1)
		fmt.Fprintf(w, `{"data":[{"id":"a"}],"has_more":true,"last_id":"%d"}`, page)
	}))
	defer server.Close()
	got, err := (&Model{}).ListModels(t.Context(), agent.Config{Protocol: "anthropic", BaseURL: server.URL}, "test-key")
	if err == nil || !strings.Contains(err.Error(), "分页限制") || len(got.Models) != 0 || requests.Load() != catalogMaxPages {
		t.Fatalf("catalog=%#v requests=%d err=%v", got, requests.Load(), err)
	}
}

func TestListModelsRejectsRedirectWithoutMutatingInjectedClient(t *testing.T) {
	t.Parallel()
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(true)
		fmt.Fprint(w, `{"data":[{"id":"a"}]}`)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/test-secret-key", http.StatusFound)
	}))
	defer server.Close()
	var originalRedirectCalls atomic.Int32
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		originalRedirectCalls.Add(1)
		return nil
	}}
	_, err := (&Model{HTTP: client}).ListModels(t.Context(), agent.Config{Protocol: "openai-chat", BaseURL: server.URL}, "test-secret-key")
	if err == nil || leaked.Load() || originalRedirectCalls.Load() != 0 || strings.Contains(err.Error(), "test-secret-key") {
		t.Fatalf("redirect leaked or unsafe error: contacted=%t err=%v", leaked.Load(), err)
	}
	if err := client.CheckRedirect(nil, nil); err != nil || originalRedirectCalls.Load() != 1 {
		t.Fatal("injected client's redirect policy was mutated")
	}
}

func TestListModelsCancellationAndTimeout(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		timeout bool
	}{
		{name: "caller cancellation"},
		{name: "injected client timeout", timeout: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !tt.timeout {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			client := &http.Client{}
			want := context.Canceled
			if tt.timeout {
				client.Timeout = 30 * time.Millisecond
				want = context.DeadlineExceeded
			}
			_, err := (&Model{HTTP: client}).ListModels(ctx, agent.Config{Protocol: "openai-chat", BaseURL: server.URL}, "test-secret-key")
			if !errors.Is(err, want) || strings.Contains(err.Error(), "test-secret-key") {
				t.Fatalf("err=%v, want %v without credentials", err, want)
			}
		})
	}
}

func TestListModelsRejectsInvalidConnectionWithoutRequest(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, protocol, base string }{
		{name: "unknown protocol", protocol: "unknown", base: "https://example.invalid"},
		{name: "missing base", protocol: "openai-chat"},
		{name: "invalid scheme", protocol: "openai-chat", base: "file:///test-secret-key"},
		{name: "URL credential", protocol: "openai-chat", base: "https://test-secret-key@example.invalid"},
		{name: "query credential", protocol: "openai-chat", base: "https://example.invalid?key=test-secret-key"},
		{name: "malformed URL", protocol: "openai-chat", base: "http://test-secret-key%"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := (&Model{}).ListModels(t.Context(), agent.Config{Protocol: tt.protocol, BaseURL: tt.base}, "test-secret-key")
			if err == nil || strings.Contains(err.Error(), "test-secret-key") {
				t.Fatalf("expected sanitized validation failure, got %v", err)
			}
		})
	}
}

type catalogRoundTripper func(*http.Request) (*http.Response, error)

func (f catalogRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestListModelsSanitizesTransportErrors(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: catalogRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("proxy rejected credential test-secret-key")
	})}
	_, err := (&Model{HTTP: client}).ListModels(t.Context(), agent.Config{Protocol: "openai-chat", BaseURL: "https://example.invalid"}, "test-secret-key")
	if err == nil || strings.Contains(err.Error(), "test-secret-key") || !strings.Contains(err.Error(), "手动填写") {
		t.Fatalf("expected sanitized transport error, got %v", err)
	}
}
