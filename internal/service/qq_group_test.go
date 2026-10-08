package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentTest/internal/agent"
	"agentTest/internal/domain"
	"agentTest/internal/job"
	"agentTest/internal/message"
	"agentTest/internal/store"
)

func qqGroupTestApp(t *testing.T) *App {
	t.Helper()
	a := setupOneBot(t)
	p := domain.Persona{ID: "group-persona", Name: "群聊助理", Tools: []string{}}
	if err := a.Store.Put(t.Context(), "persona", p.ID, p); err != nil {
		t.Fatal(err)
	}
	b, err := a.oneBotBinding(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b.AllowedGroupIDs, b.GroupPersonaID = []string{"1128987429", "1128987430"}, p.ID
	if err := a.Store.Put(t.Context(), "qq-binding", "onebot", b); err != nil {
		t.Fatal(err)
	}
	return a
}

func qqGroupInbound() message.InboundMessage {
	return message.InboundMessage{Route: "onebot", Account: "10001", Peer: "20002", RoomID: "1128987429", Mentioned: true, MessageID: "-12345", Text: "群聊请求", ReceivedAt: time.Now().UTC()}
}

func TestQQGroupHostRequiresMentionAndRoomBinding(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*message.InboundMessage, *oneBotBinding)
	}{
		{"not mentioned", func(in *message.InboundMessage, _ *oneBotBinding) { in.Mentioned = false }},
		{"room not allowed even for private contact", func(in *message.InboundMessage, _ *oneBotBinding) { in.RoomID = "999999" }},
		{"different account", func(in *message.InboundMessage, _ *oneBotBinding) { in.Account = "10002" }},
		{"self message", func(in *message.InboundMessage, _ *oneBotBinding) { in.Peer = in.Account }},
		{"disabled binding", func(_ *message.InboundMessage, b *oneBotBinding) { b.Enabled = false }},
		{"groups removed", func(_ *message.InboundMessage, b *oneBotBinding) { b.AllowedGroupIDs = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := qqGroupTestApp(t)
			in := qqGroupInbound()
			b, _ := a.oneBotBinding(t.Context())
			tc.edit(&in, &b)
			if err := a.Store.Put(t.Context(), "qq-binding", "onebot", b); err != nil {
				t.Fatal(err)
			}
			if err := a.HandleIncoming(t.Context(), in); err != nil {
				t.Fatal("ignored group message returned an error", err)
			}
			if runs, err := a.Store.List(t.Context(), "run"); err != nil || len(runs) != 0 {
				t.Fatal("ignored group message queued work", len(runs), err)
			}
		})
	}
}

