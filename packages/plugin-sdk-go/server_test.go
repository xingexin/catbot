package pluginsdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	pluginsdk "github.com/xingexin/catbot/packages/plugin-sdk-go"
)

func options() pluginsdk.Options {
	return pluginsdk.Options{Manifest: pluginsdk.Manifest{
		ID: "test-go", Version: "1.0.0", Tools: []pluginsdk.Tool{{
			Name: "test_echo", Description: "Echo text", InputSchema: map[string]any{
				"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}},
				"required": []any{"text"}, "additionalProperties": false,
			},
		}},
	}}
}

func connect(t *testing.T, options pluginsdk.Options, handler pluginsdk.Handler) *mcp.ClientSession {
	t.Helper()
	server, err := pluginsdk.NewServer(options, map[string]pluginsdk.Handler{"test_echo": handler})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, &mcp.ClientSessionOptions{ProtocolVersion: "2024-11-05"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func toolText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("content = %#v", result.Content)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T", result.Content[0])
	}
	return text.Text
}

func TestServerMCPDiscoveryAndOperationContext(t *testing.T) {
	t.Parallel()
	opts := options()
	opts.Config = map[string]any{"prefix": "hello"}
	session := connect(t, opts, func(ctx context.Context, args map[string]any, tool pluginsdk.ToolContext) (any, error) {
		if tool.OperationID != "run-1/tool-1" {
			t.Errorf("operation ID = %q", tool.OperationID)
		}
		if tool.Host == nil {
			t.Error("host client missing")
		}
		return map[string]any{"text": fmt.Sprint(tool.Config["prefix"], " ", args["text"])}, nil
	})
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "test_echo" || listed.Tools[0].Description != "Echo text" {
		t.Fatalf("tools = %+v", listed.Tools)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "test_echo", Arguments: map[string]any{"text": "Go"},
		Meta: mcp.Meta{"secretary/operationId": "run-1/tool-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || toolText(t, result) != `{"text":"hello Go"}` {
		t.Fatalf("result = %+v", result)
	}
	if result.StructuredContent == nil {
		t.Fatal("missing structured content")
	}
	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "unknown", Arguments: map[string]any{}})
	if err == nil {
		t.Fatal("unknown tool unexpectedly succeeded")
	}
}

func TestServerToolResults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		handler   pluginsdk.Handler
		arguments map[string]any
		output    map[string]any
		want      string
		wantError bool
	}{
		{name: "invalid arguments", handler: func(context.Context, map[string]any, pluginsdk.ToolContext) (any, error) {
			t.Error("invalid args reached handler")
			return nil, nil
		}, arguments: map[string]any{"text": 123}, want: "Invalid arguments", wantError: true},
		{name: "invalid output", handler: func(context.Context, map[string]any, pluginsdk.ToolContext) (any, error) {
			return map[string]any{"text": 123}, nil
		},
			output: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}, want: "Invalid tool output", wantError: true},
		{name: "scalar normalization", handler: func(context.Context, map[string]any, pluginsdk.ToolContext) (any, error) { return "hi", nil }, want: `{"value":"hi"}`},
		{name: "array normalization", handler: func(context.Context, map[string]any, pluginsdk.ToolContext) (any, error) { return []string{"hi"}, nil }, want: `{"value":["hi"]}`},
		{name: "nil normalization", handler: func(context.Context, map[string]any, pluginsdk.ToolContext) (any, error) { return nil, nil }, want: `{"value":null}`},
		{name: "handler error", handler: func(context.Context, map[string]any, pluginsdk.ToolContext) (any, error) {
			return nil, errors.New("cannot read")
		}, want: "cannot read", wantError: true},
		{name: "panic contained", handler: func(context.Context, map[string]any, pluginsdk.ToolContext) (any, error) { panic("sensitive panic") }, want: "Tool panicked", wantError: true},
		{name: "JSON output required", handler: func(context.Context, map[string]any, pluginsdk.ToolContext) (any, error) { return make(chan int), nil }, want: "JSON values", wantError: true},
		{name: "large output", handler: func(context.Context, map[string]any, pluginsdk.ToolContext) (any, error) {
			return strings.Repeat("x", 513<<10), nil
		}, want: "exceeds 512 KB", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := options()
			opts.Manifest.Tools[0].OutputSchema = test.output
			session := connect(t, opts, test.handler)
			arguments := test.arguments
			if arguments == nil {
				arguments = map[string]any{"text": "hello"}
			}
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "test_echo", Arguments: arguments})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError != test.wantError || !strings.Contains(toolText(t, result), test.want) {
				t.Fatalf("result = %+v, text = %q", result, toolText(t, result))
			}
			if _, err := session.ListTools(t.Context(), nil); err != nil {
				t.Fatalf("server did not survive tool failure: %v", err)
			}
		})
	}
}

