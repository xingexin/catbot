package conversation

import (
	"github.com/xingexin/catbot/internal/domain/agent"
	"testing"
)

func TestSDKEnvironmentSwitchDoesNotResumeAnotherEnvironment(t *testing.T) {
	run := Run{Config: agent.Config{Kind: "sdk", Provider: "codebuddy", Model: "fixture", CodeBuddyEnvironment: agent.CodeBuddyEnvironmentIOA}}
	session := Session{}
	key := run.NativeKey()
	session.RememberNative(key, "ioa-session")
	if session.NativeFor(run.NativeKey()) != "ioa-session" {
		t.Fatal("same environment lost native session")
	}
	for _, environment := range []agent.CodeBuddyEnvironment{agent.CodeBuddyEnvironmentCN, agent.CodeBuddyEnvironmentInternational, agent.CodeBuddyEnvironmentInherit} {
		run.Config.CodeBuddyEnvironment = environment
		if session.NativeFor(run.NativeKey()) != "" {
			t.Fatalf("environment %d reused IOA native session", environment)
		}
	}
}
