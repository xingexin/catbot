package modelapi

import (
	"encoding/json"
	"errors"
	"github.com/xingexin/catbot/internal/domain/agent"
	"strings"
	"testing"
)

func TestInputBudgetCountsProtocolAndPreservesCurrentToolChain(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			config := agent.Config{Protocol: protocol, MaxInputBytes: 8 << 10}
			examples := []agent.Entry{{Role: "user", Text: "example question"}, {Role: "assistant", Text: "example answer"}}
			history := []agent.Message{
				{Role: "user", Content: strings.Repeat("discard old question", 600)},
				{Role: "assistant", Content: "discard this answer too"},
				{Role: "user", Content: "recent question"},
				{Role: "assistant", Content: "recent answer"},
			}
			current := []agent.Entry{
				{Role: "user", Text: "do not truncate this request"},
				{Role: "assistant", Calls: []agent.Call{{ID: "call-1", Name: "example__echo", Arguments: `{"text":"test"}`}}},
				{Role: "tool", CallID: "call-1", Text: `{"done":true}`},
			}
			tools := []agent.Tool{{Name: "example__echo", InputSchema: map[string]any{"type": "object"}}}
			entries, remaining, size, err := agent.FitHistory(&Model{}, config, "keep system persona", examples, history, current, tools)
			if err != nil || len(remaining) != 2 || size > config.MaxInputBytes {
				t.Fatalf("budget failed: remaining=%d bytes=%d err=%v", len(remaining), size, err)
			}
			payload, err := json.Marshal(makeBody(config, "keep system persona", entries, tools))
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range []string{"keep system persona", "example answer", "recent question", "recent answer", "do not truncate this request", "call-1", "done"} {
				if !strings.Contains(string(payload), fragment) {
					t.Fatalf("lost required context: %s", fragment)
				}
			}
			if strings.Contains(string(payload), "discard") {
				t.Fatal("old conversation turn was partly retained")
			}
			if len(payload) != size {
				t.Fatal("budget did not count the serialized protocol request")
			}
			current[2].Text = strings.Repeat("must not forget completed action", 1000)
			_, _, _, err = agent.FitHistory(&Model{}, config, "system", examples, nil, current, tools)
			var budgetErr *agent.ContextBudgetError
			if !errors.As(err, &budgetErr) || !budgetErr.HasTools {
				t.Fatalf("current tool chain silently truncated: %v", err)
			}
			current = current[:1]
			tools[0].Description = strings.Repeat("large tool description", 1000)
			_, _, _, err = agent.FitHistory(&Model{}, config, "system", examples, nil, current, tools)
			if !errors.As(err, &budgetErr) {
				t.Fatal("tool declarations were not budgeted")
			}
		})
	}
}
