package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xingexin/catbot/internal/domain"
	"github.com/xingexin/catbot/internal/message"
	"github.com/xingexin/catbot/internal/store"
)

func pluginNotificationRequest(a *App, token string, body map[string]any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/internal/plugin/notifications", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func seedNotificationPlugin(t *testing.T, a *App, id string, grants []string) string {
	t.Helper()
	key := id + "@1.0.0:test"
	if err := a.Store.Put(t.Context(), "plugin-version", key, domain.Plugin{ID: id, Grants: grants}); err != nil {
		t.Fatal(err)
	}
	return a.Token("plugin:" + key)
}

func TestPluginNotificationsRequireExplicitPermissionAndStableRecipient(t *testing.T) {
	a := testApp(t)
	body := map[string]any{"sessionId": "session", "text": "处理已完成", "operationId": "tool-op:notify"}
	for _, token := range []string{"invalid", a.Token("plugin:missing"), seedNotificationPlugin(t, a, "storage-only", []string{"storage"})} {
		w := pluginNotificationRequest(a, token, body)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("ungranted host notification accepted", w.Code, w.Body.String())
		}
	}
	token := seedNotificationPlugin(t, a, "notifier", []string{"notifications"})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			w := pluginNotificationRequest(a, token, body)
			if w.Code != 200 {
				t.Error(w.Code, w.Body.String())
			}
		})
	}
	wg.Wait()
	records, err := store.All[notification](t.Context(), a.Store, "notification")
	if err != nil || len(records) != 1 || records[0].Status != "saved" || records[0].PluginID != "notifier" || records[0].Attempts != 1 {
		t.Fatal("duplicate or untraceable plugin notification", records, err)
	}
	for _, changed := range []map[string]any{
		{"sessionId": "session", "text": "替换已有内容", "operationId": "tool-op:notify"},
		{"sessionId": "session", "text": "缺少操作ID"},
		{"sessionId": "session", "text": "   ", "operationId": "empty"},
		{"sessionId": "missing", "text": "未知会话", "operationId": "unknown-session"},
	} {
		if w := pluginNotificationRequest(a, token, changed); w.Code < 400 {
			t.Fatal("invalid notification accepted", w.Code)
		}
	}
	if err := a.Store.Put(t.Context(), "session", "background", domain.Session{ID: "background", Channel: "task"}); err != nil {
		t.Fatal(err)
	}
	if w := pluginNotificationRequest(a, token, map[string]any{"sessionId": "background", "text": "内部会话", "operationId": "background"}); w.Code < 400 {
		t.Fatal("internal task session accepted")
	}
	other := seedNotificationPlugin(t, a, "other", []string{"notifications"})
	if w := pluginNotificationRequest(a, other, body); w.Code != 200 {
		t.Fatal("plugin operation namespaces collided", w.Code, w.Body.String())
	}
	records, err = store.All[notification](t.Context(), a.Store, "notification")
	if err != nil || len(records) != 2 {
		t.Fatal(records, err)
	}
}

func TestPluginNotificationsUseCommonQQAuthorizationAndUncertainProtection(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unbound", true: "uncertain"}[allowed], func(t *testing.T) {
			var sends atomic.Int32
			a := outboxApp(t, func(context.Context, message.OutboundMessage) (message.SendResult, error) {
				sends.Add(1)
				return message.SendResult{Status: message.Uncertain}, errors.New("connection lost after dispatch")
			})
			if !allowed {
				c := a.channels["onebot"]
				c.Binding = func(context.Context) (ChannelBinding, error) {
					return ChannelBinding{}, nil
				}
				a.channels["onebot"] = c
			}
			token := seedNotificationPlugin(t, a, "notifier", []string{"notifications"})
			body := map[string]any{"sessionId": "recipient", "text": "检测到变化", "operationId": "monitor-change"}
			for range 2 {
				if w := pluginNotificationRequest(a, token, body); w.Code < 400 {
					t.Fatal("rejected delivery returned success", w.Code)
				}
			}
			a.reconcileNotifications(t.Context())
			wantCount, wantStatus := int32(0), "failed"
			if allowed {
				wantCount, wantStatus = 1, "uncertain"
			}
			records, err := store.All[notification](t.Context(), a.Store, "notification")
			if err != nil || len(records) != 1 || records[0].Status != wantStatus || sends.Load() != wantCount {
				t.Fatal("transport policy bypassed", records, sends.Load(), err)
			}
		})
	}
}
