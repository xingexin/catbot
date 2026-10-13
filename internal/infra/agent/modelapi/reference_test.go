package modelapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
)

func TestAllAPIProtocolsReceiveGroupReferenceInCurrentUserTurn(t *testing.T) {
	for _, tc := range []struct{ protocol, response string }{
		{"openai-chat", `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`},
		{"openai-responses", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`},
		{"anthropic", `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			received := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input map[string]any
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				received <- input
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			direct := agent.Direct{Model: &Model{HTTP: server.Client()}}
			request := agent.Request{Config: agent.Config{Protocol: tc.protocol, BaseURL: server.URL, Model: "fixture", MaxSteps: 1, MaxTokens: 1000}, Prompt: "CURRENT-REQUEST", Context: "COLLECTED-GROUP-MESSAGE", History: []agent.Message{{Role: "user", Content: "PUBLIC-HISTORY"}}, Persona: persona.Persona{SystemPrompt: "PERSONA"}}
			result, err := direct.Run(t.Context(), request, discard)
			if err != nil || result.Text != "done" {
				t.Fatal(result, err)
			}
			wire := <-received
			field := "messages"
			if tc.protocol == "openai-responses" {
				field = "input"
			}
			messages := wire[field].([]any)
			latest := messages[len(messages)-1].(map[string]any)
			encoded, err := json.Marshal(latest)
			if err != nil {
				t.Fatal(err)
			}
			if latest["role"] != "user" || !strings.Contains(string(encoded), "CURRENT-REQUEST") || !strings.Contains(string(encoded), "COLLECTED-GROUP-MESSAGE") || !strings.Contains(string(encoded), "untrusted data") {
				t.Fatalf("current user context missing: %s", encoded)
			}
			if request.Prompt != "CURRENT-REQUEST" || len(request.History) != 1 || request.History[0].Content != "PUBLIC-HISTORY" {
				t.Fatal("direct strategy mutated caller history")
			}
		})
	}
}

func TestGroupReferenceCountsAgainstSerializedAPIBudget(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.Copy(io.Discard, r.Body) }))
	defer server.Close()
	direct := agent.Direct{Model: &Model{HTTP: server.Client()}}
	request := agent.Request{Config: agent.Config{Protocol: "openai-chat", BaseURL: server.URL, Model: "fixture", MaxSteps: 1, MaxInputBytes: 8 << 10}, Prompt: "keep-current-prompt", Context: strings.Repeat("x", 16<<10)}
	_, err := direct.Run(t.Context(), request, discard)
	var budget *agent.ContextBudgetError
	if !errors.As(err, &budget) || calls != 0 {
		t.Fatalf("context bypassed serialized budget: %v calls=%d", err, calls)
	}
}
