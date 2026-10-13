package sdkbridge

import (
	"encoding/json"
	"fmt"
	"github.com/xingexin/catbot/internal/domain/agent"
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
	result, err := b.Run(t.Context(), agent.Request{RunID: "one"}, discard)
	if err == nil || !strings.Contains(err.Error(), "interrupted") || result.NativeID != "native-one" {
		t.Fatal(result, err)
	}
	if calls != 1 {
		t.Fatal("unsafe SDK retry")
	}
}

func TestSDKBridgeKeepsTurnContextSeparateOnNativeResume(t *testing.T) {
	t.Parallel()
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		received <- input
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"completed\",\"data\":{\"text\":\"done\"}}\n\n")
	}))
	defer server.Close()
	b := Bridge{URL: server.URL, RunToken: func(string) string { return "run-token" }}
	request := agent.Request{RunID: "r", Prompt: "summarize", Context: strings.Repeat("群消息", 6000), NativeID: "existing-native", History: []agent.Message{{Role: "user", Content: "original-history"}}}
	if _, err := b.Run(t.Context(), request, discard); err != nil {
		t.Fatal(err)
	}
	wire := <-received
	if wire["prompt"] != request.Prompt || wire["nativeId"] != request.NativeID || wire["context"] != agent.BoundReference(request.Context) {
		t.Fatal("bridge lost current context or native session")
	}
	history := wire["history"].([]any)
	if history[0].(map[string]any)["content"] != "original-history" {
		t.Fatal("context replaced public history")
	}
}
