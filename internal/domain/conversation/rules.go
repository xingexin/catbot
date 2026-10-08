package conversation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
)

func ValidatePrompt(prompt string) error {
	if strings.TrimSpace(prompt) == "" || len(prompt) > 128<<10 {
		return errors.New("message must contain 1..131072 bytes")
	}
	return nil
}
func (r Run) CheckRequest(sessionID, prompt string) error {
	if r.SessionID != sessionID || r.Prompt != prompt {
		return errors.New("requestId already used for a different message")
	}
	return nil
}
func (r Run) CheckExecution() error {
	if r.Status != "queued" {
		return fmt.Errorf("run is %s; refusing automatic replay", r.Status)
	}
	return nil
}
func (r Run) CheckRetry() error {
	if r.Status == "running" || r.Status == "queued" || r.Status == "completed" {
		return errors.New("only failed or interrupted runs may be retried")
	}
	return nil
}
func (r *Run) CancelQueued() error {
	if r.Status != "queued" {
		return errors.New("run is not cancellable")
	}
	r.Status = "cancelled"
	return nil
}
func (r Run) NativeKey() string {
	// These serialized fields form the existing persisted SDK session identity.
	identity, _ := json.Marshal(struct {
		Config   agent.Config
		Persona  persona.Persona
		Versions map[string]string
	}{r.Config, r.Persona, r.Versions})
	sum := sha256.Sum256(identity)
	return hex.EncodeToString(sum[:])
}
func (s Session) NativeFor(key string) string {
	if s.ActiveConfig == key {
		return s.Native[key]
	}
	return ""
}
func (s *Session) RememberNative(key, nativeID string) {
	if nativeID != "" {
		if s.Native == nil {
			s.Native = map[string]string{}
		}
		s.Native[key] = nativeID
	}
	s.ActiveConfig = key
}
func (r *Run) Finish(result agent.Result, runErr error, now time.Time) {
	r.FinishedAt = &now
	r.Usage = result.Usage
	r.Result = result.Text
	if runErr == nil {
		r.Status = "completed"
		return
	}
	r.Status = "failed"
	if errors.Is(runErr, context.Canceled) {
		r.Status = "cancelled"
	} else if r.Config.Kind == "sdk" {
		r.Status = "interrupted"
	}
	r.Error = runErr.Error()
}
func (r *Run) Interrupt(now time.Time) {
	r.Status = "interrupted"
	r.Error = "service restarted during execution; inspect before resuming"
	r.FinishedAt = &now
}
func (r Run) NeedsReply() bool {
	return r.ReplyPending && r.Status != "queued" && r.Status != "running"
}
func (r Run) ReplyText() string {
	if r.Status == "completed" {
		return r.Result
	}
	return "这次请求未能完成（" + r.Status + "）。请在管理端运行记录查看原因；已执行的操作不会自动重做。"
}
