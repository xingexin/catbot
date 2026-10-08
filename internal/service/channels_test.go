package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/agent"
	"github.com/xingexin/catbot/internal/domain"
	"github.com/xingexin/catbot/internal/job"
	"github.com/xingexin/catbot/internal/message"
	"github.com/xingexin/catbot/internal/store"
	"github.com/xingexin/catbot/internal/transport/onebot"
	"github.com/xingexin/catbot/internal/transport/qqofficial"
)

const testOneBotToken = "private-onebot-fixture"
const testQQSecret = "test-qq-secret"

func testQQKey() ed25519.PrivateKey {
	seed := []byte(testQQSecret)
	for len(seed) < ed25519.SeedSize {
		seed = append(seed, seed...)
	}
	return ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
}

func registerTestOfficial(t *testing.T, a *App, endpoint string) {
	t.Helper()
	q := qqofficial.New(qqofficial.Options{AppID: a.Options.QQAppID, Secret: testQQSecret, BaseURL: endpoint}, QQTokenCache{Store: a.Store, Vault: a.Vault}, a.IncomingHandler("official"))
	// Tests replace dependencies before any requests or worker execution.
	delete(a.channels, "official")
	if err := a.RegisterChannel("official", Channel{Title: "QQ 官方私聊", Implementation: "QQ 官方 API", Sender: q, Status: q, Receive: http.HandlerFunc(q.Receive), Binding: a.OfficialChannelBinding}); err != nil {
		t.Fatal(err)
	}
}

func registerTestOneBot(t *testing.T, a *App, endpoint string) {
	t.Helper()
	q := onebot.New(onebot.Options{URL: endpoint, Token: testOneBotToken}, a.IncomingHandler("onebot"))
	delete(a.channels, "onebot")
	if err := a.RegisterChannel("onebot", Channel{Title: "QQ 个人号", Implementation: "test OneBot", Sender: q, Status: q, Receive: http.HandlerFunc(q.Receive), Binding: a.OneBotChannelBinding}); err != nil {
		t.Fatal(err)
	}
}

// A third party Sender has no health check, HTTP handler or service.App dependency.
type senderFunc func(context.Context, message.OutboundMessage) (message.SendResult, error)

func (f senderFunc) Send(ctx context.Context, in message.OutboundMessage) (message.SendResult, error) {
	return f(ctx, in)
}

func postOfficial(a *App, id, text, peer string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"op": 0, "t": "C2C_MESSAGE_CREATE", "d": map[string]any{"id": id, "content": text, "author": map[string]string{"user_openid": peer}}})
	r := httptest.NewRequest("POST", "/qq/webhook", bytes.NewReader(body))
	r.Header.Set("X-Signature-Timestamp", "1725442341")
	r.Header.Set("X-Signature-Ed25519", hex.EncodeToString(ed25519.Sign(testQQKey(), append([]byte("1725442341"), body...))))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func TestConversationAndTaskNotificationAcrossSenders(t *testing.T) {
	for _, implementation := range []string{"official", "onebot", "custom"} {
		t.Run(implementation, func(t *testing.T) {
			a := setupOneBot(t)
			route := "onebot"
			var sends atomic.Int32
			var customMessages []message.OutboundMessage
			switch implementation {
			case "official":
				route = "official"
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sends.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["content"] == "reply" && body["msg_id"] != "first" {
						t.Error("reply reference lost", body)
					}
					if body["content"] == "scheduled" && body["msg_id"] != nil {
						t.Error("task used reply window", body)
					}
					JSON(w, 200, map[string]string{"id": "official-message"})
				}))
				t.Cleanup(server.Close)
				registerTestOfficial(t, a, server.URL)
				if err := (QQTokenCache{Store: a.Store, Vault: a.Vault}).Put(t.Context(), "fixture-token", time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			case "onebot":
				oneBotServer(t, a, "10001", func(w http.ResponseWriter, r *http.Request) {
					sends.Add(1)
					JSON(w, 200, map[string]any{"status": "ok", "retcode": 0, "data": map[string]int{"message_id": 678}})
				})
			case "custom":
				// Only swap the dependency; bindings, receiver, conversation and job code stay identical.
				c := a.channels[route]
				c.Sender = senderFunc(func(ctx context.Context, in message.OutboundMessage) (message.SendResult, error) {
					var d delivery
					if err := a.Store.Get(ctx, "delivery", in.OperationID, &d); err != nil || d.Status != "uncertain" {
						t.Error("intent not persisted before send", err, d)
					}
					sends.Add(1)
					customMessages = append(customMessages, in)
					return message.SendResult{Status: message.Sent, MessageID: "custom-message"}, nil
				})
				c.Status, c.Implementation = nil, "self-built"
				delete(a.channels, route)
				if err := a.RegisterChannel(route, c); err != nil {
					t.Fatal(err)
				}
			}
			a.Notifier = a.DeliverNotification
			a.Direct = execFunc(func(_ context.Context, r agent.Request, _ agent.Emit) (agent.Result, error) {
				if r.Run.Persona.ID != "secretary" || r.Run.Config.ID != "config" {
					t.Error("common execution configuration lost")
				}
				return agent.Result{Text: "reply"}, nil
			})
			var response *httptest.ResponseRecorder
			if route == "official" {
				response = postOfficial(a, "first", "hello", "bound-user")
			} else {
				response = postOneBot(a, oneBotEvent(), testOneBotToken)
			}
			if response.Code != 200 {
				t.Fatal(response.Body.String())
			}
			runs, err := store.All[domain.Run](t.Context(), a.Store, "run")
			if err != nil || len(runs) != 1 {
				t.Fatal(runs, err)
			}
			run := runs[0]
			if _, err := a.Execute(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			if err := a.SendMessage(t.Context(), run.SessionID, "duplicate", "reply:"+run.ID); err != nil {
				t.Fatal(err)
			}
			snapshot := job.Snapshot{Task: domain.Task{ID: "task", SessionID: run.SessionID}}
			for range 2 {
				if err := a.Notify(t.Context(), snapshot, "scheduled", "task:stable"); err != nil {
					t.Fatal(err)
				}
			}
			if sends.Load() != 2 {
				t.Fatal("duplicate/missing replies or scheduled notifications", sends.Load())
			}
			var session domain.Session
			if err := a.Store.Get(t.Context(), "session", run.SessionID, &session); err != nil {
				t.Fatal(err)
			}
			if session.ChannelProvider != route || len(session.Messages) != 2 || session.Messages[1].Content != "reply" {
				t.Fatal("conversation history lost", session)
			}
			if implementation == "custom" {
				if len(customMessages) != 2 || customMessages[0].ReplyTo == nil || customMessages[1].ReplyTo != nil || customMessages[1].Account != "10001" || customMessages[1].Peer != "20002" {
					t.Fatal(customMessages)
				}
			}
		})
	}
}

