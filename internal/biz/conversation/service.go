package conversation

import (
	"context"
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	convrepo "github.com/xingexin/catbot/internal/domain/conversation/repository"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepo "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/persona"
	personarepo "github.com/xingexin/catbot/internal/domain/persona/repository"
	plugindomain "github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
	secret "github.com/xingexin/catbot/internal/infra/vault"
	"log/slog"
	"strings"
	"time"
)

type Plugins interface {
	Snapshots(context.Context) (map[string]string, error)
}
type ToolSource interface {
	Tools(context.Context, conversation.Run) ([]agent.Tool, error)
}
type ReplyNotifier interface {
	NotifyReply(context.Context, conversation.Run) error
}
type Service struct {
	Store        store.Store
	Vault        *secret.Vault
	Plugins      Plugins
	ToolSource   ToolSource
	Replies      ReplyNotifier
	Direct       agent.Executor
	SDK          agent.Executor
	CancelActive func(string) bool
}

func (a *Service) Submit(ctx context.Context, sessionID, prompt, requestID string) (conversation.Run, error) {
	if err := conversation.ValidatePrompt(prompt); err != nil {
		return conversation.Run{}, err
	}
	if requestID == "" {
		requestID = idgen.New()
	}
	id := "run-" + requestID
	metaUnlock, err := a.Store.Lock(ctx, "session-meta:"+sessionID)
	if err != nil {
		return conversation.Run{}, err
	}
	defer metaUnlock()
	if gone, err := store.Purged(ctx, a.Store, "run", id); err != nil {
		return conversation.Run{}, err
	} else if gone {
		return conversation.Run{}, lifecycleRepo.ErrPurged
	}
	unlock, err := a.Store.Lock(ctx, "submit:"+id)
	if err != nil {
		return conversation.Run{}, err
	}
	defer unlock()
	var existing conversation.Run
	if err := convrepo.New(a.Store).GetRun(ctx, id, &existing); err == nil {
		if err := existing.CheckRequest(sessionID, prompt); err != nil {
			return existing, err
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return existing, err
	}
	referencesUnlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return conversation.Run{}, err
	}
	defer referencesUnlock()
	var session conversation.Session
	if err := convrepo.New(a.Store).GetSession(ctx, sessionID, &session); err != nil {
		return existing, err
	}
	if err := lifecycleRepo.RequireActive(ctx, a.Store, lifecycle.ResourceConfig, session.ConfigID); err != nil {
		if errors.Is(err, lifecycleRepo.ErrArchived) {
			return existing, fmt.Errorf("模型配置已归档，请在归档栏恢复或为此对话切换其他模型配置: %w", err)
		}
		return existing, err
	}
	var config agent.Config
	if err := a.Store.Get(ctx, "config", session.ConfigID, &config); err != nil {
		return existing, fmt.Errorf("select a configured execution strategy: %w", err)
	}
	var persona persona.Persona
	if err := personarepo.New(a.Store).Get(ctx, session.PersonaID, &persona); err != nil {
		return existing, err
	}
	versions, err := a.Plugins.Snapshots(ctx)
	if err != nil {
		return existing, err
	}
	// A new incoming message brings an archived conversation back to the inbox.
	if err := lifecycleRepo.Unmark(ctx, a.Store, lifecycle.ResourceSession, sessionID); err != nil {
		return existing, err
	}
	r := conversation.Run{ID: id, SessionID: sessionID, Prompt: prompt, Status: "queued", Config: config, Persona: persona, Versions: versions, CreatedAt: time.Now().UTC(), ReplyPending: session.Channel == "qq"}
	return r, convrepo.New(a.Store).SaveRun(ctx, r)
}

func (a *Service) emit(ctx context.Context, id, typ string, data map[string]any) error {
	return convrepo.New(a.Store).AppendEvent(ctx, conversation.Event{RunID: id, Type: typ, Data: data, Time: time.Now().UTC()})
}

