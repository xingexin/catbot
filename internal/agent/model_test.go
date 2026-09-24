package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentTest/internal/domain"
)

func discard(string, map[string]any) error { return nil }
func TestStreamingProtocols(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, stream, text, args string }{
		{"openai-chat", `data: {"choices":[{"delta":{"content":"你好"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"example__echo","arguments":"{\"te"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"xt\":\"值\"}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`, "你好", `{"text":"值"}`},
		{"openai-responses", `data: {"type":"response.completed","response":{"status":"completed","usage":{"output_tokens":3},"output":[{"type":"message","content":[{"type":"output_text","text":"完成"}]},{"type":"function_call","call_id":"c1","name":"example__echo","arguments":"{\"text\":\"值\"}"}]}}

`, "完成", `{"text":"值"}`},
		{"anthropic", `data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"c1","name":"example__echo","input":{}}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"text\":"}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"值\"}"}}

data: {"type":"message_stop"}

`, "你好", `{"text":"值"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var delta strings.Builder
			turn, err := parseStream(strings.NewReader(tt.stream), tt.name, func(typ string, d map[string]any) error {
				if typ == "text.delta" {
					delta.WriteString(d["text"].(string))
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if turn.Text != tt.text || delta.String() != tt.text {
				t.Fatalf("text %q / deltas %q", turn.Text, delta.String())
			}
			if len(turn.Calls) != 1 || turn.Calls[0].Arguments != tt.args {
				t.Fatalf("calls: %#v", turn.Calls)
			}
			if tt.name == "anthropic" && object(turn.Raw[1])["input"].(map[string]any)["text"] != "值" {
				t.Fatal("tool input missing from continuation")
			}
		})
	}
}
func TestStreamFailures(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, protocol, stream string }{
		{"cut", "openai-chat", `data: {"choices":[{"delta":{"content":"partial"}}]}`},
		{"limit", "openai-chat", `data: {"choices":[{"delta":{},"finish_reason":"length"}]}`},
		{"anthropic-limit", "anthropic", "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"bad-json", "openai-chat", "data: {no}\n\n"},
		{"response-incomplete", "openai-responses", `data: {"type":"response.incomplete"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseStream(strings.NewReader(tt.stream), tt.protocol, discard); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

type callFunc func(context.Context, string, string, map[string]any, string) (any, error)

func (f callFunc) Call(c context.Context, r, n string, a map[string]any, id string) (any, error) {
	return f(c, r, n, a, id)
}
func TestDirectLoopProtocols(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			requests := 0
			toolCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				switch protocol {
				case "anthropic":
					if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "test-key" {
						t.Error("bad anthropic request")
					}
				case "openai-responses":
					if r.URL.Path != "/v1/responses" {
						t.Error("bad responses path")
					}
				case "openai-chat":
					if r.URL.Path != "/v1/chat/completions" {
						t.Error("bad chat path")
					}
				}
				if requests == 2 {
					b, _ := json.Marshal(body)
					if !strings.Contains(string(b), "observed-value") {
						t.Error("tool observation missing")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				if protocol == "openai-chat" {
					if requests == 1 {
						fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"call1","type":"function","function":{"name":"example__echo","arguments":"{\"text\":\"hello\"}"}}]},"finish_reason":"tool_calls"}]}`)
					} else {
						fmt.Fprint(w, `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`)
					}
				} else if protocol == "anthropic" {
					if requests == 1 {
						fmt.Fprint(w, `{"content":[{"type":"tool_use","id":"call1","name":"example__echo","input":{"text":"hello"}}],"stop_reason":"tool_use"}`)
					} else {
						fmt.Fprint(w, `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`)
					}
				} else {
					if requests == 1 {
						fmt.Fprint(w, `{"status":"completed","output":[{"type":"function_call","call_id":"call1","name":"example__echo","arguments":"{\"text\":\"hello\"}"}]}`)
					} else {
						fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`)
					}
				}
			}))
			defer server.Close()
			d := Direct{Model: &Model{}, Tools: callFunc(func(_ context.Context, r, n string, args map[string]any, id string) (any, error) {
				toolCalls++
				if args["text"] != "hello" || id != "run:0:0" {
					t.Error("bad tool contract")
				}
				return map[string]any{"text": "observed-value"}, nil
			})}
			config := domain.Config{BaseURL: server.URL + "/v1", Protocol: protocol, Model: "fixture", MaxSteps: 4, MaxTokens: 100, Capabilities: domain.Capabilities{Tools: true}}
			result, err := d.Run(t.Context(), Request{Run: domain.Run{ID: "run", Config: config, Prompt: "test"}, Key: "test-key", Tools: []domain.Tool{{Name: "example__echo", InputSchema: map[string]any{"type": "object"}}}}, discard)
			if err != nil || result.Text != "done" || toolCalls != 1 || requests != 2 {
				t.Fatalf("result %#v, err %v, requests %d tools %d", result, err, requests, toolCalls)
			}
		})
	}
}
func TestRateLimitAndNoUnsafeTransportRetry(t *testing.T) {
	t.Parallel()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()
	_, err := (&Model{}).Step(t.Context(), domain.Config{BaseURL: server.URL, Protocol: "openai-chat"}, "", "", nil, nil, discard)
	if err != nil || attempts != 2 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}
func TestStepLimitAndDisabledTools(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"call","function":{"name":"echo","arguments":"{}"}}]}}]}`)
	}))
	defer server.Close()
	count := 0
	d := Direct{Model: &Model{}, Tools: callFunc(func(context.Context, string, string, map[string]any, string) (any, error) {
		count++
		return nil, fmt.Errorf("fixture tool failure")
	})}
	req := Request{Run: domain.Run{Config: domain.Config{Protocol: "openai-chat", BaseURL: server.URL, MaxSteps: 2, Capabilities: domain.Capabilities{Tools: true}}}, Tools: []domain.Tool{{Name: "echo"}}}
	if _, err := d.Run(t.Context(), req, discard); err == nil || !strings.Contains(err.Error(), "step limit") {
		t.Fatalf("err %v", err)
	}
	if count != 2 {
		t.Fatal(count)
	}
	req.Run.Config.Capabilities.Tools = false
	if _, err := d.Run(t.Context(), req, discard); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err %v", err)
	}
	if count != 2 {
		t.Fatal("disabled tool was called")
	}
}
func TestImageProtocolMapping(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{"anthropic", "openai-chat", "openai-responses"} {
		body := makeBody(domain.Config{Protocol: protocol}, "", []Entry{{Role: "user", Text: "describe", Images: []string{"data:image/jpeg;base64,YQ=="}}}, nil)
		b, _ := json.Marshal(body)
		if !strings.Contains(string(b), "YQ==") {
			t.Fatal("missing image for " + protocol)
		}
	}
}
func TestInvalidAPIConfiguration(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"ftp://example.org", "https://key:secret@example.org", "https://example.org?key=secret"} {
		c := domain.Config{Name: "test", Model: "test", Kind: "api", Protocol: "openai-chat", BaseURL: base}
		if ValidateConfig(&c) == nil {
			t.Fatal("accepted unsafe URL")
		}
	}
}
