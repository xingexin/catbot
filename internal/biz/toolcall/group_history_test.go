package toolcall

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/infra/store"
)

type groupHistoryStub struct {
	allowed bool
	err     error
	source  conversation.Session
	cutoff  time.Time
	calls   int
	args    map[string]any
}

func (g *groupHistoryStub) CanReadGroupHistory(context.Context, conversation.Session) (bool, error) {
	return g.allowed, g.err
}
func (g *groupHistoryStub) GroupHistory(_ context.Context, source conversation.Session, cutoff time.Time, args map[string]any) (any, error) {
	g.source, g.cutoff, g.args = source, cutoff, args
	g.calls++
	return map[string]any{"messages": []any{}, "hasMore": false}, nil
}

func TestGroupHistoryRequiresExplicitPersonaAndCurrentRecordingAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name             string
		group, recording bool
		tools            []string
		available        bool
	}{
		{"explicit", true, true, []string{agent.GroupHistoryToolName}, true},
		{"nil permission", true, true, nil, false},
		{"empty permission", true, true, []string{}, false},
		{"other permission", true, true, []string{"system__task_list"}, false},
		{"private conversation", false, true, []string{agent.GroupHistoryToolName}, false},
		{"recording revoked", true, false, []string{agent.GroupHistoryToolName}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := store.NewMemory()
			service := seedListToolRun(t, s, tc.group)
			history := &groupHistoryStub{allowed: tc.recording}
			service.GroupHistory = history
			var run conversation.Run
			if err := s.Get(t.Context(), "run", "run", &run); err != nil {
				t.Fatal(err)
			}
			run.Persona.Tools = tc.tools
			run.CreatedAt = time.Now().UTC()
			if err := s.Put(t.Context(), "run", run.ID, run); err != nil {
				t.Fatal(err)
			}
			tools, err := service.Tools(t.Context(), run)
			if err != nil {
				t.Fatal(err)
			}
			found := slices.ContainsFunc(tools, func(tool agent.Tool) bool { return tool.Name == agent.GroupHistoryToolName })
			if found != tc.available {
				t.Fatalf("availability=%v, want %v", found, tc.available)
			}
			_, err = service.Call(t.Context(), run.ID, agent.GroupHistoryToolName, map[string]any{}, "read")
			if (err == nil) != tc.available {
				t.Fatalf("call permission mismatch: %v", err)
			}
			if !tc.available && history.calls != 0 {
				t.Fatal("unauthorized history read")
			}
			if tc.available {
				history.allowed = false
				if _, err := service.Call(t.Context(), run.ID, agent.GroupHistoryToolName, map[string]any{}, "read"); err == nil || history.calls != 1 {
					t.Fatal("revocation ignored or cached output leaked")
				}
				history.err = errors.New("authorization unavailable")
				if _, err := service.Tools(t.Context(), run); !errors.Is(err, history.err) {
					t.Fatal("authorization errors ignored", err)
				}
			}
		})
	}
}

func TestGroupHistoryPreservesTaskSourceCutoffAndRejectsCallerScope(t *testing.T) {
	s := store.NewMemory()
	service := seedListToolRun(t, s, true)
	history := &groupHistoryStub{allowed: true}
	service.GroupHistory = history
	var run conversation.Run
	if err := s.Get(t.Context(), "run", "run", &run); err != nil {
		t.Fatal(err)
	}
	run.Persona.Tools = []string{agent.GroupHistoryToolName}
	run.CreatedAt = time.Now().UTC()
	run.SessionID = "task-session"
	if err := s.Put(t.Context(), "run", run.ID, run); err != nil {
		t.Fatal(err)
	}
	origin := conversation.Session{ID: "source", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "bot-account", ChannelRoom: "own-room"}
	if err := s.Put(t.Context(), "session", "source", origin); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(t.Context(), "session", "task-session", conversation.Session{ID: "task-session", Channel: "task", OriginSessionID: origin.ID}); err != nil {
		t.Fatal(err)
	}
	received := run.CreatedAt.Add(-time.Minute)
	if err := s.Put(t.Context(), "qq-receipt", run.ID, messaging.ReplyReference{ReceivedAt: received}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"limit": 20, "keyword": "discussion", "sender": "member", "startTime": "2026-10-13T00:00:00+08:00", "cursor": "opaque-cursor"}
	if _, err := service.Call(t.Context(), run.ID, agent.GroupHistoryToolName, args, "page"); err != nil {
		t.Fatal(err)
	}
	if history.source.ID != origin.ID || history.source.ChannelAccount != origin.ChannelAccount || history.source.ChannelRoom != origin.ChannelRoom || history.source.ChannelProvider != origin.ChannelProvider || !history.cutoff.Equal(received) || history.args["cursor"] != "opaque-cursor" {
		t.Fatalf("source/cutoff/filters lost: %+v", history)
	}
	for _, bad := range []map[string]any{{"groupId": "other"}, {"route": "other"}, {"account": "other"}, {"limit": 101}, {"limit": 0}, {"limit": 1.5}} {
		if _, err := service.Call(t.Context(), run.ID, agent.GroupHistoryToolName, bad, "bad"); err == nil {
			t.Fatalf("accepted bad scope or limit: %+v", bad)
		}
	}
	if history.calls != 1 {
		t.Fatal("invalid arguments reached history store")
	}
}
