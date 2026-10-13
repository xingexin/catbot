package conversation_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/biz/toolcall"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/messaging"
)

type referenceSource struct {
	text   string
	source conversation.Session
	cutoff time.Time
	calls  int
	err    error
}

func (s *referenceSource) GroupContext(_ context.Context, source conversation.Session, cutoff time.Time) (string, error) {
	s.source, s.cutoff = source, cutoff
	s.calls++
	return s.text, s.err
}

func TestGroupReferenceIsPerTurnWithFixedCutoffAndUnchangedHistory(t *testing.T) {
	for _, kind := range []string{"api", "sdk"} {
		t.Run(kind, func(t *testing.T) {
			service, s := configArchiveConversation(t)
			executor := &snapshotExecutor{}
			service.Direct, service.SDK, service.ToolSource = executor, executor, noConversationTools{}
			reference := &referenceSource{text: "COLLECTED-GROUP-ONLY"}
			service.GroupContext = reference
			var session conversation.Session
			if err := s.Get(t.Context(), "session", "s", &session); err != nil {
				t.Fatal(err)
			}
			session.ChannelRoom, session.ChannelAccount, session.ChannelProvider = "authorized-room", "bot-account", "onebot"
			if err := s.Put(t.Context(), "session", "s", session); err != nil {
				t.Fatal(err)
			}
			var config agent.Config
			if err := s.Get(t.Context(), "config", "c", &config); err != nil {
				t.Fatal(err)
			}
			config.Kind = kind
			if err := s.Put(t.Context(), "config", "c", config); err != nil {
				t.Fatal(err)
			}
			run, err := service.Submit(t.Context(), "s", "summarize recent discussion", "first")
			if err != nil {
				t.Fatal(err)
			}
			received := run.CreatedAt.Add(-time.Minute)
			if err := s.Put(t.Context(), "qq-receipt", run.ID, messaging.ReplyReference{ReceivedAt: received}); err != nil {
				t.Fatal(err)
			}
			session.RememberNative(run.NativeKey(), "native-existing")
			if err := s.Put(t.Context(), "session", "s", session); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Execute(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			request := executor.request
			if request.Context != reference.text || request.NativeID != "native-existing" || request.Prompt != run.Prompt {
				t.Fatalf("context/native/prompt lost: %+v", request)
			}
			if !reference.cutoff.Equal(received) || reference.source.ChannelRoom != "authorized-room" || reference.source.ChannelAccount != "bot-account" {
				t.Fatalf("receipt cutoff or scope lost: %+v", reference)
			}
			if err := s.Get(t.Context(), "session", "s", &session); err != nil {
				t.Fatal(err)
			}
			if len(session.Messages) != 3 || session.Messages[1].Content != run.Prompt || session.Summary != "" {
				t.Fatalf("history changed: %+v", session)
			}
			for _, msg := range session.Messages {
				if strings.Contains(msg.Content, reference.text) {
					t.Fatal("ephemeral group data persisted in history")
				}
			}
			reference.text = "NEXT-TURN-REFERENCE"
			next, err := service.Submit(t.Context(), "s", "follow up", "second")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.Execute(t.Context(), next.ID); err != nil {
				t.Fatal(err)
			}
			if executor.request.Context != reference.text || !reference.cutoff.Equal(next.CreatedAt) {
				t.Fatal("new turn reused old context or wall-time cutoff")
			}
		})
	}
}

func TestBackgroundConversationUsesOriginalGroupSource(t *testing.T) {
	service, s := configArchiveConversation(t)
	executor := &snapshotExecutor{}
	service.Direct = executor
	service.ToolSource = &toolcall.Service{Store: s}
	reference := &referenceSource{text: "GROUP-ORIGIN"}
	service.GroupContext = reference
	source := conversation.Session{ID: "group-origin", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "account", ChannelRoom: "group"}
	if err := s.Put(t.Context(), "session", source.ID, source); err != nil {
		t.Fatal(err)
	}
	var session conversation.Session
	if err := s.Get(t.Context(), "session", "s", &session); err != nil {
		t.Fatal(err)
	}
	session.Channel, session.OriginSessionID = "task", source.ID
	if err := s.Put(t.Context(), "session", session.ID, session); err != nil {
		t.Fatal(err)
	}
	run, err := service.Submit(t.Context(), session.ID, "scheduled summary", "background")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Execute(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if reference.source.ID != source.ID || !reference.cutoff.Equal(run.CreatedAt) || executor.request.Context != "GROUP-ORIGIN" {
		t.Fatalf("task lost source scope: %+v", reference)
	}
}

func TestGroupReferenceReadFailureStopsExecutionAndPrivateChatsDoNotRead(t *testing.T) {
	for _, group := range []bool{false, true} {
		service, s := configArchiveConversation(t)
		executor := &snapshotExecutor{}
		service.Direct, service.ToolSource = executor, noConversationTools{}
		failure := errors.New("group records unavailable")
		reference := &referenceSource{err: failure}
		service.GroupContext = reference
		if group {
			var session conversation.Session
			if err := s.Get(t.Context(), "session", "s", &session); err != nil {
				t.Fatal(err)
			}
			session.ChannelRoom = "group"
			if err := s.Put(t.Context(), "session", "s", session); err != nil {
				t.Fatal(err)
			}
		}
		run, err := service.Submit(t.Context(), "s", "question", "run")
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.Execute(t.Context(), run.ID)
		if group {
			if !errors.Is(err, failure) || executor.calls != 0 {
				t.Fatalf("group read silently ignored: %v calls=%d", err, executor.calls)
			}
		} else if err != nil || reference.calls != 0 || executor.calls != 1 {
			t.Fatalf("private chat queried group records: %v calls=%d", err, reference.calls)
		}
	}
}
