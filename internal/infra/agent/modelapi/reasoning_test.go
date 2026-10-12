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

	"github.com/xingexin/catbot/internal/domain/agent"
)

func TestChatReasoningContentSurvivesMultipleToolRounds(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		stream bool
	}{
		{name: "JSON"},
		{name: "SSE", stream: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				round := int(requests.Add(1))
				var body struct {
					Model    string           `json:"model"`
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if body.Model != "unlisted-compatible-model" {
					t.Error("compatibility must not depend on model name")
				}
				var reasoning []string
				var calls, results []string
				for _, message := range body.Messages {
					if str(message["role"]) == "assistant" {
						value, exists := message["reasoning_content"]
						if !exists {
							t.Error("tool continuation lost reasoning_content")
						}
						reasoning = append(reasoning, str(value))
						for _, call := range array(message["tool_calls"]) {
							calls = append(calls, str(object(call)["id"]))
						}
					} else if _, exists := message["reasoning_content"]; exists {
						t.Error("reasoning_content escaped onto a non-assistant message")
					}
					if str(message["role"]) == "tool" {
						results = append(results, str(message["tool_call_id"]))
					}
				}
				wantReasoning := []string(nil)
				wantCalls := []string(nil)
				if round >= 2 {
					wantReasoning = []string{"opaque-a雪"}
					wantCalls = []string{"call-a", "call-b"}
				}
				if round >= 3 {
					wantReasoning = append(wantReasoning, "opaque-b")
					wantCalls = append(wantCalls, "call-c")
				}
				if !reflect.DeepEqual(reasoning, wantReasoning) || !reflect.DeepEqual(calls, wantCalls) || !reflect.DeepEqual(results, wantCalls) {
					t.Errorf("round %d: reasoning=%v calls=%v results=%v", round, reasoning, calls, results)
				}
				var ids []string
				reason := "opaque-final"
				content := "visible final answer"
				if round == 1 {
					ids, reason, content = []string{"call-a", "call-b"}, "opaque-a雪", ""
				} else if round == 2 {
					ids, reason, content = []string{"call-c"}, "opaque-b", ""
				}
				finish := "stop"
				if len(ids) > 0 {
					finish = "tool_calls"
				}
				if !tt.stream {
					toolCalls := []any{}
					for _, id := range ids {
						toolCalls = append(toolCalls, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": "echo", "arguments": `{"value":"ok"}`}})
					}
					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]any{"content": content, "reasoning_content": reason, "tool_calls": toolCalls}}}}); err != nil {
						t.Error(err)
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				writeReasoningChunk(t, w, map[string]any{"reasoning_content": reason[:3]}, "")
				writeReasoningChunk(t, w, map[string]any{"reasoning_content": reason[3:]}, "")
				for index, id := range ids {
					writeReasoningChunk(t, w, map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": id, "type": "function", "function": map[string]any{"name": "echo", "arguments": `{"value":`}}}}, "")
					writeReasoningChunk(t, w, map[string]any{"tool_calls": []any{map[string]any{"index": index, "function": map[string]any{"arguments": `"ok"}`}}}}, "")
				}
				writeReasoningChunk(t, w, map[string]any{"content": content}, finish)
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			var operations []string
			direct := agent.Direct{Model: &Model{}, Tools: callFunc(func(_ context.Context, run, name string, args map[string]any, operation string) (any, error) {
				if run != "reasoning-run" || name != "echo" || args["value"] != "ok" {
					t.Error("tool invocation changed")
				}
				operations = append(operations, operation)
				return map[string]any{"ok": true}, nil
			})}
			visible := ""
			result, err := direct.Run(t.Context(), agent.Request{
				RunID: "reasoning-run", Prompt: "perform the task",
				Config: agent.Config{Protocol: "openai-chat", BaseURL: server.URL, Model: "unlisted-compatible-model", MaxSteps: 3, Capabilities: agent.Capabilities{Tools: true, Stream: tt.stream}},
				Tools:  []agent.Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}},
			}, func(kind string, data map[string]any) error {
				encoded, err := json.Marshal(data)
				if err != nil {
					return err
				}
				if strings.Contains(string(encoded), "opaque-") {
					t.Error("provider reasoning leaked into user-facing execution events")
				}
				if kind == "text.delta" {
					visible += str(data["text"])
				}
				return nil
			})
			wantOperations := []string{"reasoning-run:0:0", "reasoning-run:0:1", "reasoning-run:1:0"}
			if err != nil || result.Text != "visible final answer" || visible != result.Text || requests.Load() != 3 || !reflect.DeepEqual(operations, wantOperations) {
				t.Fatalf("result=%#v visible=%q operations=%v requests=%d error=%v", result, visible, operations, requests.Load(), err)
			}
		})
	}
}

func writeReasoningChunk(t *testing.T, w http.ResponseWriter, delta map[string]any, finish string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	if err != nil {
		t.Error(err)
		return
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		t.Error(err)
	}
}

func TestChatReasoningContentOptionalFieldContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		value   any
		include bool
		present bool
	}{
		{name: "absent"},
		{name: "null", include: true},
		{name: "explicit empty", include: true, value: "", present: true},
		{name: "text", include: true, value: "opaque-state", present: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			message := map[string]any{"content": "answer"}
			if tt.include {
				message["reasoning_content"] = tt.value
			}
			turn, err := parseJSON(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "stop"}}}, "openai-chat")
			if err != nil || (turn.ReasoningContent != nil) != tt.present || turn.Text != "answer" {
				t.Fatalf("turn=%#v err=%v", turn, err)
			}
			body := makeBody(agent.Config{Protocol: "openai-chat"}, "system", []agent.Entry{{Role: "assistant", Text: turn.Text, ReasoningContent: turn.ReasoningContent}}, nil)
			messages := body["messages"].([]any)
			output := messages[1].(map[string]any)
			value, present := output["reasoning_content"]
			if present != tt.present || (present && value != tt.value) {
				t.Fatalf("reasoning presence/value changed: %#v", output)
			}
		})
	}
}

func TestChatReasoningContentDoesNotReplaceFinalText(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, body string
		stream     bool
	}{
		{name: "JSON", body: `{"choices":[{"message":{"reasoning_content":"opaque-state"},"finish_reason":"stop"}]}`},
		{name: "SSE", stream: true, body: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"opaque-state\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var turn agent.Turn
			var err error
			if tt.stream {
				turn, err = parseStream(strings.NewReader(tt.body), "openai-chat", func(string, map[string]any) error { t.Error("reasoning emitted as text"); return nil })
			} else {
				var data map[string]any
				if err := json.Unmarshal([]byte(tt.body), &data); err != nil {
					t.Fatal(err)
				}
				turn, err = parseJSON(data, "openai-chat")
			}
			if err == nil || turn.Text != "" || !strings.Contains(err.Error(), "no text") {
				t.Fatalf("reasoning-only output treated as final: turn=%#v err=%v", turn, err)
			}
		})
	}
}

func TestChatReasoningContentSizeAndFormatAreBounded(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		value any
		want  string
	}{
		{name: "non-string", value: map[string]any{"opaque": true}, want: "must be a string"},
		{name: "oversized", value: strings.Repeat("a", (256<<10)+1), want: "too large"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			message := map[string]any{"reasoning_content": tt.value, "content": "answer"}
			data := map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "stop"}}}
			if _, err := parseJSON(data, "openai-chat"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("JSON reasoning field was not rejected: %v", err)
			}
			chunk, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": message, "finish_reason": "stop"}}})
			if err != nil {
				t.Fatal(err)
			}
			stream := "data: " + string(chunk) + "\n\ndata: [DONE]\n\n"
			if _, err := parseStream(strings.NewReader(stream), "openai-chat", discard); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SSE reasoning field was not rejected: %v", err)
			}
		})
	}
}

func TestChatReasoningContentCountsTowardInputBudget(t *testing.T) {
	t.Parallel()
	reasoning := strings.Repeat("opaque-state", 800)
	config := agent.Config{Protocol: "openai-chat", MaxInputBytes: 8 << 10}
	current := []agent.Entry{
		{Role: "user", Text: "run a tool"},
		{Role: "assistant", ReasoningContent: &reasoning, Calls: []agent.Call{{ID: "call", Name: "echo", Arguments: "{}"}}},
		{Role: "tool", CallID: "call", Text: `{"ok":true}`},
	}
	_, _, _, err := agent.FitHistory(&Model{}, config, "system", nil, nil, current, nil)
	var budget *agent.ContextBudgetError
	if !errors.As(err, &budget) || !budget.HasTools {
		t.Fatalf("opaque reasoning silently omitted from input budget: %v", err)
	}
	if *current[1].ReasoningContent != reasoning {
		t.Fatal("current execution reasoning was truncated")
	}
}

func TestStreamReasoningContentOptionalFieldContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, delta string
		present     bool
	}{
		{name: "absent", delta: `{}`},
		{name: "null", delta: `{"reasoning_content":null}`},
		{name: "explicit empty", delta: `{"reasoning_content":""}`, present: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stream := "data: {\"choices\":[{\"delta\":" + tt.delta + "}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			visible := ""
			turn, err := parseStream(strings.NewReader(stream), "openai-chat", func(kind string, data map[string]any) error {
				if kind != "text.delta" {
					t.Errorf("unexpected visible event: %s", kind)
				}
				visible += str(data["text"])
				return nil
			})
			if err != nil || (turn.ReasoningContent != nil) != tt.present || turn.Text != "answer" || visible != "answer" {
				t.Fatalf("turn=%#v visible=%q err=%v", turn, visible, err)
			}
			if turn.ReasoningContent != nil && *turn.ReasoningContent != "" {
				t.Fatal("explicit empty reasoning field changed")
			}
		})
	}
}

func TestStreamReasoningContentLimitIncludesAllChunks(t *testing.T) {
	t.Parallel()
	chunk, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": strings.Repeat("a", 128<<10)}}}})
	if err != nil {
		t.Fatal(err)
	}
	stream := strings.Repeat("data: "+string(chunk)+"\n\n", 3) + "data: [DONE]\n\n"
	_, err = parseStream(strings.NewReader(stream), "openai-chat", func(string, map[string]any) error { t.Error("reasoning emitted"); return nil })
	if err == nil || !strings.Contains(err.Error(), "reasoning content too large") {
		t.Fatalf("accumulated reasoning exceeded its bound: %v", err)
	}
}
