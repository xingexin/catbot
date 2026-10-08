package conversation

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
	"testing"
	"time"
)

func TestRunUncertainSDKCompletionCannotBeAutomaticallyReplayed(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		err    error
		status string
	}{
		{"sdk", errors.New("bridge disconnected"), "interrupted"},
		{"sdk", context.Canceled, "cancelled"},
		{"api", context.DeadlineExceeded, "failed"},
		{"api", nil, "completed"},
	} {
		run := Run{ID: "run", Status: "running", ReplyPending: true, Config: agent.Config{Kind: tc.kind}}
		run.Finish(agent.Result{Text: "partial", Usage: map[string]int{"input_tokens": 3}}, tc.err, time.Now())
		if run.Status != tc.status || run.FinishedAt == nil || run.Usage["input_tokens"] != 3 {
			t.Fatal(run)
		}
		if run.CheckExecution() == nil {
			t.Fatal("terminal run authorized automatic replay", run.Status)
		}
		if !run.NeedsReply() {
			t.Fatal("terminal result lost pending reply")
		}
	}
}
func TestNativeSessionIdentityChangesWithPersonaAndExecutionSnapshot(t *testing.T) {
	base := Run{Config: agent.Config{ID: "model", Kind: "sdk", Provider: "claude"}, Persona: persona.Persona{ID: "persona", Version: 1}, Versions: map[string]string{"mail": "v1"}}
	s := Session{}
	s.RememberNative(base.NativeKey(), "native-session")
	if s.NativeFor(base.NativeKey()) != "native-session" {
		t.Fatal("same snapshot cannot resume")
	}
	for _, changed := range []Run{
		{Config: base.Config, Persona: persona.Persona{ID: "persona", Version: 2}, Versions: base.Versions},
		{Config: base.Config, Persona: base.Persona, Versions: map[string]string{"mail": "v2"}},
		{Config: agent.Config{ID: "model", Kind: "sdk", Provider: "codex"}, Persona: base.Persona, Versions: base.Versions},
	} {
		if s.NativeFor(changed.NativeKey()) != "" {
			t.Fatal("incompatible snapshot resumed native session")
		}
	}
	s.ActiveConfig = ""
	if s.NativeFor(base.NativeKey()) != "" {
		t.Fatal("ambiguous native turn resumed after reset")
	}
}
