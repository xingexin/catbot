package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentTest/internal/domain"
)

type Call struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type Entry struct {
	Role, Text, CallID string
	IsError            bool
	Images             []string
	Calls              []Call
	Raw                []any
}
type Turn struct {
	Text  string
	Calls []Call
	Raw   []any
	Usage map[string]int
}
type Emit func(string, map[string]any) error
type Model struct{ HTTP *http.Client }
type HTTPError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string { return fmt.Sprintf("model endpoint returned HTTP %d", e.Status) }

func ValidateConfig(c *domain.Config) error {
	if c.Name == "" || c.Model == "" {
		return errors.New("name and model are required")
	}
	if c.Kind != "sdk" && c.Kind != "api" {
		return errors.New("kind must be sdk or api")
	}
	if c.Kind == "sdk" {
		if c.Provider != "codebuddy" && c.Provider != "claude" && c.Provider != "codex" {
			return errors.New("unsupported agent SDK")
		}
		if c.Capabilities.Images {
			return errors.New("Agent SDK image input is not supported; use an image-capable API configuration")
		}
		// Tools is an administrator opt-in, unlike the SDK's fixed transport
		// capabilities. Preserve an explicit opt-out when validating a save.
		c.Capabilities.Stream = true
		c.Capabilities.Resume = true
	} else {
		switch c.Protocol {
		case "openai-chat", "openai-responses", "anthropic":
		default:
			return errors.New("unsupported API protocol")
		}
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("baseUrl must be an HTTP(S) URL without credentials, query or fragment")
		}
	} else if c.Kind == "api" {
		return errors.New("baseUrl is required")
	}
	if c.MaxSteps == 0 {
		c.MaxSteps = 12
	}
	if c.MaxSteps < 1 || c.MaxSteps > 50 {
		return errors.New("maxSteps must be 1..50")
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 4096
	}
	if c.MaxTokens < 1 || c.MaxTokens > 32768 {
		return errors.New("maxTokens must be 1..32768")
	}
	if c.MaxInputBytes == 0 {
		c.MaxInputBytes = DefaultMaxInputBytes
	}
	if c.MaxInputBytes < 8<<10 || c.MaxInputBytes > 2<<20 {
		return errors.New("maxInputBytes must be 8192..2097152 (serialized API conversation bytes, not tokens)")
	}
	if c.TimeoutSec == 0 {
		c.TimeoutSec = 180
	}
	if c.TimeoutSec < 1 || c.TimeoutSec > 3600 {
		return errors.New("timeoutSec must be 1..3600")
	}
	return nil
}

