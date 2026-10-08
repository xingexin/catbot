package bootstrap

import (
	"encoding/json"

	"os"

	plugindomain "github.com/xingexin/catbot/internal/domain/plugin"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	taskservice "github.com/xingexin/catbot/internal/domain/task/service"
	"testing"
)

func TestSaveTaskNotificationReferences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		step         taskentity.Step
		when         string
		text         string
		closedOutput bool
		patternField bool
		wantError    bool
	}{
		{
			name: "echo text cannot be a condition",
			step: taskentity.Step{ID: "reminder", Kind: "tool", Tool: "example__echo"},
			when: "${steps.reminder.text}", text: "${steps.reminder.text}", wantError: true,
		},
		{
			name: "echo character count cannot be a condition",
			step: taskentity.Step{ID: "reminder", Kind: "tool", Tool: "example__echo"},
			when: "${steps.reminder.characters}", wantError: true,
		},
		{
			name: "mail cursor object cannot be a condition",
			step: taskentity.Step{ID: "watch", Kind: "tool", Tool: "mail__watch"},
			when: "${steps.watch.cursor}", wantError: true,
		},
		{
			name: "missing field in closed output cannot be a condition",
			step: taskentity.Step{ID: "reminder", Kind: "tool", Tool: "example__echo"},
			when: "${steps.reminder.changed}", closedOutput: true, wantError: true,
		},
		{
			name: "unconditional echo reminder",
			step: taskentity.Step{ID: "reminder", Kind: "tool", Tool: "example__echo"},
			text: "${steps.reminder.text}",
		},
		{
			name: "open output field defers validation",
			step: taskentity.Step{ID: "reminder", Kind: "tool", Tool: "example__echo"},
			when: "${steps.reminder.changed}", text: "${steps.reminder.text}",
		},
		{
			name: "closed output pattern field defers validation",
			step: taskentity.Step{ID: "reminder", Kind: "tool", Tool: "example__echo"},
			when: "${steps.reminder.changed}", closedOutput: true, patternField: true,
		},
		{
			name: "mail boolean condition and string notification",
			step: taskentity.Step{ID: "watch", Kind: "tool", Tool: "mail__watch"},
			when: "${steps.watch.changed}", text: "${steps.watch.notificationText}",
		},
		{
			name: "boolean cannot be notification text",
			step: taskentity.Step{ID: "watch", Kind: "tool", Tool: "mail__watch"},
			text: "${steps.watch.changed}", wantError: true,
		},
		{
			name: "unspecified output schema defers validation",
			step: taskentity.Step{ID: "reminder", Kind: "tool", Tool: "example__note"},
			when: "${steps.reminder.changed}", text: "${steps.reminder.text}",
		},
		{
			name: "agent text cannot be a condition",
			step: taskentity.Step{ID: "reminder", Kind: "agent", Prompt: "提醒用户"},
			when: "${steps.reminder.text}", wantError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			seedNotificationValidationExample(t, a, tc.closedOutput, tc.patternField)
			seedMailPlugin(t, a)
			task := taskentity.Task{
				ID: "notification-validation", Name: "群聊提醒", Kind: "manual",
				ConfigID: "config", PersonaID: "secretary", SessionID: "session",
				Notify: true, NotifyWhen: tc.when, NotifyText: tc.text,
				Steps: []taskentity.Step{tc.step},
			}
			saved, err := a.Tasks.SaveTask(t.Context(), task)
			if tc.wantError {
				if err == nil {
					t.Error("invalid notification reference was accepted")
				}
				for _, kind := range []string{"task", "schedule-intent"} {
					rows, listErr := a.Store.List(t.Context(), kind)
					if listErr != nil {
						t.Fatal(listErr)
					}
					if len(rows) != 0 {
						t.Errorf("invalid notification persisted %d %s records", len(rows), kind)
					}
				}
				if applied := a.Tasks.Scheduler.(*fakeScheduler).applied; len(applied) != 0 {
					t.Errorf("invalid notification provisioned %d schedules", len(applied))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if saved.Status != "active" || saved.NotifyWhen != tc.when || saved.NotifyText != tc.text {
				t.Fatalf("notification settings not preserved: %+v", saved)
			}
			if applied := a.Tasks.Scheduler.(*fakeScheduler).applied; len(applied) != 1 {
				t.Fatalf("expected one schedule, got %d", len(applied))
			}
			if tc.step.Tool == "example__note" {
				_, send, err := taskservice.Notification(saved, "completed", "", map[string]any{
					"reminder": map[string]any{"changed": "not a boolean", "text": "reminder"},
				})
				if err == nil || send {
					t.Fatal("unknown output schema bypassed runtime boolean validation")
				}
			}
		})
	}
}

func seedNotificationValidationExample(t *testing.T, a *App, closedOutput, patternField bool) {
	t.Helper()
	b, err := os.ReadFile("../../plugins/example/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest plugindomain.Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	if closedOutput {
		for i := range manifest.Tools {
			if manifest.Tools[i].Name == "echo" {
				manifest.Tools[i].OutputSchema["additionalProperties"] = false
				if patternField {
					manifest.Tools[i].OutputSchema["patternProperties"] = map[string]any{
						"^change": map[string]any{"type": "boolean"},
					}
				}
			}
		}
	}
	p := plugindomain.Plugin{ID: manifest.ID, Enabled: true, Manifest: manifest, Grants: []string{"storage"}}
	if err := a.Store.Put(t.Context(), "plugin", p.ID, p); err != nil {
		t.Fatal(err)
	}
}