func (a *Service) Execute(ctx context.Context, id string) (conversation.Run, error) {
	var run conversation.Run
	if err := convrepo.New(a.Store).GetRun(ctx, id, &run); err != nil {
		return run, err
	}
	unlock, err := a.Store.Lock(ctx, "session:"+run.SessionID)
	if err != nil {
		return run, err
	}
	defer unlock()
	stateUnlock, err := a.Store.Lock(ctx, "run-state:"+id)
	if err != nil {
		return run, err
	}
	stateLocked := true
	defer func() {
		if stateLocked {
			stateUnlock()
		}
	}()
	if err := convrepo.New(a.Store).GetRun(ctx, id, &run); err != nil {
		return run, err
	}
	if run.Status == "completed" {
		return run, nil
	}
	if err := run.CheckExecution(); err != nil {
		return run, err
	}
	var session conversation.Session
	if err := convrepo.New(a.Store).GetSession(ctx, run.SessionID, &session); err != nil {
		return run, err
	}
	if !strings.HasPrefix(id, "background-") {
		for pluginID := range run.Versions {
			var current plugindomain.Plugin
			if err := a.Store.Get(ctx, "plugin", pluginID, &current); err != nil {
				return run, err
			}
			if !current.Enabled {
				delete(run.Versions, pluginID)
			}
		}
	}
	run.Status = "running"
	run.ReplyPending = session.Channel == "qq"
	if err := convrepo.New(a.Store).SaveRun(ctx, run); err != nil {
		return run, err
	}
	stateUnlock()
	stateLocked = false
	ctx, cancel := context.WithTimeout(ctx, time.Duration(run.Config.TimeoutSec)*time.Second)
	defer cancel()
	emit := func(typ string, data map[string]any) error {
		if typ == "native.session" {
			if native, ok := data["id"].(string); ok {
				run.NativeID = native
				if err := convrepo.New(a.Store).SaveRun(ctx, run); err != nil {
					return err
				}
			}
		}
		return a.emit(ctx, id, typ, data)
	}
	_ = emit("started", map[string]any{"strategy": run.Config.Kind, "model": run.Config.Model})
	historyLimit := 32000
	if run.Config.Kind == "api" {
		budget := run.Config.MaxInputBytes
		if budget == 0 {
			budget = agent.DefaultMaxInputBytes
		}
		historyLimit = budget / 2
	}
	history, summary := agent.Compact(session.Messages, historyLimit)
	session.Summary = summary
	if summary != "" {
		history = append([]agent.Message{{Role: "user", Content: summary}}, history...)
	}
	tools, runErr := a.ToolSource.Tools(ctx, run)
	key := ""
	if runErr == nil {
		key, runErr = a.Vault.Get(ctx, run.Config.CredentialID)
	}
	result := agent.Result{}
	if runErr == nil {
		executor := a.Direct
		nativeKey := run.NativeKey()
		native := session.NativeFor(nativeKey)
		if run.Config.Kind == "sdk" {
			executor = a.SDK
		}
		result, runErr = executor.Run(ctx, agent.Request{RunID: run.ID, SessionID: run.SessionID, Prompt: run.Prompt, Config: run.Config, Persona: run.Persona, History: history, Tools: tools, Key: key, NativeID: native}, emit)
		if result.NativeID != "" {
			run.NativeID = result.NativeID
		}
		if runErr == nil {
			session.RememberNative(nativeKey, result.NativeID)
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer finishCancel()
	run.Finish(result, runErr, time.Now().UTC())
	if runErr != nil {
		// Never resume an SDK session whose last turn ended ambiguously.
		session.ActiveConfig = ""
		if err := convrepo.New(a.Store).SaveSession(finishCtx, session); err != nil {
			return run, err
		}

		_ = a.emit(finishCtx, id, "error", map[string]any{"message": run.Error, "status": run.Status})
	} else {
		session.Messages = append(session.Messages, agent.Message{Role: "user", Content: run.Prompt}, agent.Message{Role: "assistant", Content: result.Text})
		if err := convrepo.New(a.Store).SaveSession(finishCtx, session); err != nil {
			run.Status = "interrupted"
			run.Error = "result generated but session persistence failed"
			runErr = err
		}
	}
	if err := convrepo.New(a.Store).SaveRun(finishCtx, run); err != nil {
		return run, err
	}
	_ = a.emit(finishCtx, id, "finished", map[string]any{"status": run.Status, "result": run.Result, "usage": run.Usage})
	if run.ReplyPending && a.Replies != nil {
		// Model timeout must not leave only a fraction of a second to deliver
		// its reply. Persist the complete text before the external operation.
		notifyCtx, notifyCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err := a.Replies.NotifyReply(notifyCtx, run)
		notifyCancel()
		if err != nil {
			slog.Warn("message notification failed", "runId", id, "error", err)
		}
	}
	return run, runErr
}

func (a *Service) CancelRun(ctx context.Context, id string) error {
	if a.CancelActive != nil && a.CancelActive(id) {
		return nil
	}
	unlock, err := a.Store.Lock(ctx, "run-state:"+id)
	if err != nil {
		return err
	}
	defer unlock()
	var r conversation.Run
	if err := convrepo.New(a.Store).GetRun(ctx, id, &r); err != nil {
		return err
	}
	if err := r.CancelQueued(); err != nil {
		return err
	}
	return convrepo.New(a.Store).SaveRun(ctx, r)
}
