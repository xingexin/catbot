package conversation_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	conversationbiz "github.com/xingexin/catbot/internal/biz/conversation"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	archive "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/persona"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
)

func configArchiveConversation(t *testing.T) (*conversationbiz.Service, *store.Memory) {
	t.Helper()
	s := store.NewMemory()
	for _, record := range []struct {
		kind, id string
		value    any
	}{
		{"session", "s", conversation.Session{ID: "s", ConfigID: "c", PersonaID: "p", Messages: []agent.Message{{Role: "user", Content: "remember me"}}}},
		{"config", "c", agent.Config{ID: "c", Kind: "api", Model: "original-model", TimeoutSec: 5}},
		{"persona", "p", persona.Persona{ID: "p"}},
	} {
		if err := s.Put(t.Context(), record.kind, record.id, record.value); err != nil {
			t.Fatal(err)
		}
	}
	v, err := vault.New(s, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return &conversationbiz.Service{Store: s, Vault: v, Plugins: emptyPlugins{}}, s
}

func markConversationResource(t *testing.T, s store.Store, resource lifecycle.Resource, id string) {
	t.Helper()
	unlock, err := s.Lock(t.Context(), lifecycle.ReferenceLock)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := archive.Mark(t.Context(), s, lifecycle.ArchiveRecord{Resource: resource, RecordID: id, ArchivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitRejectsArchivedConfigWithoutChangingConversation(t *testing.T) {
	t.Parallel()
	service, s := configArchiveConversation(t)
	markConversationResource(t, s, lifecycle.ResourceSession, "s")
	markConversationResource(t, s, lifecycle.ResourceConfig, "c")

	if _, err := service.Submit(t.Context(), "s", "next turn", "new"); !errors.Is(err, archive.ErrArchived) || !strings.Contains(err.Error(), "切换") {
		t.Fatalf("expected actionable archived configuration error, got %v", err)
	}
	if runs, err := store.All[conversation.Run](t.Context(), s, "run"); err != nil || len(runs) != 0 {
		t.Fatalf("rejected request created a run: %+v, %v", runs, err)
	}
	if archived, err := archive.Archived(t.Context(), s, lifecycle.ResourceSession, "s"); err != nil || !archived {
		t.Fatalf("rejected request restored archived conversation: %v", err)
	}
	var session conversation.Session
	if err := s.Get(t.Context(), "session", "s", &session); err != nil || len(session.Messages) != 1 || session.Messages[0].Content != "remember me" || session.ConfigID != "c" {
		t.Fatalf("rejected request changed history or binding: %+v, %v", session, err)
	}
	if err := archive.Unmark(t.Context(), s, lifecycle.ResourceConfig, "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Submit(t.Context(), "s", "next turn", "new"); err != nil {
		t.Fatalf("restored configuration should work with preserved history: %v", err)
	}
}

type noConversationTools struct{}

func (noConversationTools) Tools(context.Context, conversation.Run) ([]agent.Tool, error) {
	return nil, nil
}

type snapshotExecutor struct {
	request agent.Request
	calls   int
}

func (e *snapshotExecutor) Run(_ context.Context, request agent.Request, _ agent.Emit) (agent.Result, error) {
	e.request = request
	e.calls++
	return agent.Result{Text: "retained history"}, nil
}

func TestConfigArchivePreservesSubmittedRunAndIdempotentReplay(t *testing.T) {
	t.Parallel()
	service, s := configArchiveConversation(t)
	executor := &snapshotExecutor{}
	service.Direct, service.ToolSource = executor, noConversationTools{}
	run, err := service.Submit(t.Context(), "s", "next turn", "existing")
	if err != nil {
		t.Fatal(err)
	}
	markConversationResource(t, s, lifecycle.ResourceConfig, "c")

	replayed, err := service.Submit(t.Context(), "s", "next turn", "existing")
	if err != nil || replayed.ID != run.ID {
		t.Fatalf("archive broke submission idempotency: %+v, %v", replayed, err)
	}
	completed, err := service.Execute(t.Context(), run.ID)
	if err != nil || completed.Status != "completed" || executor.calls != 1 {
		t.Fatalf("prepared run did not complete: %+v, %v", completed, err)
	}
	if executor.request.Config.Model != "original-model" || len(executor.request.History) != 1 || executor.request.History[0].Content != "remember me" {
		t.Fatalf("prepared configuration or history changed: %+v", executor.request)
	}
	if _, err := service.Submit(t.Context(), "s", "another turn", "new"); !errors.Is(err, archive.ErrArchived) {
		t.Fatalf("new turn should reject archived config: %v", err)
	}
	if _, err := service.Execute(t.Context(), run.ID); err != nil || executor.calls != 1 {
		t.Fatalf("completed request should not execute again: calls=%d err=%v", executor.calls, err)
	}
}

func TestConversationCanSwitchAwayFromArchivedConfig(t *testing.T) {
	t.Parallel()
	service, s := configArchiveConversation(t)
	markConversationResource(t, s, lifecycle.ResourceConfig, "c")
	if err := s.Put(t.Context(), "config", "replacement", agent.Config{ID: "replacement"}); err != nil {
		t.Fatal(err)
	}
	updated, err := service.SaveSession(t.Context(), conversationbiz.SessionInput{ID: "s", Title: "kept conversation", ConfigID: "replacement", PersonaID: "p"})
	if err != nil || updated.ConfigID != "replacement" || len(updated.Messages) != 1 || updated.Messages[0].Content != "remember me" {
		t.Fatalf("switch lost conversation history: %+v, %v", updated, err)
	}
	if _, err := service.Submit(t.Context(), "s", "next turn", "replacement-run"); err != nil {
		t.Fatalf("replacement configuration should allow next turn: %v", err)
	}
}
