package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentTest/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type tokenTransport struct{ token string }

func (t tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(clone)
}
func TestMCPGatewayUsesRunScopedPermissions(t *testing.T) {
	a := testApp(t)
	run := domain.Run{ID: "sdk-run", Status: "running", Config: domain.Config{Kind: "sdk", Capabilities: domain.Capabilities{Tools: true}}, Persona: domain.Persona{Tools: []string{"system__task_list"}}, Versions: map[string]string{}}
	if err := a.Store.Put(t.Context(), "run", run.ID, run); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "sdk-contract-fixture", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/internal/mcp", HTTPClient: &http.Client{Transport: tokenTransport{a.Token("run:" + run.ID)}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "system__task_list" {
		t.Fatal("persona restrictions lost", tools)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "system__task_list", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "system__task_create", Arguments: map[string]any{}}); err == nil {
		t.Fatal("unauthorized tool accepted")
	}
	run.Status = "completed"
	_ = a.Store.Put(t.Context(), "run", run.ID, run)
	if _, err := session.ListTools(t.Context(), nil); err == nil {
		t.Fatal("completed run token remained active")
	}
}
func TestQQUncertainDeliveryIsRecordedWithoutResend(t *testing.T) {
	a := testApp(t)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Header.Get("Authorization") != "QQBot fixture-token" {
			t.Error("bad QQ auth")
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	registerTestOfficial(t, a, server.URL)
	_ = a.Vault.Set(t.Context(), "qq-test", "fixture", "fixture-token")
	_ = a.Store.Put(t.Context(), "qq-access", "current", qqAccess{ID: "qq-test", Expires: time.Now().Add(time.Hour)})
	_ = a.Store.Put(t.Context(), "session", "qq", domain.Session{ID: "qq", Channel: "qq", Recipient: "bound-user"})
	if err := a.SendMessage(t.Context(), "qq", "hello", "stable-delivery"); err == nil {
		t.Fatal("broken connection accepted")
	}
	var d delivery
	if err := a.Store.Get(t.Context(), "delivery", "stable-delivery", &d); err != nil {
		t.Fatal(err)
	}
	if d.Status != "uncertain" {
		t.Fatal(d)
	}
	if err := a.SendMessage(t.Context(), "qq", "hello", "stable-delivery"); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatal("potential duplicate send", count.Load())
	}
}
