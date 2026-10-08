// Package mcp exposes active execution tools without coupling the SDK transport
// to an application container or persistent storage implementation.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/infra/store"
)

type TokenVerifier interface{ VerifyToken(string) (string, bool) }
type RunReader interface {
	FindRun(context.Context, string) (conversation.Run, error)
}
type ToolCaller interface {
	Tools(context.Context, conversation.Run) ([]agent.Tool, error)
	Call(context.Context, string, string, map[string]any, string) (any, error)
}
type EventEmitter interface {
	Emit(context.Context, string, string, map[string]any) error
}
type Handler struct {
	tokens TokenVerifier
	runs   RunReader
	tools  ToolCaller
	events EventEmitter
}

func New(tokens TokenVerifier, runs RunReader, tools ToolCaller, events EventEmitter) *Handler {
	return &Handler{tokens: tokens, runs: runs, tools: tools, events: events}
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.tokens.VerifyToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if !ok || !strings.HasPrefix(scope, "run:") {
		responseError(w, 401, "invalid run token")
		return
	}
	run, err := h.runs.FindRun(r.Context(), strings.TrimPrefix(scope, "run:"))
	if err != nil || run.Status != "running" {
		responseError(w, 403, "run is not active")
		return
	}
	specs, err := h.tools.Tools(r.Context(), run)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		responseError(w, status, err.Error())
		return
	}
	server := protocol.NewServer(&protocol.Implementation{Name: "secretary-tools", Version: "1.0.0"}, nil)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		responseError(w, 400, err.Error())
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var envelope struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(raw, &envelope)
	operationID := run.ID + ":mcp:" + string(envelope.ID)
	for _, spec := range specs {
		server.AddTool(&protocol.Tool{Name: spec.Name, Description: spec.Description, InputSchema: spec.InputSchema}, func(ctx context.Context, req *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
			var args map[string]any
			rawArgs := req.Params.Arguments
			if len(rawArgs) == 0 {
				rawArgs = json.RawMessage("{}")
			}
			if err := json.Unmarshal(rawArgs, &args); err != nil {
				return nil, err
			}
			if len(envelope.ID) == 0 {
				return nil, errors.New("tool invocation requires a JSON-RPC ID")
			}
			_ = h.events.Emit(ctx, run.ID, "tool.started", map[string]any{"name": spec.Name, "callId": operationID})
			value, err := h.tools.Call(ctx, run.ID, spec.Name, args, operationID)
			encoded := ""
			if err == nil {
				encoded, value, err = agent.ToolObservation(value, 64<<10)
			}
			event := map[string]any{"name": spec.Name, "callId": operationID, "result": value, "isError": err != nil}
			if err != nil {
				event["error"] = err.Error()
			}
			_ = h.events.Emit(ctx, run.ID, "tool.completed", event)
			if err != nil {
				return &protocol.CallToolResult{IsError: true, Content: []protocol.Content{&protocol.TextContent{Text: err.Error()}}}, nil
			}
			return &protocol.CallToolResult{Content: []protocol.Content{&protocol.TextContent{Text: encoded}}}, nil
		})
	}
	protocol.NewStreamableHTTPHandler(func(*http.Request) *protocol.Server { return server }, &protocol.StreamableHTTPOptions{Stateless: true, JSONResponse: true}).ServeHTTP(w, r)
}
func responseError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