func endpoint(base, protocol string) string {
	base = strings.TrimRight(base, "/")
	switch protocol {
	case "openai-chat":
		return base + "/chat/completions"
	case "openai-responses":
		return base + "/responses"
	default:
		if strings.HasSuffix(base, "/v1") {
			return base + "/messages"
		}
		return base + "/v1/messages"
	}
}
func makeBody(c domain.Config, system string, h []Entry, tools []domain.Tool) map[string]any {
	body := map[string]any{"model": c.Model, "stream": c.Capabilities.Stream}
	if c.Protocol == "anthropic" {
		body["system"] = system
		body["max_tokens"] = c.MaxTokens
		msgs := []any{}
		for _, e := range h {
			if e.Role == "tool" {
				msgs = append(msgs, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": e.CallID, "content": e.Text, "is_error": e.IsError}}})
				continue
			}
			blocks := e.Raw
			if len(blocks) == 0 {
				blocks = []any{}
				if e.Text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": e.Text})
				}
				for _, img := range e.Images {
					header, data, _ := strings.Cut(img, ",")
					mime := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
					blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mime, "data": data}})
				}
				for _, call := range e.Calls {
					var args any
					_ = json.Unmarshal([]byte(call.Arguments), &args)
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": args})
				}
			}
			msgs = append(msgs, map[string]any{"role": e.Role, "content": blocks})
		}
		body["messages"] = msgs
		ts := []any{}
		for _, t := range tools {
			ts = append(ts, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.InputSchema})
		}
		if len(ts) > 0 {
			body["tools"] = ts
		}
		return body
	}
	if c.Protocol == "openai-responses" {
		body["instructions"] = system
		body["max_output_tokens"] = c.MaxTokens
		body["store"] = false
		input := []any{}
		for _, e := range h {
			if e.Role == "tool" {
				input = append(input, map[string]any{"type": "function_call_output", "call_id": e.CallID, "output": e.Text})
				continue
			}
			if len(e.Raw) > 0 {
				input = append(input, e.Raw...)
				continue
			}
			if len(e.Images) > 0 {
				content := []any{map[string]any{"type": "input_text", "text": e.Text}}
				for _, img := range e.Images {
					content = append(content, map[string]any{"type": "input_image", "image_url": img})
				}
				input = append(input, map[string]any{"role": e.Role, "content": content})
			} else if e.Text != "" {
				input = append(input, map[string]any{"role": e.Role, "content": e.Text})
			}
			for _, call := range e.Calls {
				input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments})
			}
		}
		body["input"] = input
		ts := []any{}
		for _, t := range tools {
			ts = append(ts, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.InputSchema, "strict": false})
		}
		if len(ts) > 0 {
			body["tools"] = ts
		}
		return body
	}
	body["max_tokens"] = c.MaxTokens
	msgs := []any{map[string]any{"role": "system", "content": system}}
	for _, e := range h {
		m := map[string]any{"role": e.Role, "content": e.Text}
		if len(e.Images) > 0 {
			content := []any{map[string]any{"type": "text", "text": e.Text}}
			for _, img := range e.Images {
				content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": img}})
			}
			m["content"] = content
		}
		if e.Role == "tool" {
			m["tool_call_id"] = e.CallID
		}
		if len(e.Calls) > 0 {
			calls := []any{}
			for _, call := range e.Calls {
				calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": call.Arguments}})
			}
			m["tool_calls"] = calls
		}
		msgs = append(msgs, m)
	}
	body["messages"] = msgs
	if c.Capabilities.Stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	ts := []any{}
	for _, t := range tools {
		ts = append(ts, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.InputSchema}})
	}
	if len(ts) > 0 {
		body["tools"] = ts
	}
	return body
}

func (m *Model) Step(ctx context.Context, c domain.Config, key, system string, h []Entry, tools []domain.Tool, emit Emit) (Turn, error) {
	b, err := json.Marshal(makeBody(c, system, h, tools))
	if err != nil {
		return Turn{}, err
	}
	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 180 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(c.BaseURL, c.Protocol), bytes.NewReader(b))
		if err != nil {
			return Turn{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream, application/json")
		if c.Protocol == "anthropic" {
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err = client.Do(req)
		if err != nil {
			return Turn{}, fmt.Errorf("model request failed: %w", err)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			break
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 429 || attempt == 2 {
			return Turn{}, &HTTPError{Status: resp.StatusCode}
		}
		delay := time.Second * time.Duration(1<<attempt)
		if secs, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil {
			delay = min(time.Duration(secs)*time.Second, 30*time.Second)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Turn{}, ctx.Err()
		case <-timer.C:
		}
	}
	defer resp.Body.Close()
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return parseStream(resp.Body, c.Protocol, emit)
	}
	dataBytes, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return Turn{}, fmt.Errorf("read model response: %w", err)
	}
	if len(dataBytes) > 8<<20 {
		return Turn{}, errors.New("model response exceeds 8 MiB")
	}
	var data map[string]any
	if err := json.Unmarshal(dataBytes, &data); err != nil {
		return Turn{}, fmt.Errorf("decode model response: %w", err)
	}
	turn, err := parseJSON(data, c.Protocol)
	if err == nil && turn.Text != "" {
		err = emit("text.delta", map[string]any{"text": turn.Text})
	}
	return turn, err
}

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any           { a, _ := v.([]any); return a }
func str(v any) string            { s, _ := v.(string); return s }
func number(v any) int            { n, _ := v.(float64); return int(n) }
func usage(v any) map[string]int {
	out := map[string]int{}
	for k, v := range object(v) {
		if n, ok := v.(float64); ok {
			out[k] = int(n)
		}
	}
	return out
}

