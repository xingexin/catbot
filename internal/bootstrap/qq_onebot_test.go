package bootstrap

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	msgbiz "github.com/xingexin/catbot/internal/biz/messaging"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	message "github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/infra/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func setupOneBot(t *testing.T) *App {
	t.Helper()
	a := testApp(t)
	registerTestOneBot(t, a, "")
	b := msgbiz.OneBotBinding{Enabled: true, SelfID: "10001", AllowedUserIDs: []string{"20002"}, ConfigID: "config", PersonaID: "secretary"}
	if err := a.Store.Put(t.Context(), "qq-binding", "onebot", b); err != nil {
		t.Fatal(err)
	}
	return a
}

func oneBotEvent() map[string]any {
	return map[string]any{"time": time.Now().Unix(), "self_id": 10001, "user_id": 20002,
		"post_type": "message", "message_type": "private", "message_id": -12345,
		"message": []any{map[string]any{"type": "text", "data": map[string]string{"text": "请提醒我"}}}}
}

func postOneBot(a *App, event map[string]any, token string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(event)
	mac := hmac.New(sha1.New, []byte(token))
	_, _ = mac.Write(body)
	r := httptest.NewRequest("POST", "/qq/onebot/events", bytes.NewReader(body))
	r.Header.Set("X-Signature", "sha1="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func TestOneBotInboundBoundary(t *testing.T) {
	for _, tc := range []struct {
		name         string
		edit         func(map[string]any)
		badSignature bool
		status       int
	}{
		{"invalid signature", func(map[string]any) {}, true, 401},
		{"unbound peer", func(e map[string]any) { e["user_id"] = 30003 }, false, 200},
		{"different account", func(e map[string]any) { e["self_id"] = 99999 }, false, 200},
		{"group ignored", func(e map[string]any) { e["message_type"] = "group" }, false, 200},
		{"self echo", func(e map[string]any) { e["user_id"] = 10001 }, false, 200},
		{"sent message echo", func(e map[string]any) { e["post_type"] = "message_sent" }, false, 200},
		{"stale replay", func(e map[string]any) { e["time"] = time.Now().Add(-time.Hour).Unix() }, false, 200},
		{"missing timestamp", func(e map[string]any) { delete(e, "time") }, false, 200},
		{"empty text", func(e map[string]any) { e["message"] = "  " }, false, 200},
		{"missing ID", func(e map[string]any) { delete(e, "message_id") }, false, 200},
		{"CQ string", func(e map[string]any) { e["message"] = "[CQ:image,file=secret]" }, false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := setupOneBot(t)
			event := oneBotEvent()
			tc.edit(event)
			token := testOneBotToken
			if tc.badSignature {
				token = "wrong"
			}
			w := postOneBot(a, event, token)
			if w.Code != tc.status {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			runs, err := a.Store.List(t.Context(), "run")
			if err != nil || len(runs) != 0 {
				t.Fatalf("unexpected model execution: %d %v", len(runs), err)
			}
		})
	}
}

func TestOneBotConcurrentDedupAndAccountIsolation(t *testing.T) {
	a := setupOneBot(t)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if w := postOneBot(a, oneBotEvent(), testOneBotToken); w.Code != 200 {
				t.Error(w.Code, w.Body.String())
			}
		})
	}
	wg.Wait()
	runs, _ := store.All[conversation.Run](t.Context(), a.Store, "run")
	if len(runs) != 1 {
		t.Fatalf("duplicate runs: %d", len(runs))
	}
	var s conversation.Session
	if err := a.Store.Get(t.Context(), "session", runs[0].SessionID, &s); err != nil {
		t.Fatal(err)
	}
	if s.ChannelProvider != "onebot" || s.ChannelAccount != "10001" || s.Recipient != "20002" || runs[0].Prompt != "请提醒我" {
		t.Fatal(s, runs[0])
	}
	// Same peer and message ID on another account must form a separate conversation.
	b, _ := a.Messaging.OneBotBinding(t.Context())
	b.SelfID = "10002"
	_ = a.Store.Put(t.Context(), "qq-binding", "onebot", b)
	e := oneBotEvent()
	e["self_id"] = "10002"
	e["message"] = "string text"
	if w := postOneBot(a, e, testOneBotToken); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	runs, _ = store.All[conversation.Run](t.Context(), a.Store, "run")
	if len(runs) != 2 || runs[0].SessionID == runs[1].SessionID {
		t.Fatal("accounts shared a conversation")
	}
	b.Enabled = false
	_ = a.Store.Put(t.Context(), "qq-binding", "onebot", b)
	e["message_id"] = 999
	_ = postOneBot(a, e, testOneBotToken)
	runs, _ = store.All[conversation.Run](t.Context(), a.Store, "run")
	if len(runs) != 2 {
		t.Fatal("disabled binding accepted work")
	}
}

func oneBotServer(t *testing.T, a *App, account string, send http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testOneBotToken {
			t.Error("missing API bearer token")
		}
		switch r.URL.Path {
		case "/get_login_info":
			JSON(w, 200, map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"user_id": account, "nickname": "fixture"}})
		case "/get_status":
			JSON(w, 200, map[string]any{"status": "ok", "retcode": 0, "data": map[string]bool{"online": true, "good": true}})
		case "/send_private_msg":
			send(w, r)
		default:
			t.Error("unexpected action", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.Close)
	registerTestOneBot(t, a, s.URL)
	return s
}

