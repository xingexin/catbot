package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/infra/auth"
	"github.com/xingexin/catbot/internal/infra/store"
)

type runsFixture struct{ run conversation.Run }

func (r runsFixture) FindRun(context.Context, string) (conversation.Run, error) { return r.run, nil }

type toolsFixture struct {
	calls int
	err   error
}

func (t *toolsFixture) Tools(context.Context, conversation.Run) ([]agent.Tool, error) {
	t.calls++
	return nil, t.err
}
func (t *toolsFixture) Call(context.Context, string, string, map[string]any, string) (any, error) {
	t.calls++
	return nil, nil
}

type eventsFixture struct{}

func (eventsFixture) Emit(context.Context, string, string, map[string]any) error { return nil }
func TestMCPRejectsInactiveOrUnscopedTokensBeforeTools(t *testing.T) {
	tokens := &auth.Tokens{Key: "fixture"}
	for _, tc := range []struct {
		token, status string
		want          int
	}{
		{"invalid", "running", http.StatusUnauthorized},
		{tokens.Token("plugin:snapshot"), "running", http.StatusUnauthorized},
		{tokens.Token("run:run-id"), "completed", http.StatusForbidden},
		{tokens.Token("run:run-id"), "interrupted", http.StatusForbidden},
	} {
		tools := &toolsFixture{}
		h := New(tokens, runsFixture{conversation.Run{ID: "run-id", Status: tc.status}}, tools, eventsFixture{})
		r := httptest.NewRequest("POST", "/internal/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want || tools.calls != 0 {
			t.Fatal(w.Code, tc.want, tools.calls)
		}
	}
}

func TestMCPToolSourceErrorStatus(t *testing.T) {
	tokens := &auth.Tokens{Key: "fixture"}
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"missing", store.ErrNotFound, http.StatusNotFound},
		{"wrapped missing", fmt.Errorf("source session: %w", store.ErrNotFound), http.StatusNotFound},
		{"other failure", errors.New("cannot discover tools"), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := &toolsFixture{err: tc.err}
			h := New(tokens, runsFixture{conversation.Run{ID: "run-id", Status: "running"}}, tools, eventsFixture{})
			r := httptest.NewRequest("POST", "/internal/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			r.Header.Set("Authorization", "Bearer "+tokens.Token("run:run-id"))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || tools.calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, tools.calls, w.Body.String())
			}
		})
	}
}
