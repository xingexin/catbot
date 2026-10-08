package agent

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xingexin/catbot/internal/domain"
)

func TestToolObservationIsBoundedJSON(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{256, 1024, 64 << 10} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			value := map[string]any{"body": strings.Repeat("中文\"\n😀", 30000)}
			encoded, observed, err := ToolObservation(value, limit)
			if err != nil || !json.Valid([]byte(encoded)) || !utf8.ValidString(encoded) || len(encoded) > limit {
				t.Fatalf("invalid bounded observation: length=%d limit=%d error=%v", len(encoded), limit, err)
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(encoded), &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed["truncated"] != true || observed.(map[string]any)["truncated"] != true {
				t.Fatal("truncated output was not identified")
			}
			preview := parsed["preview"].(string)
			if strings.Contains(preview, "�") || len(preview) == 0 {
				t.Fatal("preview lost UTF-8 data")
			}
			if _, ok := value["truncated"]; ok {
				t.Fatal("caller-owned value was mutated")
			}
		})
	}
	encoded, value, err := ToolObservation(map[string]any{"done": true}, 256)
	if err != nil || encoded != `{"done":true}` || value.(map[string]any)["done"] != true {
		t.Fatalf("small result changed: %q %#v %v", encoded, value, err)
	}
	if _, _, err := ToolObservation(math.NaN(), 256); err == nil {
		t.Fatal("invalid JSON result was accepted")
	}
}

func TestCompactPreservesTurnsAndBudget(t *testing.T) {
	t.Parallel()
	messages := []domain.Message{
		{Role: "user", Content: strings.Repeat("old question 中文", 200)},
		{Role: "assistant", Content: strings.Repeat("old answer", 200)},
		{Role: "user", Content: "remember the latest question"},
		{Role: "assistant", Content: "remember the latest answer"},
	}
	const limit = 1024
	history, excerpts := Compact(messages, limit)
	if len(history) != 2 || history[0] != messages[2] || history[1] != messages[3] {
		t.Fatalf("conversation turn was split: %#v", history)
	}
	if !strings.Contains(excerpts, "not complete history") || !utf8.ValidString(excerpts) {
		t.Fatalf("invalid excerpts: %q", excerpts)
	}
	size := len(excerpts)
	for _, msg := range history {
		size += messageSize(msg)
	}
	if size > limit {
		t.Fatalf("history and excerpts exceed budget: %d", size)
	}
	history[0].Content = "changed"
	if messages[2].Content == "changed" {
		t.Fatal("history aliases session storage")
	}
	all, summary := Compact(messages, 100000)
	if len(all) != len(messages) || summary != "" {
		t.Fatal("history was unnecessarily compacted")
	}
	if history, summary := Compact(messages, 0); len(history) != 0 || summary != "" {
		t.Fatal("zero budget was ignored")
	}
}

func TestInputBudgetCountsProtocolAndPreservesCurrentToolChain(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			config := domain.Config{Protocol: protocol, MaxInputBytes: 8 << 10}
			examples := []Entry{{Role: "user", Text: "example question"}, {Role: "assistant", Text: "example answer"}}
			history := []domain.Message{
				{Role: "user", Content: strings.Repeat("discard old question", 600)},
				{Role: "assistant", Content: "discard this answer too"},
				{Role: "user", Content: "recent question"},
				{Role: "assistant", Content: "recent answer"},
			}
			current := []Entry{
				{Role: "user", Text: "do not truncate this request"},
				{Role: "assistant", Calls: []Call{{ID: "call-1", Name: "example__echo", Arguments: `{"text":"test"}`}}},
				{Role: "tool", CallID: "call-1", Text: `{"done":true}`},
			}
			tools := []domain.Tool{{Name: "example__echo", InputSchema: map[string]any{"type": "object"}}}
			entries, remaining, size, err := fitHistory(config, "keep system persona", examples, history, current, tools)
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
			_, _, _, err = fitHistory(config, "system", examples, nil, current, tools)
			var budgetErr *ContextBudgetError
			if !errors.As(err, &budgetErr) || !budgetErr.HasTools {
				t.Fatalf("current tool chain silently truncated: %v", err)
			}
			current = current[:1]
			tools[0].Description = strings.Repeat("large tool description", 1000)
			_, _, _, err = fitHistory(config, "system", examples, nil, current, tools)
			if !errors.As(err, &budgetErr) {
				t.Fatal("tool declarations were not budgeted")
			}
		})
	}
}

func TestConfigInputBudgetDefaultsAndLimits(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		limit int
		valid bool
	}{{"default", 0, true}, {"minimum", 8 << 10, true}, {"too small", 8191, false}, {"maximum", 2 << 20, true}, {"too large", (2 << 20) + 1, false}} {
		t.Run(tt.name, func(t *testing.T) {
			c := domain.Config{Name: "test", Model: "fixture", Kind: "api", Protocol: "openai-chat", BaseURL: "https://example.org/v1", MaxInputBytes: tt.limit}
			err := ValidateConfig(&c)
			if (err == nil) != tt.valid {
				t.Fatalf("limit %d error %v", tt.limit, err)
			}
			if tt.limit == 0 && c.MaxInputBytes != DefaultMaxInputBytes {
				t.Fatal("default budget was not applied")
			}
		})
	}
}
