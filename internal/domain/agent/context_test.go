package agent

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
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
	messages := []Message{
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

func TestConfigInputBudgetDefaultsAndLimits(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		limit int
		valid bool
	}{{"default", 0, true}, {"minimum", 8 << 10, true}, {"too small", 8191, false}, {"maximum", 2 << 20, true}, {"too large", (2 << 20) + 1, false}} {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Name: "test", Model: "fixture", Kind: "api", Protocol: "openai-chat", BaseURL: "https://example.org/v1", MaxInputBytes: tt.limit}
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