func parseJSON(data map[string]any, protocol string) (Turn, error) {
	t := Turn{Usage: usage(data["usage"])}
	if data["error"] != nil {
		return t, errors.New("model returned an error")
	}
	switch protocol {
	case "openai-chat":
		choices := array(data["choices"])
		if len(choices) == 0 {
			return t, errors.New("model response has no choices")
		}
		ch := object(choices[0])
		if str(ch["finish_reason"]) == "length" {
			return t, errors.New("model output token limit reached")
		}
		if str(ch["finish_reason"]) == "content_filter" {
			return t, errors.New("model output was stopped by the provider content filter")
		}
		msg := object(ch["message"])
		t.Text = str(msg["content"])
		if t.Text == "" {
			t.Text = str(msg["refusal"])
		}
		for _, v := range array(msg["tool_calls"]) {
			x := object(v)
			f := object(x["function"])
			t.Calls = append(t.Calls, Call{str(x["id"]), str(f["name"]), str(f["arguments"])})
		}
	case "openai-responses":
		if s := str(data["status"]); s == "incomplete" || s == "failed" {
			return t, fmt.Errorf("model response %s", s)
		}
		t.Raw = array(data["output"])
		for _, v := range t.Raw {
			x := object(v)
			switch str(x["type"]) {
			case "function_call":
				t.Calls = append(t.Calls, Call{str(x["call_id"]), str(x["name"]), str(x["arguments"])})
			case "message":
				for _, p := range array(x["content"]) {
					part := object(p)
					if str(part["type"]) == "refusal" {
						t.Text += str(part["refusal"])
					} else {
						t.Text += str(part["text"])
					}
				}
			}
		}
	case "anthropic":
		if str(data["stop_reason"]) == "max_tokens" {
			return t, errors.New("model output token limit reached")
		}
		if str(data["stop_reason"]) == "pause_turn" {
			return t, errors.New("model paused a server tool turn; server tools are not supported by this executor")
		}
		t.Raw = array(data["content"])
		for _, v := range t.Raw {
			x := object(v)
			switch str(x["type"]) {
			case "text":
				t.Text += str(x["text"])
			case "tool_use":
				b, _ := json.Marshal(x["input"])
				t.Calls = append(t.Calls, Call{str(x["id"]), str(x["name"]), string(b)})
			}
		}
	}
	return t, validateTurn(t)
}

func validateTurn(t Turn) error {
	if len(t.Text) > 256<<10 {
		return errors.New("model output too large")
	}
	if len(t.Calls) > 32 {
		return errors.New("too many tool calls in one step")
	}
	seen := map[string]bool{}
	for _, call := range t.Calls {
		if call.ID == "" || call.Name == "" {
			return errors.New("tool call must have an ID and name")
		}
		if seen[call.ID] {
			return errors.New("model returned duplicate tool call IDs")
		}
		seen[call.ID] = true
		if len(call.ID) > 256 || len(call.Name) > 256 {
			return errors.New("tool call ID or name is too large")
		}
		if len(call.Arguments) > 128<<10 {
			return errors.New("tool arguments too large")
		}
	}
	if strings.TrimSpace(t.Text) == "" && len(t.Calls) == 0 {
		return errors.New("model returned no text or supported tool calls")
	}
	return nil
}

