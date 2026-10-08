package bootstrap

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/xingexin/catbot/internal/config"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeScheduler struct {
	mu         sync.Mutex
	applied    []taskentity.Task
	fail       bool
	operations map[string]string
}

func (f *fakeScheduler) Apply(_ context.Context, t taskentity.Task, _ *taskentity.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, t)
	if f.fail {
		return errors.New("fixture unavailable")
	}
	return nil
}
func (f *fakeScheduler) Trigger(_ context.Context, t taskentity.Task, op string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.operations == nil {
		f.operations = map[string]string{}
	}
	f.operations[op] = "execution-" + op
	return f.operations[op], nil
}
func (f *fakeScheduler) Cancel(context.Context, taskentity.Task) error { return nil }
func (f *fakeScheduler) Ping(context.Context) error                    { return nil }

type execFunc func(context.Context, agent.Request, agent.Emit) (agent.Result, error)

func (f execFunc) Run(ctx context.Context, r agent.Request, emit agent.Emit) (agent.Result, error) {
	return f(ctx, r, emit)
}
func testApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	if err := os.MkdirAll(plugins, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := New(store.NewMemory(), config.Options{DataDir: root, PluginDir: plugins, MasterKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), AdminPassword: "test-password", RuntimeToken: "test-runtime", QQUser: "bound-user", QQConfigID: "config"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	registerTestOfficial(t, a, "")
	if err := a.Bootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	c := agent.Config{ID: "config", Name: "fixture", Kind: "api", Model: "fixture", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:1", MaxSteps: 3, TimeoutSec: 5, Capabilities: agent.Capabilities{Tools: true}}
	if err := a.Store.Put(t.Context(), "config", c.ID, c); err != nil {
		t.Fatal(err)
	}
	s := conversation.Session{ID: "session", ConfigID: c.ID, PersonaID: "secretary", Messages: []agent.Message{}, Native: map[string]string{}}
	if err := a.Store.Put(t.Context(), "session", s.ID, s); err != nil {
		t.Fatal(err)
	}
	a.Tasks.Scheduler = &fakeScheduler{}
	return a
}
func request(t *testing.T, a *App, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func TestLoginAndSecretRedaction(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	if w := request(t, a, "GET", "/api/configs", nil, nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(t, a, "POST", "/api/login", map[string]string{"password": "bad"}, nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	login := request(t, a, "POST", "/api/login", map[string]string{"password": "test-password"}, nil)
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe login cookie")
	}
	created := request(t, a, "POST", "/api/secrets", map[string]string{"name": "key", "value": "DO_NOT_EXPOSE"}, cookie)
	if created.Code != 200 {
		t.Fatal(created.Body.String())
	}
	list := request(t, a, "GET", "/api/secrets", nil, cookie)
	if strings.Contains(list.Body.String(), "DO_NOT_EXPOSE") || strings.Contains(list.Body.String(), "ciphertext") {
		t.Fatal("secret leaked")
	}
	r := httptest.NewRequest("POST", "http://local/api/configs", strings.NewReader("{}"))
	r.Header.Set("Origin", "http://evil")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin mutation permitted")
	}
}
func TestQQSignatureBindingAndDuplicate(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	body := []byte(`{"op":0,"t":"C2C_MESSAGE_CREATE","d":{"id":"event1","content":"提醒","author":{"user_openid":"bound-user"}}}`)
	timestamp := "1725442341"
	signature := hex.EncodeToString(ed25519.Sign(testQQKey(), append([]byte(timestamp), body...)))
	send := func(sig string) int {
		r := httptest.NewRequest("POST", "/qq/webhook", bytes.NewReader(body))
		r.Header.Set("X-Signature-Timestamp", timestamp)
		r.Header.Set("X-Signature-Ed25519", sig)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if send("bad") != 401 {
		t.Fatal("bad signature accepted")
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if code := send(signature); code != 200 {
				t.Errorf("callback status %d", code)
			}
		})
	}
	wg.Wait()
	runs, err := store.All[conversation.Run](t.Context(), a.Store, "run")
	if err != nil || len(runs) != 1 {
		t.Fatalf("duplicate created %d runs: %v", len(runs), err)
	}
	if !strings.HasPrefix(runs[0].SessionID, "qq-") {
		t.Fatal("wrong session binding")
	}
}
func TestConversationSerialAndCancelQueued(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	var mu sync.Mutex
	inflight := map[string]int{}
	a.Conversation.Direct = execFunc(func(ctx context.Context, r agent.Request, _ agent.Emit) (agent.Result, error) {
		mu.Lock()
		inflight[r.SessionID]++
		if inflight[r.SessionID] > 1 {
			t.Error("same session executed concurrently")
		}
		mu.Unlock()
		defer func() { mu.Lock(); inflight[r.SessionID]--; mu.Unlock() }()
		select {
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
		return agent.Result{Text: "answer:" + r.Prompt}, nil
	})
	cancelled, err := a.Conversation.Submit(t.Context(), "session", "cancel me", "cancel")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Conversation.CancelRun(t.Context(), cancelled.ID); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		if _, err := a.Conversation.Submit(t.Context(), "session", string(rune('a'+i)), idgen.New()); err != nil {
			t.Fatal(err)
		}
	}
	a.Start()
	deadline := time.After(5 * time.Second)
	for {
		var s conversation.Session
		if err := a.Store.Get(t.Context(), "session", "session", &s); err != nil {
			t.Fatal(err)
		}
		if len(s.Messages) == 12 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("queued messages did not finish")
		case <-time.After(20 * time.Millisecond):
		}
	}
	var s conversation.Session
	_ = a.Store.Get(t.Context(), "session", "session", &s)
	for i := 0; i < 12; i += 2 {
		if s.Messages[i+1].Content != "answer:"+s.Messages[i].Content {
			t.Fatal("history overwritten")
		}
	}
	var r conversation.Run
	_ = a.Store.Get(t.Context(), "run", cancelled.ID, &r)
	if r.Status != "cancelled" {
		t.Fatal(r.Status)
	}
}
func TestPersonaSnapshotAndSDKStrategySwitch(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	var c agent.Config
	_ = a.Store.Get(t.Context(), "config", "config", &c)
	c.Kind = "sdk"
	c.Provider = "claude"
	_ = a.Store.Put(t.Context(), "config", c.ID, c)
	received := []string{}
	a.Conversation.SDK = execFunc(func(_ context.Context, r agent.Request, _ agent.Emit) (agent.Result, error) {
		received = append(received, r.NativeID)
		return agent.Result{Text: r.Persona.SystemPrompt, NativeID: "native"}, nil
	})
	for i := range 3 {
		r, err := a.Conversation.Submit(t.Context(), "session", "hello", idgen.New())
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			var p persona.Persona
			_ = a.Store.Get(t.Context(), "persona", "secretary", &p)
			p.SystemPrompt = "new-persona"
			p.Version++
			_ = a.Store.Put(t.Context(), "persona", p.ID, p)
		}
		done, err := a.Conversation.Execute(t.Context(), r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && done.Result == "new-persona" {
			t.Fatal("running snapshot changed")
		}
	}
	if received[0] != "" || received[1] != "" || received[2] != "native" {
		t.Fatalf("native session mapping %v", received)
	}
}
func TestTaskControlsAndReconcile(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	f := a.Tasks.Scheduler.(*fakeScheduler)
	task := taskentity.Task{Name: "test", Kind: "recurring", Cron: "0 9 * * *", ConfigID: "config", PersonaID: "secretary", Steps: []taskentity.Step{{ID: "one", Kind: "agent", Prompt: "hello"}}}
	created, err := a.Tasks.SaveTask(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Tasks.ControlTask(t.Context(), created.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	var paused taskentity.Task
	_ = a.Store.Get(t.Context(), "task", created.ID, &paused)
	if !paused.Paused || paused.Revision != 2 {
		t.Fatal(paused)
	}
	f.fail = true
	paused.Cron = "0 10 * * *"
	failed, err := a.Tasks.SaveTask(t.Context(), paused)
	if err == nil || failed.Status != "error" {
		t.Fatal("expected provisioning failure")
	}
	f.fail = false
	a.Tasks.Reconcile(t.Context())
	var repaired taskentity.Task
	_ = a.Store.Get(t.Context(), "task", created.ID, &repaired)
	if repaired.Status != "paused" || repaired.Cron != "0 10 * * *" {
		t.Fatal(repaired)
	}
	if intents, _ := a.Store.List(t.Context(), "schedule-intent"); len(intents) != 0 {
		t.Fatal("intent not cleared")
	}
}
