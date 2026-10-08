package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"agentTest/internal/agent"
	"agentTest/internal/domain"
	"agentTest/internal/job"
	"agentTest/internal/message"
	"agentTest/internal/store"
)

// These are host-contract tests with a fake scheduler and Sender, not live
// IMAP or QQ tests. The real mail manifest supplies the permission contract.
func seedMailPlugin(t *testing.T, a *App) domain.Plugin {
	t.Helper()
	b, err := os.ReadFile("../../plugins/mail/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest domain.Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	p := domain.Plugin{ID: "mail", Enabled: true, Manifest: manifest, Grants: []string{"mail.read", "storage"}}
	if err := a.Store.Put(t.Context(), "plugin", p.ID, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMailWatchSetupKeepsIdentityAndRoutesOnlyChangedNotifications(t *testing.T) {
	t.Parallel()
	a := setupOneBot(t)
	seedMailPlugin(t, a)
	a.Direct = execFunc(func(context.Context, agent.Request, agent.Emit) (agent.Result, error) {
		t.Fatal("mail polling must not invoke a model")
		return agent.Result{}, nil
	})
	session := domain.Session{ID: "mail-recipient", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "10001", Recipient: "20002", ConfigID: "config", PersonaID: "secretary"}
	if err := a.Store.Put(t.Context(), "session", session.ID, session); err != nil {
		t.Fatal(err)
	}
	var sent []message.OutboundMessage
	channel := a.channels["onebot"]
	channel.Sender = senderFunc(func(ctx context.Context, in message.OutboundMessage) (message.SendResult, error) {
		var intent notification
		if err := a.Store.Get(ctx, "notification", "notification-"+in.OperationID, &intent); err != nil || intent.Status != "pending" {
			t.Error("notification intent missing before external send", err, intent)
		}
		sent = append(sent, in)
		return message.SendResult{Status: message.Sent, MessageID: "fake-qq-receipt"}, nil
	})
	a.channels["onebot"] = channel
	a.Notifier = a.DeliverNotification
	in := mailWatchRequest{SessionID: session.ID}
	task, err := a.SaveMailWatch(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if task.Cron != "*/5 * * * *" || task.Steps[0].Arguments["monitorId"] != task.ID || task.ConfigID != session.ConfigID || task.PersonaID != session.PersonaID || !task.Notify {
		t.Fatal("incomplete watch configuration", task)
	}
	repeated, err := a.SaveMailWatch(t.Context(), in)
	if err != nil || repeated.ID != task.ID {
		t.Fatal("repeated creation changed watch identity", repeated, err)
	}
	in.ID, in.IntervalMinutes = task.ID, 10
	updated, err := a.SaveMailWatch(t.Context(), in)
	if err != nil || updated.ID != task.ID || updated.Steps[0].Arguments["monitorId"] != task.ID || updated.Cron != "*/10 * * * *" {
		t.Fatal("updating frequency lost watch identity", updated, err)
	}
	all, err := store.All[domain.Task](t.Context(), a.Store, "task")
	if err != nil || len(all) != 1 {
		t.Fatal("duplicate watch persisted", all, err)
	}
	scheduler := a.Scheduler.(*fakeScheduler)
	if len(scheduler.operations) != 1 || scheduler.operations["initialize:"+task.ID] == "" {
		t.Fatal("initial baseline not submitted once", scheduler.operations)
	}
	for _, changed := range []bool{false, true} {
		text, shouldSend, err := job.Notification(updated, "completed", "", map[string]any{"watch": map[string]any{"changed": changed, "notificationText": "新邮件：面试邀请"}})
		if err != nil || shouldSend != changed {
			t.Fatal(text, shouldSend, err)
		}
		if shouldSend {
			for range 2 {
				if err := a.Notify(t.Context(), job.Snapshot{Task: updated}, text, "mail-execution-notify"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if len(sent) != 1 || sent[0].Peer != session.Recipient || sent[0].Text != "新邮件：面试邀请" {
		t.Fatal("quiet result sent, changed result lost, or recipient changed", sent)
	}
}

func TestMailWatchRejectsUnavailableCapabilitiesAndUnauthorizedTargets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*App, *domain.Plugin, *domain.Session, *mailWatchRequest)
	}{
		{"disabled plugin", func(_ *App, p *domain.Plugin, _ *domain.Session, _ *mailWatchRequest) { p.Enabled = false }},
		{"missing read permission", func(_ *App, p *domain.Plugin, _ *domain.Session, _ *mailWatchRequest) { p.Grants = []string{"storage"} }},
		{"missing storage permission", func(_ *App, p *domain.Plugin, _ *domain.Session, _ *mailWatchRequest) {
			p.Grants = []string{"mail.read"}
		}},
		{"missing session", func(_ *App, _ *domain.Plugin, _ *domain.Session, in *mailWatchRequest) { in.SessionID = "missing" }},
		{"no recipient session", func(_ *App, _ *domain.Plugin, _ *domain.Session, in *mailWatchRequest) { in.SessionID = "" }},
		{"unauthorized QQ peer", func(_ *App, _ *domain.Plugin, s *domain.Session, _ *mailWatchRequest) { s.Recipient = "30003" }},
		{"wrong QQ account", func(_ *App, _ *domain.Plugin, s *domain.Session, _ *mailWatchRequest) { s.ChannelAccount = "99999" }},
		{"missing transport", func(_ *App, _ *domain.Plugin, s *domain.Session, _ *mailWatchRequest) { s.ChannelProvider = "absent" }},
		{"unsupported interval", func(_ *App, _ *domain.Plugin, _ *domain.Session, in *mailWatchRequest) { in.IntervalMinutes = 7 }},
		{"persona denies tool", func(a *App, _ *domain.Plugin, _ *domain.Session, _ *mailWatchRequest) {
			p := domain.Persona{ID: "secretary", Tools: []string{"example__echo"}}
			if err := a.Store.Put(context.Background(), "persona", p.ID, p); err != nil {
				panic(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := setupOneBot(t)
			plugin := seedMailPlugin(t, a)
			s := domain.Session{ID: "target", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "10001", Recipient: "20002", ConfigID: "config", PersonaID: "secretary"}
			in := mailWatchRequest{SessionID: s.ID}
			tc.edit(a, &plugin, &s, &in)
			if err := a.Store.Put(t.Context(), "plugin", plugin.ID, plugin); err != nil {
				t.Fatal(err)
			}
			if err := a.Store.Put(t.Context(), "session", s.ID, s); err != nil {
				t.Fatal(err)
			}
			if _, err := a.SaveMailWatch(t.Context(), in); err == nil {
				t.Fatal("invalid watcher accepted")
			}
			tasks, err := store.All[domain.Task](t.Context(), a.Store, "task")
			if err != nil || len(tasks) != 0 {
				t.Fatal("invalid task persisted", tasks, err)
			}
		})
	}
}

func TestMailAndVideoModelChecksRunBeforePluginConfiguration(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	seedMailPlugin(t, a)
	for _, config := range []domain.Config{
		{ID: "sdk", Kind: "sdk", Provider: "codebuddy"},
		{ID: "vision", Kind: "api", Protocol: "openai-chat", Capabilities: domain.Capabilities{Images: true}},
		{ID: "anthropic", Kind: "api", Protocol: "anthropic"},
	} {
		if err := a.Store.Put(t.Context(), "config", config.ID, config); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name    string
		plugin  string
		config  map[string]any
		wantErr bool
	}{
		{"optional summary", "mail", nil, false},
		{"API summary", "mail", map[string]any{"summaryConfigId": "config"}, false},
		{"missing summary", "mail", map[string]any{"summaryConfigId": "missing"}, true},
		{"SDK summary", "mail", map[string]any{"summaryConfigId": "sdk"}, true},
		{"vision without images", "video", map[string]any{"visionConfigId": "config"}, true},
		{"vision with images", "video", map[string]any{"visionConfigId": "vision"}, false},
		{"unsupported transcription", "video", map[string]any{"transcriptionConfigId": "anthropic"}, true},
		{"OpenAI transcription", "video", map[string]any{"transcriptionConfigId": "config"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := a.validatePluginModels(t.Context(), tc.plugin, tc.config); (err != nil) != tc.wantErr {
				t.Fatal(err)
			}
		})
	}
	login := request(t, a, "POST", "/api/login", map[string]string{"password": "test-password"}, nil)
	cookie := login.Result().Cookies()[0]
	w := request(t, a, "POST", "/api/plugins/mail/configure", map[string]any{"config": map[string]any{"host": "imap.example.test", "username": "test@example.test", "password": "fixture-private", "summaryConfigId": "sdk"}, "grants": []string{"mail.read", "storage"}}, cookie)
	if w.Code < 400 || !strings.Contains(w.Body.String(), "API") {
		t.Fatal("invalid model accepted through HTTP", w.Code, w.Body.String())
	}
	var unchanged domain.Plugin
	if err := a.Store.Get(t.Context(), "plugin", "mail", &unchanged); err != nil || len(unchanged.Secrets) != 0 {
		t.Fatal("rejected config persisted a credential", unchanged, err)
	}
}

func TestNotificationsKeepDeliveryOutcomesAndOnlyRetryKnownFailures(t *testing.T) {
	t.Parallel()
	for _, outcome := range []message.SendStatus{message.Sent, message.Failed, message.Uncertain} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			a := setupOneBot(t)
			s := domain.Session{ID: "recipient", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "10001", Recipient: "20002"}
			if err := a.Store.Put(t.Context(), "session", s.ID, s); err != nil {
				t.Fatal(err)
			}
			var sends atomic.Int32
			var current atomic.Value
			current.Store(outcome)
			channel := a.channels["onebot"]
			channel.Sender = senderFunc(func(context.Context, message.OutboundMessage) (message.SendResult, error) {
				sends.Add(1)
				status := current.Load().(message.SendStatus)
				if status != message.Sent {
					return message.SendResult{Status: status}, errors.New("fake delivery rejected or disconnected")
				}
				return message.SendResult{Status: status, MessageID: "fake-delivery"}, nil
			})
			a.channels["onebot"] = channel
			a.Notifier = a.DeliverNotification
			snapshot := job.Snapshot{Task: domain.Task{ID: "watcher", SessionID: s.ID}}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					err := a.Notify(t.Context(), snapshot, "new mail", "stable-notification")
					if (err == nil) != (outcome == message.Sent) {
						t.Error("unexpected notify outcome", err)
					}
				})
			}
			wg.Wait()
			var n notification
			if err := a.Store.Get(t.Context(), "notification", "notification-stable-notification", &n); err != nil || n.Status != string(outcome) || sends.Load() != 1 {
				t.Fatal(n, sends.Load(), err)
			}
			firstOperation := n.OperationID
			current.Store(message.Sent)
			login := request(t, a, "POST", "/api/login", map[string]string{"password": "test-password"}, nil)
			cookie := login.Result().Cookies()[0]
			w := request(t, a, "POST", "/api/notifications/"+n.ID+"/retry", map[string]any{}, cookie)
			if outcome == message.Failed {
				if w.Code != 200 || sends.Load() != 2 {
					t.Fatal(w.Code, w.Body.String(), sends.Load())
				}
				if err := a.Store.Get(t.Context(), "notification", n.ID, &n); err != nil || n.Status != "sent" || n.OperationID == firstOperation {
					t.Fatal(n, err)
				}
			} else if w.Code < 400 || sends.Load() != 1 {
				t.Fatal("sent or uncertain notification retried", w.Code, sends.Load())
			}
		})
	}
}

func TestWebNotificationsPersistWithoutTransport(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	a.Notifier = func(context.Context, NotificationDelivery) error {
		t.Fatal("Web notification used QQ transport")
		return nil
	}
	snapshot := job.Snapshot{Task: domain.Task{ID: "watch", SessionID: "session"}}
	for range 2 {
		if err := a.Notify(t.Context(), snapshot, "new mail", "web-notification"); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.All[notification](t.Context(), a.Store, "notification")
	if err != nil || len(all) != 1 || all[0].Status != "saved" || all[0].Attempts != 1 {
		t.Fatal(all, err)
	}
}

func TestMailTaskNotificationReferencesRequireOneKnownStepExpression(t *testing.T) {
	t.Parallel()
	for _, expression := range []string{
		"literal text",
		"${steps.missing.changed}",
		"${steps.watch.notificationText}${steps.watch.changed}",
	} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			seedMailPlugin(t, a)
			task, err := a.SaveMailWatch(t.Context(), mailWatchRequest{SessionID: "session"})
			if err != nil {
				t.Fatal(err)
			}
			task.NotifyText = expression
			if _, err := a.SaveTask(t.Context(), task); err == nil {
				t.Fatal("malformed notification reference accepted", expression)
			}
		})
	}
}