func TestCustomSenderFailureAndConcurrentDedup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result message.SendResult
		err    error
		want   string
	}{
		{"sent", message.SendResult{Status: message.Sent}, nil, "sent"},
		{"failed", message.SendResult{Status: message.Failed}, errors.New("rejected"), "failed"},
		{"unknown", message.SendResult{}, errors.New("disconnected"), "uncertain"},
		{"contradictory", message.SendResult{Status: message.Sent}, errors.New("disconnected"), "uncertain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := setupOneBot(t)
			var count atomic.Int32
			c := a.channels["onebot"]
			c.Sender = senderFunc(func(context.Context, message.OutboundMessage) (message.SendResult, error) {
				count.Add(1)
				return tc.result, tc.err
			})
			a.channels["onebot"] = c
			s := domain.Session{ID: "existing", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "10001", Recipient: "20002"}
			if err := a.Store.Put(t.Context(), "session", s.ID, s); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for range 12 {
				wg.Go(func() {
					err := a.SendMessage(t.Context(), s.ID, "text", "stable")
					if (err == nil) != (tc.want == "sent") {
						t.Error("wrong outcome", err)
					}
				})
			}
			wg.Wait()
			var d delivery
			if err := a.Store.Get(t.Context(), "delivery", "stable", &d); err != nil || d.Status != tc.want || count.Load() != 1 {
				t.Fatal(d, count.Load(), err)
			}
			b, _ := a.oneBotBinding(t.Context())
			b.Enabled = false
			if err := a.Store.Put(t.Context(), "qq-binding", "onebot", b); err != nil {
				t.Fatal(err)
			}
			if err := a.SendMessage(t.Context(), s.ID, "text", "new-op"); err == nil || count.Load() != 1 {
				t.Fatal("custom sender bypassed host binding", err)
			}
		})
	}
}

func TestIndependentStatusAndBindingForCustomSender(t *testing.T) {
	a := setupOneBot(t)
	c := a.channels["onebot"]
	c.Status = nil
	c.Implementation = "custom"
	c.Sender = senderFunc(func(context.Context, message.OutboundMessage) (message.SendResult, error) {
		t.Fatal("management check must not send a message")
		return message.SendResult{}, nil
	})
	a.channels["onebot"] = c
	login := request(t, a, "POST", "/api/login", map[string]string{"password": "test-password"}, nil)
	cookie := login.Result().Cookies()[0]
	b, _ := a.oneBotBinding(t.Context())
	if w := request(t, a, "PUT", "/api/qq/onebot", b, cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s := a.channelStatus(t.Context(), "onebot"); s.State != "unknown" || s.Implementation != "custom" {
		t.Fatal(s)
	}
	delete(a.channels, "onebot")
	if w := postOneBot(a, oneBotEvent(), testOneBotToken); w.Code != 503 {
		t.Fatal("missing adapter panicked or acknowledged")
	}
}

func TestOfficialUnboundContactIsIgnoredByHost(t *testing.T) {
	a := testApp(t)
	if w := postOfficial(a, "ignored", "hello", "unbound"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if runs, err := a.Store.List(t.Context(), "run"); err != nil || len(runs) != 0 {
		t.Fatal("unbound contact reached agent", err)
	}
}