func TestOneBotRealConversationPath(t *testing.T) {
	a := setupOneBot(t)
	var sends atomic.Int32
	oneBotServer(t, a, "10001", func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		var body struct {
			UserID  int64 `json:"user_id"`
			Message []struct {
				Type string            `json:"type"`
				Data map[string]string `json:"data"`
			} `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.UserID != 20002 || len(body.Message) != 1 || body.Message[0].Type != "text" || body.Message[0].Data["text"] != "已安排 [CQ:at,qq=all]" {
			t.Error("recipient or literal text lost", body)
		}
		JSON(w, 200, map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"message_id": -678}})
	})
	a.Conversation.Direct = execFunc(func(_ context.Context, r agent.Request, _ agent.Emit) (agent.Result, error) {
		if r.Persona.ID != "secretary" {
			t.Error("persona not propagated")
		}
		return agent.Result{Text: "已安排 [CQ:at,qq=all]"}, nil
	})
	a.Messaging.Notifier = a.Messaging.DeliverNotification
	if w := postOneBot(a, oneBotEvent(), testOneBotToken); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	runs, _ := store.All[conversation.Run](t.Context(), a.Store, "run")
	if _, err := a.Conversation.Execute(t.Context(), runs[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Messaging.SendMessage(t.Context(), runs[0].SessionID, "ignored duplicate", "reply:"+runs[0].ID); err != nil {
		t.Fatal(err)
	}
	var d message.Delivery
	_ = a.Store.Get(t.Context(), "delivery", "reply:"+runs[0].ID, &d)
	if sends.Load() != 1 || d.Provider != "onebot" || d.Status != "sent" || d.MessageID != "-678" {
		t.Fatal(sends.Load(), d)
	}
	// Task notifications use the same destination strategy, outside a reply window.
	if err := a.Messaging.SendMessage(t.Context(), runs[0].SessionID, "已安排 [CQ:at,qq=all]", "task:test"); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 2 {
		t.Fatal("background notification missing")
	}
}

func TestOneBotDeliveryFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name, response, want string
		code                 int
		close                bool
	}{
		{"rejected", `{"status":"failed","retcode":1200}`, "failed", 200, false},
		{"invalid json", "{", "uncertain", 200, false},
		{"missing receipt", `{"status":"ok","retcode":0,"data":{}}`, "uncertain", 200, false},
		{"queued", `{"status":"async","retcode":1}`, "uncertain", 200, false},
		{"bad token", "", "failed", 401, false},
		{"remote internal error", "", "uncertain", 500, false},
		{"broken connection", "", "uncertain", 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := setupOneBot(t)
			var sends atomic.Int32
			oneBotServer(t, a, "10001", func(w http.ResponseWriter, r *http.Request) {
				sends.Add(1)
				if tc.close {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.response))
			})
			s := conversation.Session{ID: "personal", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "10001", Recipient: "20002"}
			_ = a.Store.Put(t.Context(), "session", s.ID, s)
			if err := a.Messaging.SendMessage(t.Context(), s.ID, "hi", "same-op"); err == nil {
				t.Fatal("failure reported as success")
			}
			var d message.Delivery
			_ = a.Store.Get(t.Context(), "delivery", "same-op", &d)
			if d.Status != tc.want {
				t.Fatal(d)
			}
			_ = a.Messaging.SendMessage(t.Context(), s.ID, "hi", "same-op")
			if sends.Load() != 1 {
				t.Fatal("unsafe automatic resend")
			}
		})
	}
}

func TestOneBotBindingAndAccountGuard(t *testing.T) {
	a := setupOneBot(t)
	oneBotServer(t, a, "99999", func(http.ResponseWriter, *http.Request) { t.Error("send attempted on wrong account") })
	s := conversation.Session{ID: "personal", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "10001", Recipient: "20002"}
	_ = a.Store.Put(t.Context(), "session", s.ID, s)
	if err := a.Messaging.SendMessage(t.Context(), s.ID, "hi", "guard"); err == nil {
		t.Fatal("account mismatch ignored")
	}
	b, _ := a.Messaging.OneBotBinding(t.Context())
	if w := request(t, a, "PUT", "/api/qq/onebot", b, nil); w.Code != 401 {
		t.Fatal("binding did not require login")
	}
	login := request(t, a, "POST", "/api/login", map[string]string{"password": "test-password"}, nil)
	cookie := login.Result().Cookies()[0]
	if w := request(t, a, "PUT", "/api/qq/onebot", b, cookie); w.Code != 400 {
		t.Fatal("unverified account binding accepted")
	}
	b.SelfID = "99999"
	if w := request(t, a, "PUT", "/api/qq/onebot", b, cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := request(t, a, "GET", "/api/qq", nil, cookie); w.Code != 200 || strings.Contains(w.Body.String(), testOneBotToken) || !strings.Contains(w.Body.String(), `"online"`) {
		t.Fatal(w.Body.String())
	}
	// Disabled transports must be switchable off even while NapCat is unreachable.
	registerTestOneBot(t, a, "http://127.0.0.1:1")
	b.Enabled = false
	if w := request(t, a, "PUT", "/api/qq/onebot", b, cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.ChannelProvider = "unknown"
	_ = a.Store.Put(t.Context(), "session", s.ID, s)
	if err := a.Messaging.SendMessage(t.Context(), s.ID, "hi", "unsupported"); err == nil {
		t.Fatal("unknown strategy fell back")
	}
}
