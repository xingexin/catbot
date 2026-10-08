package modelapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xingexin/catbot/internal/domain/agent"
)

func TestDirectToolResultsAreBoundedForEveryProtocol(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests++
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Error(err)
				}
				if len(body) > agent.DefaultMaxInputBytes || !utf8.Valid(body) || !json.Valid(body) {
					t.Error("request is oversized or invalid JSON/UTF-8")
				}
				if requests == 2 && !strings.Contains(string(body), `truncated`) {
					t.Error("large result is not marked as truncated")
				}
				w.Header().Set("Content-Type", "application/json")
				var response string
				switch protocol {
				case "openai-chat":
					response = `{"choices":[{"message":{"tool_calls":[{"id":"first","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
					if requests == 2 {
						response = `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`
					}
				case "openai-responses":
					response = `{"status":"completed","output":[{"type":"function_call","call_id":"first","name":"echo","arguments":"{}"}]}`
					if requests == 2 {
						response = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`
					}
				case "anthropic":
					response = `{"content":[{"type":"tool_use","id":"first","name":"echo","input":{}}],"stop_reason":"tool_use"}`
					if requests == 2 {
						response = `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`
					}
				}
				fmt.Fprint(w, response)
			}))
			defer server.Close()
			direct := agent.Direct{Model: &Model{}, Tools: callFunc(func(context.Context, string, string, map[string]any, string) (any, error) {
				return map[string]any{"text": strings.Repeat("测试🔔", 30000)}, nil
			})}
			events := 0
			result, err := direct.Run(t.Context(), agent.Request{
				RunID: "run", Prompt: "summarize", Config: agent.Config{Protocol: protocol, BaseURL: server.URL, MaxSteps: 3, Capabilities: agent.Capabilities{Tools: true}},
				Tools: []agent.Tool{{Name: "echo"}},
			}, func(kind string, data map[string]any) error {
				if kind == "tool.completed" {
					events++
					encoded, err := json.Marshal(data["result"])
					if err != nil || len(encoded) > agent.DefaultMaxInputBytes/2 || !json.Valid(encoded) {
						t.Error("tool event result is unbounded")
					}
				}
				return nil
			})
			if err != nil || result.Text != "done" || requests != 2 || events != 1 {
				t.Fatalf("result=%#v err=%v requests=%d events=%d", result, err, requests, events)
			}
		})
	}
}

func TestDirectRejectsInvalidBatchBeforeSideEffects(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, calls string }{
		{"duplicate IDs", `[{"id":"same","function":{"name":"echo","arguments":"{}"}},{"id":"same","function":{"name":"echo","arguments":"{}"}}]`},
		{"unavailable second tool", `[{"id":"one","function":{"name":"echo","arguments":"{}"}},{"id":"two","function":{"name":"unavailable","arguments":"{}"}}]`},
		{"missing second ID", `[{"id":"one","function":{"name":"echo","arguments":"{}"}},{"function":{"name":"echo","arguments":"{}"}}]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":`+tt.calls+`},"finish_reason":"tool_calls"}]}`)
			}))
			defer server.Close()
			calls := 0
			direct := agent.Direct{Model: &Model{}, Tools: callFunc(func(context.Context, string, string, map[string]any, string) (any, error) {
				calls++
				return true, nil
			})}
			_, err := direct.Run(t.Context(), agent.Request{Config: agent.Config{Protocol: "openai-chat", BaseURL: server.URL, MaxSteps: 2, Capabilities: agent.Capabilities{Tools: true}}, Tools: []agent.Tool{{Name: "echo"}}}, discard)
			if err == nil || calls != 0 {
				t.Fatalf("invalid batch produced side effects: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestDirectCancellationStopsRemainingTools(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"one","function":{"name":"echo","arguments":"{}"}},{"id":"two","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	direct := agent.Direct{Model: &Model{}, Tools: callFunc(func(context.Context, string, string, map[string]any, string) (any, error) {
		calls++
		cancel()
		return true, nil
	})}
	_, err := direct.Run(ctx, agent.Request{Config: agent.Config{Protocol: "openai-chat", BaseURL: server.URL, MaxSteps: 2, Capabilities: agent.Capabilities{Tools: true}}, Tools: []agent.Tool{{Name: "echo"}}}, discard)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read after terminal event") }

func TestStreamsReturnOnTerminalEvent(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ protocol, stream string }{
		{"openai-chat", "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{"openai-responses", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n"},
		{"anthropic", "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"done\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
	} {
		t.Run(tt.protocol, func(t *testing.T) {
			turn, err := parseStream(io.MultiReader(strings.NewReader(tt.stream), errorReader{}), tt.protocol, discard)
			if err != nil || turn.Text != "done" {
				t.Fatalf("terminal event not honored: turn=%#v err=%v", turn, err)
			}
		})
	}
}

func TestJSONAndStreamLimitsMatch(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, protocol, payload string }{
		{"empty", "openai-chat", `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`},
		{"filtered", "openai-chat", `{"choices":[{"message":{"content":"partial"},"finish_reason":"content_filter"}]}`},
		{"huge text", "openai-chat", `{"choices":[{"message":{"content":"` + strings.Repeat("x", (256<<10)+1) + `"}}]}`},
		{"huge args", "openai-chat", `{"choices":[{"message":{"tool_calls":[{"id":"one","function":{"name":"echo","arguments":"` + strings.Repeat("x", (128<<10)+1) + `"}}]}}]}`},
		{"empty responses", "openai-responses", `{"status":"completed","output":[]}`},
		{"paused anthropic", "anthropic", `{"content":[{"type":"text","text":"working"}],"stop_reason":"pause_turn"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var data map[string]any
			if err := json.Unmarshal([]byte(tt.payload), &data); err != nil {
				t.Fatal(err)
			}
			if _, err := parseJSON(data, tt.protocol); err == nil {
				t.Fatal("invalid response was accepted")
			}
		})
	}
}

func TestAnthropicToolErrorMetadata(t *testing.T) {
	t.Parallel()
	body := makeBody(agent.Config{Protocol: "anthropic"}, "", []agent.Entry{{Role: "tool", CallID: "failed", Text: `{"error":"invalid arguments"}`, IsError: true}}, nil)
	messages := body["messages"].([]any)
	result := object(array(object(messages[0])["content"])[0])
	if result["is_error"] != true || result["tool_use_id"] != "failed" {
		t.Fatalf("tool error metadata lost: %#v", result)
	}
}
