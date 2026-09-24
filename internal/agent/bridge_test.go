package agent

import (
	"agentTest/internal/domain"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSDKBridgePreservesInterruptedSessionWithoutReplay(t *testing.T) {
	t.Parallel()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer service-token" {
			t.Error("missing internal auth")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"native.session\",\"data\":{\"id\":\"native-one\"}}\n\ndata: {\"type\":\"text.delta\",\"data\":{\"text\":\"partial\"}}\n\n")
	}))
	defer server.Close()
	b := Bridge{URL: server.URL, Token: "service-token", GatewayURL: "http://core/internal/mcp", RunToken: func(string) string { return "run-token" }}
	result, err := b.Run(t.Context(), Request{Run: domain.Run{ID: "one"}}, discard)
	if err == nil || !strings.Contains(err.Error(), "interrupted") || result.NativeID != "native-one" {
		t.Fatal(result, err)
	}
	if calls != 1 {
		t.Fatal("unsafe SDK retry")
	}
}
