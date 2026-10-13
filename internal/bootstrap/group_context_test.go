package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	message "github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/domain/persona"
	"github.com/xingexin/catbot/internal/infra/store"
)

func enableGroupHistory(t *testing.T, a *App) {
	t.Helper()
	b, err := a.Messaging.OneBotBinding(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	settings := message.DefaultGroupHistorySettings()
	settings.Enabled = true
	settings.RoomIDs = []string{"1128987429"}
	b.GroupHistory = &settings
	if err := a.Store.Put(t.Context(), "qq-binding", "onebot", b); err != nil {
		t.Fatal(err)
	}
	p := persona.Persona{ID: "group-persona", Name: "群聊助理", Tools: []string{"system__group_history"}}
	if err := a.Store.Put(t.Context(), "persona", p.ID, p); err != nil {
		t.Fatal(err)
	}
}
func historySource() conversation.Session {
	return conversation.Session{Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "10001", ChannelRoom: "1128987429", Recipient: "20002"}
}
func readGroupHistory(t *testing.T, a *App, source conversation.Session, args map[string]any) string {
	t.Helper()
	value, err := a.Messaging.GroupHistory(t.Context(), source, time.Now().UTC(), args)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestGroupCaptureDoesNotExecuteAndDeduplicates(t *testing.T) {
	a := qqGroupTestApp(t)
	enableGroupHistory(t, a)
	in := qqGroupInbound()
	in.Mentioned = false
	in.Peer, in.SenderName, in.Text = "30003", "小明", "周五交付，负责人小明"
	in.ReceivedAt = time.Now().Add(-time.Minute)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := a.Messaging.HandleIncoming(t.Context(), in); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	runs, err := store.All[conversation.Run](t.Context(), a.Store, "run")
	if err != nil || len(runs) != 0 {
		t.Fatalf("ordinary message triggered work: %v %v", runs, err)
	}
	history := readGroupHistory(t, a, historySource(), map[string]any{})
	if strings.Count(history, in.Text) != 1 || !strings.Contains(history, "小明") {
		t.Fatalf("missing or duplicated history: %s", history)
	}
	for _, mutate := range []func(*message.InboundMessage){
		func(m *message.InboundMessage) { m.Account = "other-account" },
		func(m *message.InboundMessage) { m.RoomID = "1128987430" },
		func(m *message.InboundMessage) { m.RoomID = "999999" },
		func(m *message.InboundMessage) { m.RoomID = "" },
	} {
		other := in
		mutate(&other)
		other.MessageID = "other"
		other.Text = "不得记录"
		if err := a.Messaging.HandleIncoming(t.Context(), other); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(readGroupHistory(t, a, historySource(), map[string]any{}), "不得记录") {
		t.Fatal("different group/account/private message leaked")
	}
}

func TestGroupMentionUsesSharedContextButPrivateAgentHistory(t *testing.T) {
	a := qqGroupTestApp(t)
	enableGroupHistory(t, a)
	in := qqGroupInbound()
	in.Mentioned, in.Peer, in.Text, in.MessageID = false, "30003", "验收资料：周五交付", "earlier"
	in.ReceivedAt = time.Now().Add(-time.Minute)
	if err := a.Messaging.HandleIncoming(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int32
	channel, _ := a.Messaging.Channel("onebot")
	channel.Sender = senderFunc(func(context.Context, message.OutboundMessage) (message.SendResult, error) {
		return message.SendResult{Status: message.Sent, MessageID: fmt.Sprint("reply-", sends.Add(1))}, nil
	})
	if err := a.Messaging.ConfigureChannel("onebot", channel); err != nil {
		t.Fatal(err)
	}
	var executed atomic.Int32
	a.Conversation.Direct = execFunc(func(ctx context.Context, req agent.Request, _ agent.Emit) (agent.Result, error) {
		executed.Add(1)
		if !strings.Contains(req.Context, "验收资料：周五交付") || strings.Contains(req.Context, "迟到资料") {
			t.Errorf("incorrect shared context: %s", req.Context)
		}
		if len(req.History) != 0 {
			t.Errorf("new participant inherited private conversation: %#v", req.History)
		}
		value, err := a.Tools.Call(ctx, req.RunID, "system__group_history", map[string]any{"keyword": "验收资料"}, "query-history")
		if err != nil {
			return agent.Result{}, err
		}
		raw, _ := json.Marshal(value)
		if !strings.Contains(string(raw), "周五交付") {
			t.Errorf("history tool did not find earlier messages: %s", raw)
		}
		return agent.Result{Text: "结论：周五交付"}, nil
	})
	cutoff := time.Now().Add(-time.Second)
	for _, peer := range []string{"20002", "40004"} {
		request := qqGroupInbound()
		request.Peer = peer
		request.MessageID = "summary-" + peer
		request.Text = "总结聊天"
		request.ReceivedAt = cutoff
		if err := a.Messaging.HandleIncoming(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	late := in
	late.MessageID = "late"
	late.Text = "迟到资料"
	late.ReceivedAt = time.Now()
	if err := a.Messaging.HandleIncoming(t.Context(), late); err != nil {
		t.Fatal(err)
	}
	runs, err := store.All[conversation.Run](t.Context(), a.Store, "run")
	if err != nil || len(runs) != 2 || runs[0].SessionID == runs[1].SessionID {
		t.Fatalf("participants not isolated: %v %v", runs, err)
	}
	for _, run := range runs {
		if result, err := a.Conversation.Execute(t.Context(), run.ID); err != nil || result.Status != "completed" {
			t.Fatalf("summary execution: %#v %v", result, err)
		}
		var session conversation.Session
		if err := a.Store.Get(t.Context(), "session", run.SessionID, &session); err != nil {
			t.Fatal(err)
		}
		if len(session.Messages) != 2 || session.Messages[0].Content != "总结聊天" {
			t.Fatalf("shared messages were persisted into private conversation: %#v", session.Messages)
		}
	}
	if executed.Load() != 2 || sends.Load() != 2 {
		t.Fatalf("execute=%d send=%d", executed.Load(), sends.Load())
	}
	history := readGroupHistory(t, a, historySource(), map[string]any{})
	if !strings.Contains(history, "结论：周五交付") {
		t.Fatal("successful outgoing reply missing from group history")
	}
}

func TestGroupHistoryAuthorizationRevokedAndParametersScoped(t *testing.T) {
	a := qqGroupTestApp(t)
	enableGroupHistory(t, a)
	source := historySource()
	for _, mutate := range []func(*conversation.Session){
		func(s *conversation.Session) { s.ChannelRoom = "" },
		func(s *conversation.Session) { s.ChannelRoom = "1128987430" },
		func(s *conversation.Session) { s.ChannelAccount = "10002" },
		func(s *conversation.Session) { s.ChannelProvider = "other" },
	} {
		other := source
		mutate(&other)
		if ok, err := a.Messaging.CanReadGroupHistory(t.Context(), other); err != nil || ok {
			t.Fatalf("scope unexpectedly authorized: %#v %v", other, err)
		}
		if _, err := a.Messaging.GroupHistory(t.Context(), other, time.Now(), map[string]any{}); err == nil {
			t.Fatal("unauthorized scope readable")
		}
	}
	for _, args := range []map[string]any{{"groupId": "99999"}, {"limit": 101}, {"startTime": "2026-10-13"}, {"startTime": time.Now().Add(time.Hour).Format(time.RFC3339)}} {
		if _, err := a.Messaging.GroupHistory(t.Context(), source, time.Now(), args); err == nil {
			t.Fatalf("invalid/untrusted scope params accepted: %#v", args)
		}
	}
	b, _ := a.Messaging.OneBotBinding(t.Context())
	b.GroupHistory.Enabled = false
	if err := a.Store.Put(t.Context(), "qq-binding", "onebot", b); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.Messaging.CanReadGroupHistory(t.Context(), source); err != nil || ok {
		t.Fatalf("disabled history readable: %v %v", ok, err)
	}
	if context, err := a.Messaging.GroupContext(t.Context(), source, time.Now()); err != nil || context != "" {
		t.Fatalf("disabled history injected: %s %v", context, err)
	}
}

func TestGroupHistoryManagementContractAndLegacyPreservation(t *testing.T) {
	a := qqGroupTestApp(t)
	cookie := request(t, a, "POST", "/api/login", map[string]string{"password": "test-password"}, nil).Result().Cookies()[0]
	b, _ := a.Messaging.OneBotBinding(t.Context())
	if b.GroupHistory == nil || b.GroupHistory.Enabled || b.GroupHistory.RecentMessages != 30 {
		t.Fatal("unsafe or missing legacy defaults", b.GroupHistory)
	}
	b.GroupHistory.Enabled = true
	b.GroupHistory.RoomIDs = []string{"1128987429"}
	b.GroupHistory.RecentMessages = 0
	if w := request(t, a, "PUT", "/api/qq/onebot", b, cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// A legacy client omitting the new field must not disable collection.
	b.GroupHistory = nil
	if w := request(t, a, "PUT", "/api/qq/onebot", b, cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	b, _ = a.Messaging.OneBotBinding(t.Context())
	if b.GroupHistory == nil || !b.GroupHistory.Enabled || b.GroupHistory.RecentMessages != 0 {
		t.Fatal("legacy save lost settings", b.GroupHistory)
	}
	b.GroupHistory.RoomIDs = []string{"999999"}
	if w := request(t, a, "PUT", "/api/qq/onebot", b, cookie); w.Code != 400 {
		t.Fatal("accepted an unbound recording group", w.Code, w.Body.String())
	}
	b.GroupHistory.RoomIDs = nil
	if w := request(t, a, "PUT", "/api/qq/onebot", b, cookie); w.Code != 400 {
		t.Fatal("empty enabled scope accepted", w.Code, w.Body.String())
	}
}

func TestOfflineGroupPolicyChangeCannotRebindAccount(t *testing.T) {
	a := qqGroupTestApp(t)
	cookie := request(t, a, "POST", "/api/login", map[string]string{"password": "test-password"}, nil).Result().Cookies()[0]
	b, _ := a.Messaging.OneBotBinding(t.Context())
	b.SelfID = "10002"
	if w := request(t, a, "PUT", "/api/qq/onebot", b, cookie); w.Code != 400 {
		t.Fatalf("offline account change accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestGroupHistoryKeepsPaginationInsideToolResultBudget(t *testing.T) {
	a := qqGroupTestApp(t)
	enableGroupHistory(t, a)
	for i := range 12 {
		in := qqGroupInbound()
		in.Mentioned = false
		in.MessageID = fmt.Sprintf("budget-%02d", i)
		in.Text = strings.Repeat("文字内容", 120)
		if err := a.Messaging.HandleIncoming(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now()
	ctx := agent.WithToolResultLimit(t.Context(), 4096)
	value, err := a.Messaging.GroupHistory(ctx, historySource(), cutoff, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	if len(raw) > 4096 || !strings.Contains(string(raw), "nextCursor") || !strings.Contains(string(raw), "hasMore\":true") {
		t.Fatalf("lost pagination or exceeded budget (%d): %s", len(raw), raw)
	}
	page := value.(map[string]any)["history"].(message.GroupHistoryPage)
	if len(page.Messages) == 0 {
		t.Fatal("page lost all messages")
	}
	next, err := a.Messaging.GroupHistory(ctx, historySource(), cutoff, map[string]any{"cursor": page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	second := next.(map[string]any)["history"].(message.GroupHistoryPage)
	if len(second.Messages) == 0 || second.Messages[0].ID == page.Messages[0].ID {
		t.Fatal("pagination repeated or skipped all records")
	}
	if _, err := a.Messaging.GroupHistory(agent.WithToolResultLimit(t.Context(), 1024), historySource(), cutoff, map[string]any{}); err == nil {
		t.Fatal("too-small observation budget silently truncated metadata")
	}
}