func parseStream(r io.Reader, protocol string, emit Emit) (Turn, error) {
	t := Turn{Usage: map[string]int{}}
	calls := map[int]*Call{}
	blocks := map[int]map[string]any{}
	done := false
	terminal := false
	truncated := false
	scanner := bufio.NewScanner(io.LimitReader(r, 16<<20))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	dataLines := []string{}
	handle := func(data string) error {
		if data == "[DONE]" {
			done = true
			terminal = true
			return nil
		}
		if data == "" {
			return nil
		}
		var x map[string]any
		if err := json.Unmarshal([]byte(data), &x); err != nil {
			return fmt.Errorf("invalid SSE data: %w", err)
		}
		if x["error"] != nil || str(x["type"]) == "error" {
			return errors.New("model stream reported an error")
		}
		delta := ""
		switch protocol {
		case "openai-chat":
			for k, v := range usage(x["usage"]) {
				t.Usage[k] = v
			}
			for _, v := range array(x["choices"]) {
				ch := object(v)
				d := object(ch["delta"])
				delta += str(d["content"])
				finish := str(ch["finish_reason"])
				if finish != "" {
					done = true
				}
				if finish == "length" {
					truncated = true
				}
				if finish == "content_filter" {
					return errors.New("model output was stopped by the provider content filter")
				}
				delta += str(d["refusal"])
				for _, cv := range array(d["tool_calls"]) {
					c := object(cv)
					i := number(c["index"])
					if calls[i] == nil {
						calls[i] = &Call{}
					}
					cc := calls[i]
					cc.ID += str(c["id"])
					f := object(c["function"])
					cc.Name += str(f["name"])
					cc.Arguments += str(f["arguments"])
				}
			}
		case "openai-responses":
			switch str(x["type"]) {
			case "response.output_text.delta":
				delta = str(x["delta"])
			case "response.refusal.delta":
				delta = str(x["delta"])
			case "response.completed":
				parsed, err := parseJSON(object(x["response"]), protocol)
				if err != nil {
					return err
				}
				t.Calls = parsed.Calls
				t.Raw = parsed.Raw
				t.Usage = parsed.Usage
				if t.Text == "" {
					delta = parsed.Text
				}
				done = true
				terminal = true
			case "response.incomplete", "response.failed":
				return errors.New("model response did not complete")
			}
		case "anthropic":
			i := number(x["index"])
			switch str(x["type"]) {
			case "message_start":
				for k, v := range usage(object(x["message"])["usage"]) {
					t.Usage[k] = v
				}
			case "content_block_start":
				block := object(x["content_block"])
				blocks[i] = block
				if str(block["type"]) == "text" {
					delta = str(block["text"])
				}
				if str(block["type"]) == "tool_use" {
					calls[i] = &Call{ID: str(block["id"]), Name: str(block["name"])}
				}
			case "content_block_delta":
				d := object(x["delta"])
				switch str(d["type"]) {
				case "text_delta":
					delta = str(d["text"])
					if blocks[i] != nil {
						blocks[i]["text"] = str(blocks[i]["text"]) + delta
					}
				case "input_json_delta":
					if calls[i] != nil {
						calls[i].Arguments += str(d["partial_json"])
					}
				case "thinking_delta":
					if blocks[i] != nil {
						blocks[i]["thinking"] = str(blocks[i]["thinking"]) + str(d["thinking"])
					}
				case "signature_delta":
					if blocks[i] != nil {
						blocks[i]["signature"] = str(blocks[i]["signature"]) + str(d["signature"])
					}
				}
			case "message_delta":
				for k, v := range usage(x["usage"]) {
					t.Usage[k] = v
				}
				if str(object(x["delta"])["stop_reason"]) == "max_tokens" {
					truncated = true
				}
				if str(object(x["delta"])["stop_reason"]) == "pause_turn" {
					return errors.New("model paused a server tool turn; server tools are not supported by this executor")
				}
			case "message_stop":
				done = true
				terminal = true
			}
		}
		if len(delta) > 0 {
			t.Text += delta
			if len(t.Text) > 256<<10 {
				return errors.New("model output too large")
			}
			if err := emit("text.delta", map[string]any{"text": delta}); err != nil {
				return err
			}
		}
		for _, c := range calls {
			if len(c.Arguments) > 128<<10 {
				return errors.New("tool arguments too large")
			}
		}
		if len(calls) > 32 {
			return errors.New("too many tool calls in one step")
		}
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := handle(strings.Join(dataLines, "\n")); err != nil {
				return t, err
			}
			dataLines = nil
			if terminal {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return t, err
	}
	if len(dataLines) > 0 {
		if err := handle(strings.Join(dataLines, "\n")); err != nil {
			return t, err
		}
	}
	if !done {
		return t, errors.New("model stream interrupted before completion")
	}
	if truncated {
		return t, errors.New("model output token limit reached")
	}
	indexes := []int{}
	for i := range calls {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		c := calls[i]
		if c.Arguments == "" {
			c.Arguments = "{}"
			if protocol == "anthropic" && blocks[i]["input"] != nil {
				initial, err := json.Marshal(blocks[i]["input"])
				if err != nil {
					return t, fmt.Errorf("encode streamed tool arguments: %w", err)
				}
				c.Arguments = string(initial)
			}
		}
		t.Calls = append(t.Calls, *c)
		if protocol == "anthropic" {
			var args any
			if err := json.Unmarshal([]byte(c.Arguments), &args); err != nil {
				return t, errors.New("invalid streamed tool arguments")
			}
			blocks[i]["input"] = args
		}
	}
	if protocol == "anthropic" {
		indexes = nil
		for i := range blocks {
			indexes = append(indexes, i)
		}
		sort.Ints(indexes)
		for _, i := range indexes {
			t.Raw = append(t.Raw, blocks[i])
		}
	}
	return t, validateTurn(t)
}
