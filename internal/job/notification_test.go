package job

import (
	"strings"
	"testing"

	"github.com/xingexin/catbot/internal/domain"
)

func TestNotificationSelectsReadableMailChangesAndNeverHidesFailure(t *testing.T) {
	t.Parallel()
	task := domain.Task{Name: "新邮件提醒", NotifyWhen: "${steps.watch.changed}", NotifyText: "${steps.watch.notificationText}"}
	for _, tc := range []struct {
		name    string
		status  string
		failure string
		result  map[string]any
		want    string
		send    bool
		wantErr bool
	}{
		{name: "quiet baseline", status: "completed", result: map[string]any{"changed": false}, send: false},
		{name: "new mail", status: "completed", result: map[string]any{"changed": true, "notificationText": "收到新邮件：面试邀请", "messages": []any{"private full body"}}, want: "收到新邮件：面试邀请", send: true},
		{name: "failed quiet condition", status: "failed", failure: "IMAP login rejected", result: map[string]any{"changed": false}, want: "IMAP login rejected", send: true},
		{name: "interrupted before result", status: "interrupted", failure: "worker stopped", want: "worker stopped", send: true},
		{name: "missing condition", status: "completed", result: map[string]any{"notificationText": "hello"}, wantErr: true},
		{name: "string false is invalid", status: "completed", result: map[string]any{"changed": "false", "notificationText": "hello"}, wantErr: true},
		{name: "number condition is invalid", status: "completed", result: map[string]any{"changed": 1, "notificationText": "hello"}, wantErr: true},
		{name: "missing notification", status: "completed", result: map[string]any{"changed": true}, wantErr: true},
		{name: "empty notification", status: "completed", result: map[string]any{"changed": true, "notificationText": "  "}, wantErr: true},
		{name: "object notification", status: "completed", result: map[string]any{"changed": true, "notificationText": map[string]any{"text": "hello"}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text, send, err := Notification(task, tc.status, tc.failure, map[string]any{"watch": tc.result})
			if (err != nil) != tc.wantErr || send != tc.send {
				t.Fatalf("text=%q send=%v error=%v", text, send, err)
			}
			if tc.want != "" && !strings.Contains(text, tc.want) {
				t.Fatalf("notification %q does not contain %q", text, tc.want)
			}
			if strings.Contains(text, "private full body") {
				t.Fatal("unselected mail body leaked into the notification")
			}
		})
	}
}

func TestNotificationUsesLastStepHumanTextWithoutExplicitTemplate(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"notificationText", "text"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			task := domain.Task{Name: "邮箱摘要", Steps: []domain.Step{{ID: "summary"}}}
			text, send, err := Notification(task, "completed", "", map[string]any{"summary": map[string]any{field: "今日有两封重要邮件", "raw": "private"}})
			if err != nil || !send || text != "今日有两封重要邮件" {
				t.Fatal(text, send, err)
			}
		})
	}
}
