package service

import (
	"context"
	"testing"

	"github.com/xingexin/catbot/internal/agent"
	"github.com/xingexin/catbot/internal/domain"
	"github.com/xingexin/catbot/internal/job"
)

func TestBackgroundAgentCreatesReminderForOriginSession(t *testing.T) {
	a := testApp(t)
	var config domain.Config
	var persona domain.Persona
	if err := a.Store.Get(t.Context(), "config", "config", &config); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.Get(t.Context(), "persona", "secretary", &persona); err != nil {
		t.Fatal(err)
	}
	a.Direct = execFunc(func(ctx context.Context, r agent.Request, _ agent.Emit) (agent.Result, error) {
		_, err := a.Call(ctx, r.Run.ID, "system__task_create", map[string]any{
			"name": "从视频提取的提醒", "kind": "manual", "notify": true,
			"steps": []any{map[string]any{"id": "remind", "kind": "agent", "prompt": "提醒用户完成事项"}},
		}, "create-reminder-from-task")
		return agent.Result{Text: "已安排"}, err
	})
	_, err := a.Step(t.Context(), job.StepInput{ExecutionID: "video-analysis", Step: domain.Step{ID: "extract", Kind: "agent", Prompt: "提取事项并创建提醒"}, Snapshot: job.Snapshot{Task: domain.Task{Name: "视频事项", SessionID: "session"}, Config: config, Persona: persona}})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := a.Scheduler.(*fakeScheduler)
	if len(scheduler.applied) != 1 || scheduler.applied[0].SessionID != "session" {
		t.Fatalf("reminder lost recipient: %+v", scheduler.applied)
	}
	var background domain.Session
	if err := a.Store.Get(t.Context(), "session", "task-session-video-analysis", &background); err != nil {
		t.Fatal(err)
	}
	if background.OriginSessionID != "session" {
		t.Fatal("origin not persisted")
	}
}

func TestNotifiedTaskMustHaveAUserDestination(t *testing.T) {
	a := testApp(t)
	task := domain.Task{Name: "提醒", Kind: "manual", ConfigID: "config", PersonaID: "secretary", Notify: true, Steps: []domain.Step{{ID: "remind", Kind: "agent", Prompt: "提醒"}}}
	if _, err := a.SaveTask(t.Context(), task); err == nil {
		t.Fatal("notification without recipient accepted")
	}
	if err := a.Store.Put(t.Context(), "session", "internal-task", domain.Session{ID: "internal-task", Channel: "task"}); err != nil {
		t.Fatal(err)
	}
	task.SessionID = "internal-task"
	if _, err := a.SaveTask(t.Context(), task); err == nil {
		t.Fatal("notification to internal task accepted")
	}
}

func TestRecreateCancelledMailWatchRestartsStableSubscription(t *testing.T) {
	a := testApp(t)
	seedMailPlugin(t, a)
	input := mailWatchRequest{SessionID: "session"}
	original, err := a.SaveMailWatch(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ControlTask(t.Context(), original.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	restarted, err := a.SaveMailWatch(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.ID != original.ID || restarted.Paused || restarted.Status != "active" {
		t.Fatalf("cancelled watch did not restart: %+v", restarted)
	}
	if len(a.Scheduler.(*fakeScheduler).operations) != 2 {
		t.Fatal("restart did not trigger immediate check")
	}
}

func TestSDKWithoutToolCapabilityCannotCallBusinessTools(t *testing.T) {
	a := testApp(t)
	run := domain.Run{ID: "no-tools-sdk", SessionID: "session", Status: "running", Config: domain.Config{Kind: "sdk", Provider: "codebuddy", Capabilities: domain.Capabilities{Tools: false}}}
	if err := a.Store.Put(t.Context(), "run", run.ID, run); err != nil {
		t.Fatal(err)
	}
	tools, err := a.Tools(t.Context(), run)
	if err != nil || len(tools) != 0 {
		t.Fatal("tools exposed despite disabled capability", tools, err)
	}
	if _, err := a.Call(t.Context(), run.ID, "system__task_list", map[string]any{}, "disabled-capability"); err == nil {
		t.Fatal("SDK bypassed disabled tool capability")
	}
}