func TestQQGroupConcurrentDedupAndConversationIsolation(t *testing.T) {
	a := qqGroupTestApp(t)
	in := qqGroupInbound()
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := a.HandleIncoming(t.Context(), in); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	runs, err := store.All[domain.Run](t.Context(), a.Store, "run")
	if err != nil || len(runs) != 1 {
		t.Fatal("duplicate group event created more than one run", runs, err)
	}
	for _, edit := range []func(*message.InboundMessage){
		func(in *message.InboundMessage) { in.RoomID = ""; in.Mentioned = false },
		func(in *message.InboundMessage) { in.Peer = "30003" },
		func(in *message.InboundMessage) { in.RoomID = "1128987430" },
	} {
		other := in
		edit(&other)
		if err := a.HandleIncoming(t.Context(), other); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := a.oneBotBinding(t.Context())
	b.SelfID = "10002"
	if err := a.Store.Put(t.Context(), "qq-binding", "onebot", b); err != nil {
		t.Fatal(err)
	}
	other := in
	other.Account = b.SelfID
	if err := a.HandleIncoming(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	runs, err = store.All[domain.Run](t.Context(), a.Store, "run")
	if err != nil || len(runs) != 5 {
		t.Fatal("room, sender, account or direct message identities collided", runs, err)
	}
	seen := map[string]bool{}
	privateHash := sha256.Sum256([]byte("onebot:10001:20002"))
	privateID := "qq-onebot-" + hex.EncodeToString(privateHash[:16])
	for _, run := range runs {
		if seen[run.SessionID] {
			t.Fatal("messages shared conversation context", run.SessionID)
		}
		seen[run.SessionID] = true
		var s domain.Session
		if err := a.Store.Get(t.Context(), "session", run.SessionID, &s); err != nil {
			t.Fatal(err)
		}
		if s.ChannelRoom == "" {
			if s.ID != privateID || s.PersonaID != "secretary" || run.Persona.ID != "secretary" {
				t.Fatal("private chat identity or persona changed", s, run)
			}
		} else if s.PersonaID != "group-persona" || run.Persona.ID != "group-persona" || run.Persona.Tools == nil {
			t.Fatal("private persona leaked into group", s, run)
		}
	}
}

func TestQQGroupHistoryStaysWithinRoomAndSender(t *testing.T) {
	a := qqGroupTestApp(t)
	var delivered atomic.Int32
	c := a.channels["onebot"]
	c.Sender = senderFunc(func(context.Context, message.OutboundMessage) (message.SendResult, error) {
		delivered.Add(1)
		return message.SendResult{Status: message.Sent, MessageID: "fixture"}, nil
	})
	a.channels["onebot"], a.Notifier = c, a.DeliverNotification
	a.Direct = execFunc(func(_ context.Context, req agent.Request, _ agent.Emit) (agent.Result, error) {
		if strings.HasSuffix(req.Run.Prompt, "second") {
			want := strings.TrimSuffix(req.Run.Prompt, "second") + "first"
			if len(req.History) != 2 || req.History[0].Content != want || req.History[1].Content != "reply:"+want {
				t.Error("context crossed a room, sender or private chat", req.Run.Prompt, req.History)
			}
		} else if len(req.History) != 0 {
			t.Error("new group participant inherited history", req.History)
		}
		return agent.Result{Text: "reply:" + req.Run.Prompt}, nil
	})
	for _, round := range []string{"first", "second"} {
		for i, entry := range []struct{ room, peer string }{{"1128987429", "20002"}, {"1128987429", "30003"}, {"1128987430", "20002"}, {"", "20002"}} {
			in := qqGroupInbound()
			in.RoomID, in.Peer = entry.room, entry.peer
			in.MessageID, in.Text = fmt.Sprintf("%d-%s", i, round), fmt.Sprintf("%d-%s", i, round)
			if err := a.HandleIncoming(t.Context(), in); err != nil {
				t.Fatal(err)
			}
			runs, err := store.All[domain.Run](t.Context(), a.Store, "run")
			if err != nil {
				t.Fatal(err)
			}
			for _, run := range runs {
				if run.Status == "queued" {
					if _, err := a.Execute(t.Context(), run.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	if delivered.Load() != 8 {
		t.Fatal("missing conversation replies", delivered.Load())
	}
}

func TestQQGroupReceiveUsesExplicitActualPersona(t *testing.T) {
	a := qqGroupTestApp(t)
	in := qqGroupInbound()
	if err := a.HandleIncoming(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	runs, _ := store.All[domain.Run](t.Context(), a.Store, "run")
	var s domain.Session
	if err := a.Store.Get(t.Context(), "session", runs[0].SessionID, &s); err != nil {
		t.Fatal(err)
	}
	// Explicit per-conversation choices remain valid after changing the default.
	p := domain.Persona{ID: "different-group-persona", Tools: []string{}}
	if err := a.Store.Put(t.Context(), "persona", p.ID, p); err != nil {
		t.Fatal(err)
	}
	s.PersonaID = p.ID
	if err := a.Store.Put(t.Context(), "session", s.ID, s); err != nil {
		t.Fatal(err)
	}
	in.MessageID = "second"
	if err := a.HandleIncoming(t.Context(), in); err != nil {
		t.Fatal("valid conversation persona was replaced by the channel default", err)
	}
	p.Tools = nil
	if err := a.Store.Put(t.Context(), "persona", p.ID, p); err != nil {
		t.Fatal(err)
	}
	in.MessageID = "third"
	if err := a.HandleIncoming(t.Context(), in); err == nil {
		t.Fatal("group inherited unrestricted tools after persona edit")
	}
	runs, _ = store.All[domain.Run](t.Context(), a.Store, "run")
	if len(runs) != 2 {
		t.Fatal("unsafe group persona queued a run", len(runs))
	}
}

func TestQQGroupConversationAndTaskUseGroupTransport(t *testing.T) {
	a := qqGroupTestApp(t)
	var sends atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testOneBotToken {
			t.Error("missing transport authentication")
		}
		switch r.URL.Path {
		case "/get_login_info":
			JSON(w, 200, map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"user_id": 10001, "nickname": "fixture"}})
		case "/get_status":
			JSON(w, 200, map[string]any{"status": "ok", "retcode": 0, "data": map[string]bool{"online": true, "good": true}})
		case "/send_group_msg":
			count := sends.Add(1)
			var body struct {
				GroupID int64 `json:"group_id"`
				UserID  any   `json:"user_id"`
				Message []struct {
					Type string            `json:"type"`
					Data map[string]string `json:"data"`
				} `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.GroupID != 1128987429 || body.UserID != nil {
				t.Error("group response used a private destination", body)
			}
			if count == 1 {
				if len(body.Message) != 3 || body.Message[0].Type != "reply" || body.Message[0].Data["id"] != "-12345" {
					t.Error("group reply reference lost", body)
				}
			} else if len(body.Message) != 2 {
				t.Error("scheduled group message gained a reply reference", body)
			}
			at := len(body.Message) - 2
			if at < 0 || body.Message[at].Type != "at" || body.Message[at].Data["qq"] != "30003" || body.Message[len(body.Message)-1].Type != "text" {
				t.Error("group response did not mention its original participant", body)
			}
			JSON(w, 200, map[string]any{"status": "ok", "retcode": 0, "data": map[string]int{"message_id": 678}})
		default:
			t.Error("unexpected private or management action", r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()
	registerTestOneBot(t, a, srv.URL)
	a.Notifier = a.DeliverNotification
	a.Direct = execFunc(func(_ context.Context, req agent.Request, _ agent.Emit) (agent.Result, error) {
		if req.Run.Persona.ID != "group-persona" || len(req.Tools) != 0 {
			t.Error("group used the private persona or unrestricted tools", req.Run.Persona, req.Tools)
		}
		return agent.Result{Text: "群回复"}, nil
	})
	event := oneBotEvent()
	event["message_type"], event["group_id"], event["user_id"] = "group", 1128987429, 30003
	event["message"] = []any{
		map[string]any{"type": "at", "data": map[string]string{"qq": "10001"}},
		map[string]any{"type": "text", "data": map[string]string{"text": "群请求"}},
	}
	for range 2 {
		if w := postOneBot(a, event, testOneBotToken); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	runs, err := store.All[domain.Run](t.Context(), a.Store, "run")
	if err != nil || len(runs) != 1 {
		t.Fatal("duplicate group callback executed twice", runs, err)
	}
	if _, err := a.Execute(t.Context(), runs[0].ID); err != nil {
		t.Fatal(err)
	}
	snapshot := job.Snapshot{Task: domain.Task{ID: "group-task", SessionID: runs[0].SessionID}}
	for range 2 {
		if err := a.Notify(t.Context(), snapshot, "群提醒", "task-group-notify"); err != nil {
			t.Fatal(err)
		}
	}
	if sends.Load() != 2 {
		t.Fatal("reply or task notification duplicated", sends.Load())
	}
	// Removing the group authorizes neither a new reply nor a task notification.
	b, _ := a.oneBotBinding(t.Context())
	b.AllowedGroupIDs = []string{}
	if err := a.Store.Put(t.Context(), "qq-binding", "onebot", b); err != nil {
		t.Fatal(err)
	}
	if err := a.Notify(t.Context(), snapshot, "不得投递", "after-group-removal"); err == nil {
		t.Fatal("revoked group continued to receive messages")
	}
	var d delivery
	if err := a.Store.Get(t.Context(), "delivery", "after-group-removal", &d); err != nil || d.Status != "failed" || sends.Load() != 2 {
		t.Fatal("revoked group reached external transport or lost failure record", d, sends.Load(), err)
	}
}

func TestQQGroupBindingValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*oneBotBinding)
		accept bool
	}{
		{"groups only", func(b *oneBotBinding) { b.AllowedUserIDs = nil; b.PersonaID = "" }, true},
		{"both routes", func(*oneBotBinding) {}, true},
		{"private only remains compatible", func(b *oneBotBinding) { b.AllowedGroupIDs = nil; b.GroupPersonaID = "" }, true},
		{"no destinations", func(b *oneBotBinding) { b.AllowedUserIDs = nil; b.AllowedGroupIDs = nil }, false},
		{"no group persona", func(b *oneBotBinding) { b.GroupPersonaID = "" }, false},
		{"unknown group persona", func(b *oneBotBinding) { b.GroupPersonaID = "missing" }, false},
		{"unrestricted private persona", func(b *oneBotBinding) { b.GroupPersonaID = "secretary" }, false},
		{"bad group", func(b *oneBotBinding) { b.AllowedGroupIDs = []string{"0"} }, false},
		{"noncanonical group", func(b *oneBotBinding) { b.AllowedGroupIDs = []string{"00123"} }, false},
		{"too many groups", func(b *oneBotBinding) { b.AllowedGroupIDs = make([]string, 21) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := qqGroupTestApp(t)
			c := a.channels["onebot"]
			c.Status = nil // A replacement Sender need not expose management calls.
			a.channels["onebot"] = c
			b, _ := a.oneBotBinding(t.Context())
			tc.edit(&b)
			login := request(t, a, "POST", "/api/login", map[string]string{"password": "test-password"}, nil)
			cookie := login.Result().Cookies()[0]
			w := request(t, a, "PUT", "/api/qq/onebot", b, cookie)
			if (w.Code == 200) != tc.accept {
				t.Fatal("unexpected group binding result", w.Code, w.Body.String())
			}
			if tc.accept {
				got, err := a.oneBotBinding(t.Context())
				if err != nil || got.GroupPersonaID != b.GroupPersonaID || len(got.AllowedGroupIDs) != len(b.AllowedGroupIDs) {
					t.Fatal("group binding was not persisted", got, err)
				}
			}
		})
	}
}

func TestQQGroupMailWatchRequiresExplicitToolAndAllowedRoom(t *testing.T) {
	a := qqGroupTestApp(t)
	seedMailPlugin(t, a)
	s := domain.Session{ID: "group-mail", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "10001", ChannelRoom: "1128987429", Recipient: "30003", ConfigID: "config", PersonaID: "group-persona"}
	if err := a.Store.Put(t.Context(), "session", s.ID, s); err != nil {
		t.Fatal(err)
	}
	in := mailWatchRequest{SessionID: s.ID}
	if _, err := a.SaveMailWatch(t.Context(), in); err == nil {
		t.Fatal("group access automatically granted mailbox capability")
	}
	p := domain.Persona{ID: "group-persona", Tools: []string{"mail__watch"}}
	if err := a.Store.Put(t.Context(), "persona", p.ID, p); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveMailWatch(t.Context(), in); err != nil {
		t.Fatal("explicit group mailbox setup failed", err)
	}
	b, _ := a.oneBotBinding(t.Context())
	b.AllowedGroupIDs = nil
	if err := a.Store.Put(t.Context(), "qq-binding", "onebot", b); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveMailWatch(t.Context(), in); err == nil {
		t.Fatal("revoked group accepted mail notifications")
	}
}
