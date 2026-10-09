package conversation_test

import (
	"context"
	"errors"
	"testing"

	conversationbiz "github.com/xingexin/catbot/internal/biz/conversation"
	lifecyclebiz "github.com/xingexin/catbot/internal/biz/lifecycle"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	archive "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/persona"
	"github.com/xingexin/catbot/internal/infra/store"
)

type emptyPlugins struct{}

func (emptyPlugins) Snapshots(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func TestIncomingMessageRestoresArchivedHistoryButPurgedRequestCannotReplay(t *testing.T) {
	s := store.NewMemory()
	for kind, value := range map[string]any{"session": conversation.Session{ID: "s", ConfigID: "c", PersonaID: "p", Messages: []agent.Message{{Role: "user", Content: "remember me"}}}, "config": agent.Config{ID: "c"}, "persona": persona.Persona{ID: "p"}} {
		id := map[string]string{"session": "s", "config": "c", "persona": "p"}[kind]
		if err := s.Put(t.Context(), kind, id, value); err != nil {
			t.Fatal(err)
		}
	}
	catalog := &lifecyclebiz.Service{Store: s}
	result, err := catalog.Batch(t.Context(), lifecyclebiz.Request{Resource: lifecycle.ResourceSession, Action: lifecycle.ActionArchive, IDs: []string{"s"}})
	if err != nil || len(result.Failed) > 0 {
		t.Fatal(result, err)
	}
	c := &conversationbiz.Service{Store: s, Plugins: emptyPlugins{}}
	run, err := c.Submit(t.Context(), "s", "next turn", "id")
	if err != nil {
		t.Fatal(err)
	}
	archived, err := archive.Archived(t.Context(), s, lifecycle.ResourceSession, "s")
	if err != nil || archived {
		t.Fatal("new message did not restore session", err)
	}
	var session conversation.Session
	if err := s.Get(t.Context(), "session", "s", &session); err != nil || len(session.Messages) != 1 {
		t.Fatal(session, err)
	}
	run.Status = "completed"
	if err := s.Put(t.Context(), "run", run.ID, run); err != nil {
		t.Fatal(err)
	}
	for _, action := range []lifecycle.Action{lifecycle.ActionArchive, lifecycle.ActionPurge} {
		result, err = catalog.Batch(t.Context(), lifecyclebiz.Request{Resource: lifecycle.ResourceSession, Action: action, IDs: []string{"s"}})
		if err != nil || len(result.Failed) > 0 {
			t.Fatal(result, err)
		}
	}
	if _, err := c.Submit(t.Context(), "s", "next turn", "id"); !errors.Is(err, archive.ErrPurged) {
		t.Fatal("deleted callback recreated run", err)
	}
	var deleted conversation.Run
	if err := s.Get(t.Context(), "run", run.ID, &deleted); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}