func TestServerRedactsStructuredOutputAndErrors(t *testing.T) {
	t.Parallel()
	secret := "password\"with\\escapes"
	opts := options()
	opts.Config = map[string]any{"mail": map[string]any{"password": secret}, "apiKey": "key-12345"}
	opts.HostToken = "host-private-token"
	session := connect(t, opts, func(_ context.Context, args map[string]any, tool pluginsdk.ToolContext) (any, error) {
		if args["text"] == "error" {
			return nil, fmt.Errorf("failed: %s key-12345 host-private-token", secret)
		}
		return map[string]any{"text": secret, "nested": []string{"key-12345", "host-private-token"}}, nil
	})
	for _, mode := range []string{"output", "error"} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "test_echo", Arguments: map[string]any{"text": mode}})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result)
		for _, value := range []string{secret, "key-12345", "host-private-token"} {
			quoted, _ := json.Marshal(value)
			if strings.Contains(string(encoded), string(quoted[1:len(quoted)-1])) {
				t.Fatalf("leaked sensitive value in %s result", mode)
			}
		}
		if !strings.Contains(toolText(t, result), "[REDACTED]") {
			t.Fatalf("not redacted: %q", toolText(t, result))
		}
	}
}

func TestServerConfigurationIsIsolatedForConcurrentCalls(t *testing.T) {
	t.Parallel()
	opts := options()
	opts.Config = map[string]any{"nested": map[string]any{"counter": 0}}
	var bad atomic.Bool
	session := connect(t, opts, func(_ context.Context, _ map[string]any, tool pluginsdk.ToolContext) (any, error) {
		nested := tool.Config["nested"].(map[string]any)
		if nested["counter"] != float64(0) {
			bad.Store(true)
		}
		nested["counter"] = 42
		return nested, nil
	})
	var calls sync.WaitGroup
	for range 20 {
		calls.Go(func() {
			_, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "test_echo", Arguments: map[string]any{"text": "ok"}})
			if err != nil {
				t.Error(err)
			}
		})
	}
	calls.Wait()
	if bad.Load() {
		t.Fatal("calls shared mutable configuration")
	}
}

func TestServerPropagatesCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	canceled := make(chan struct{})
	session := connect(t, options(), func(ctx context.Context, _ map[string]any, _ pluginsdk.ToolContext) (any, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "test_echo", Arguments: map[string]any{"text": "wait"}})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never started")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler context was not canceled")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("call should fail after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not return")
	}
}

func TestNewServerRejectsInvalidRegistration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*pluginsdk.Options)
		missing bool
	}{
		{name: "missing handler", missing: true},
		{name: "invalid input", mutate: func(o *pluginsdk.Options) { o.Manifest.Tools[0].InputSchema = map[string]any{"type": "array"} }},
		{name: "external reference", mutate: func(o *pluginsdk.Options) {
			o.Manifest.Tools[0].InputSchema = map[string]any{"type": "object", "$ref": "file:///etc/passwd"}
		}},
		{name: "bad schema", mutate: func(o *pluginsdk.Options) { o.Manifest.Tools[0].OutputSchema = map[string]any{"type": "made-up"} }},
		{name: "duplicate names", mutate: func(o *pluginsdk.Options) { o.Manifest.Tools = append(o.Manifest.Tools, o.Manifest.Tools[0]) }},
		{name: "bad name", mutate: func(o *pluginsdk.Options) { o.Manifest.Tools[0].Name = "with space" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := options()
			if test.mutate != nil {
				test.mutate(&opts)
			}
			handlers := map[string]pluginsdk.Handler{"test_echo": func(context.Context, map[string]any, pluginsdk.ToolContext) (any, error) { return nil, nil }}
			if test.missing {
				handlers = map[string]pluginsdk.Handler{}
			}
			if _, err := pluginsdk.NewServer(opts, handlers); err == nil {
				t.Fatal("invalid registration accepted")
			}
		})
	}
}

func TestServerRedactsSchemaPasswordFields(t *testing.T) {
	t.Parallel()
	opts := options()
	opts.Config = map[string]any{
		"auth":        "top-level-sensitive",
		"connection":  map[string]any{"auth": "nested-sensitive"},
		"accounts":    []any{map[string]any{"auth": "array-sensitive"}},
		"credentials": []any{"array-string-sensitive"},
		"ordinary":    "safe-label",
	}
	password := map[string]any{"type": "string", "format": "password"}
	opts.Manifest.ConfigSchema = map[string]any{"type": "object", "properties": map[string]any{
		"auth":       password,
		"connection": map[string]any{"type": "object", "properties": map[string]any{"auth": password}},
		"accounts": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "properties": map[string]any{"auth": password},
		}},
		"credentials": map[string]any{"type": "array", "items": password},
	}}
	session := connect(t, opts, func(_ context.Context, args map[string]any, tool pluginsdk.ToolContext) (any, error) {
		if args["text"] == "error" {
			return nil, fmt.Errorf("top-level-sensitive nested-sensitive array-sensitive array-string-sensitive safe-label")
		}
		return tool.Config, nil
	})
	for _, mode := range []string{"output", "error"} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
			Name: "test_echo", Arguments: map[string]any{"text": mode},
		})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result)
		for _, secret := range []string{"top-level-sensitive", "nested-sensitive", "array-sensitive", "array-string-sensitive"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("schema password leaked in %s", mode)
			}
		}
		if !strings.Contains(string(encoded), "safe-label") {
			t.Fatalf("non-sensitive value removed in %s", mode)
		}
		if !strings.Contains(string(encoded), "[REDACTED]") {
			t.Fatalf("redaction marker missing in %s", mode)
		}
	}
}
