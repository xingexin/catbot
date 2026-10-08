package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agentTest/internal/agent"
	"agentTest/internal/domain"
	"agentTest/internal/plugin"
	"agentTest/internal/secret"
	"agentTest/internal/store"
)

type Scheduler interface {
	Apply(context.Context, domain.Task, *domain.Task) error
	Trigger(context.Context, domain.Task, string) (string, error)
	Cancel(context.Context, domain.Task) error
	Ping(context.Context) error
}
type Options struct {
	MaxUploadMB                                                                         int
	DataDir, PluginDir, InternalURL, RuntimeURL, RuntimeToken, MasterKey, AdminPassword string
	QQAppID, QQUser                                                                     string
	QQConfigID, QQPersonaID                                                             string
	CookieSecure                                                                        bool
}
type App struct {
	Store     store.Store
	Vault     *secret.Vault
	Plugins   *plugin.Manager
	Scheduler Scheduler
	Options   Options
	Direct    agent.Executor
	SDK       agent.Executor
	Notifier  func(context.Context, NotificationDelivery) error
	channels  map[string]Channel
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	active    map[string]context.CancelFunc
	sessions  map[string]bool
	wg        sync.WaitGroup
}

func New(s store.Store, o Options) (*App, error) {
	if o.MaxUploadMB <= 0 {
		o.MaxUploadMB = 100
	}
	v, err := secret.New(s, o.MasterKey)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{Store: s, Vault: v, Options: o, ctx: ctx, cancel: cancel, active: map[string]context.CancelFunc{}, sessions: map[string]bool{}}
	a.Plugins = plugin.New(s, v, o.PluginDir, o.DataDir, o.InternalURL, a.Token)
	a.Direct = &agent.Direct{Model: &agent.Model{}, Tools: a}
	a.SDK = &agent.Bridge{URL: o.RuntimeURL, Token: o.RuntimeToken, GatewayURL: o.InternalURL + "/internal/mcp", RunToken: a.Token}
	a.channels = make(map[string]Channel)
	return a, nil
}
func (a *App) Token(scope string) string {
	m := hmac.New(sha256.New, []byte(a.Options.MasterKey))
	_, _ = m.Write([]byte(scope))
	return base64.RawURLEncoding.EncodeToString([]byte(scope)) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (a *App) VerifyToken(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	scope := string(b)
	return scope, hmac.Equal([]byte(token), []byte(a.Token(scope)))
}
func (a *App) Bootstrap(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Join(a.Options.DataDir, "files"), 0700); err != nil {
		return err
	}
	personas, err := store.All[domain.Persona](ctx, a.Store, "persona")
	if err != nil {
		return err
	}
	if len(personas) == 0 {
		p := domain.Persona{ID: "secretary", Name: "小助理", Description: "清楚、可靠的个人秘书", SystemPrompt: "你是一位个人秘书。用简洁自然的中文沟通，记清用户要求，通过工具完成事务，诚实说明执行状态。", Examples: []domain.Message{}, Tools: nil, Version: 1, Default: true}
		if err := a.Store.Put(ctx, "persona", p.ID, p); err != nil {
			return err
		}
	}
	runs, err := store.All[domain.Run](ctx, a.Store, "run")
	if err != nil {
		return err
	}
	recoveredSessions := make(map[string]bool)
	for _, r := range runs {
		if r.Status == "running" {
			if !recoveredSessions[r.SessionID] {
				var session domain.Session
				err := a.Store.Get(ctx, "session", r.SessionID, &session)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("load interrupted run session: %w", err)
				}
				if err == nil && session.ActiveConfig != "" {
					// Persist this before finalizing the run so a failed recovery
					// can retry without resuming an ambiguous native SDK turn.
					session.ActiveConfig = ""
					if err := a.Store.Put(ctx, "session", session.ID, session); err != nil {
						return fmt.Errorf("clear interrupted native session: %w", err)
					}
				}
				recoveredSessions[r.SessionID] = true
			}
			r.Status = "interrupted"
			r.Error = "service restarted during execution; inspect before resuming"
			now := time.Now()
			r.FinishedAt = &now
			if err := a.Store.Put(ctx, "run", r.ID, r); err != nil {
				return err
			}
		}
	}
	if err := a.recoverModelCalls(ctx); err != nil {
		return err
	}
	return a.registerBundled(ctx)
}
func (a *App) Start() {
	a.wg.Go(func() {
		a.reconcileNotifications(a.ctx)
		timer := time.NewTicker(15 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-timer.C:
				a.reconcileNotifications(a.ctx)
			}
		}
	})
	a.wg.Go(func() {
		a.reconcile(a.ctx)
		timer := time.NewTicker(15 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-timer.C:
				a.reconcile(a.ctx)
			}
		}
	})
	a.wg.Go(func() {
		timer := time.NewTicker(150 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-timer.C:
				a.dispatch()
			}
		}
	})
}
func (a *App) Close() {
	a.cancel()
	a.mu.Lock()
	for _, cancel := range a.active {
		cancel()
	}
	a.mu.Unlock()
	a.wg.Wait()
	a.Plugins.Close()
}
func (a *App) dispatch() {
	a.mu.Lock()
	if len(a.active) >= 8 {
		a.mu.Unlock()
		return
	}
	busy := make([]string, 0, len(a.sessions))
	for id := range a.sessions {
		busy = append(busy, id)
	}
	a.mu.Unlock()
	queryCtx, queryCancel := context.WithTimeout(a.ctx, 3*time.Second)
	runs, err := store.QueuedRuns(queryCtx, a.Store, busy, 64)
	queryCancel()
	if err != nil {
		slog.Error("load queue", "error", err)
		return
	}
	for _, r := range runs {
		if r.Status != "queued" || strings.HasPrefix(r.ID, "background-") {
			continue
		}
		a.mu.Lock()
		if a.sessions[r.SessionID] || len(a.active) >= 8 {
			a.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(a.ctx)
		a.sessions[r.SessionID] = true
		a.active[r.ID] = cancel
		a.mu.Unlock()
		a.wg.Go(func() {
			defer cancel()
			if _, err := a.Execute(ctx, r.ID); err != nil {
				slog.Info("run ended", "runId", r.ID, "error", err)
			}
			a.mu.Lock()
			delete(a.active, r.ID)
			delete(a.sessions, r.SessionID)
			a.mu.Unlock()
		})
	}
}
func (a *App) Submit(ctx context.Context, sessionID, prompt, requestID string) (domain.Run, error) {
	if strings.TrimSpace(prompt) == "" || len(prompt) > 128<<10 {
		return domain.Run{}, errors.New("message must contain 1..131072 bytes")
	}
	if requestID == "" {
		requestID = domain.ID()
	}
	id := "run-" + requestID
	unlock, err := a.Store.Lock(ctx, "submit:"+id)
	if err != nil {
		return domain.Run{}, err
	}
	defer unlock()
	var existing domain.Run
	if err := a.Store.Get(ctx, "run", id, &existing); err == nil {
		if existing.SessionID != sessionID || existing.Prompt != prompt {
			return existing, errors.New("requestId already used for a different message")
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return existing, err
	}
	var session domain.Session
	if err := a.Store.Get(ctx, "session", sessionID, &session); err != nil {
		return existing, err
	}
	var config domain.Config
	if err := a.Store.Get(ctx, "config", session.ConfigID, &config); err != nil {
		return existing, fmt.Errorf("select a configured execution strategy: %w", err)
	}
	var persona domain.Persona
	if err := a.Store.Get(ctx, "persona", session.PersonaID, &persona); err != nil {
		return existing, err
	}
	versions, err := a.Plugins.Snapshots(ctx)
	if err != nil {
		return existing, err
	}
	r := domain.Run{ID: id, SessionID: sessionID, Prompt: prompt, Status: "queued", Config: config, Persona: persona, Versions: versions, CreatedAt: time.Now().UTC(), ReplyPending: session.Channel == "qq"}
	return r, a.Store.Put(ctx, "run", id, r)
}
func (a *App) emit(ctx context.Context, id, typ string, data map[string]any) error {
	return a.Store.Append(ctx, domain.Event{RunID: id, Type: typ, Data: data, Time: time.Now().UTC()})
}
func (a *App) Execute(ctx context.Context, id string) (domain.Run, error) {
	var run domain.Run
	if err := a.Store.Get(ctx, "run", id, &run); err != nil {
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
	if err := a.Store.Get(ctx, "run", id, &run); err != nil {
		return run, err
	}
	if run.Status == "completed" {
		return run, nil
	}
	if run.Status != "queued" {
		return run, fmt.Errorf("run is %s; refusing automatic replay", run.Status)
	}
	var session domain.Session
	if err := a.Store.Get(ctx, "session", run.SessionID, &session); err != nil {
		return run, err
	}
	if !strings.HasPrefix(id, "background-") {
		for pluginID := range run.Versions {
			var current domain.Plugin
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
	if err := a.Store.Put(ctx, "run", id, run); err != nil {
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
				if err := a.Store.Put(ctx, "run", run.ID, run); err != nil {
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
		history = append([]domain.Message{{Role: "user", Content: summary}}, history...)
	}
	tools, runErr := a.Tools(ctx, run)
	key := ""
	if runErr == nil {
		key, runErr = a.Vault.Get(ctx, run.Config.CredentialID)
	}
	result := agent.Result{}
	if runErr == nil {
		executor := a.Direct
		identity, _ := json.Marshal(struct {
			Config   domain.Config
			Persona  domain.Persona
			Versions map[string]string
		}{run.Config, run.Persona, run.Versions})
		sum := sha256.Sum256(identity)
		nativeKey := hex.EncodeToString(sum[:])
		native := ""
		if session.ActiveConfig == nativeKey {
			native = session.Native[nativeKey]
		}
		if run.Config.Kind == "sdk" {
			executor = a.SDK
		}
		result, runErr = executor.Run(ctx, agent.Request{Run: run, History: history, Tools: tools, Key: key, NativeID: native}, emit)
		if result.NativeID != "" {
			run.NativeID = result.NativeID
		}
		if runErr == nil && result.NativeID != "" {
			if session.Native == nil {
				session.Native = map[string]string{}
			}
			session.Native[nativeKey] = result.NativeID
		}
		if runErr == nil {
			session.ActiveConfig = nativeKey
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer finishCancel()
	now := time.Now().UTC()
	run.FinishedAt = &now
	run.Usage = result.Usage
	run.Result = result.Text
	if runErr != nil {
		// Never resume an SDK session whose last turn ended ambiguously.
		session.ActiveConfig = ""
		if err := a.Store.Put(finishCtx, "session", session.ID, session); err != nil {
			return run, err
		}
		run.Status = "failed"
		if errors.Is(runErr, context.Canceled) {
			run.Status = "cancelled"
		} else if run.Config.Kind == "sdk" {
			run.Status = "interrupted"
		}
		run.Error = runErr.Error()
		_ = a.emit(finishCtx, id, "error", map[string]any{"message": run.Error, "status": run.Status})
	} else {
		run.Status = "completed"
		session.Messages = append(session.Messages, domain.Message{Role: "user", Content: run.Prompt}, domain.Message{Role: "assistant", Content: result.Text})
		if err := a.Store.Put(finishCtx, "session", session.ID, session); err != nil {
			run.Status = "interrupted"
			run.Error = "result generated but session persistence failed"
			runErr = err
		}
	}
	if err := a.Store.Put(finishCtx, "run", id, run); err != nil {
		return run, err
	}
	_ = a.emit(finishCtx, id, "finished", map[string]any{"status": run.Status, "result": run.Result, "usage": run.Usage})
	if run.ReplyPending {
		// Model timeout must not leave only a fraction of a second to deliver
		// its reply. Persist the complete text before the external operation.
		notifyCtx, notifyCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err := a.notifyReply(notifyCtx, run)
		notifyCancel()
		if err != nil {
			slog.Warn("message notification failed", "runId", id, "error", err)
		}
	}
	return run, runErr
}
func (a *App) CancelRun(ctx context.Context, id string) error {
	a.mu.Lock()
	cancel := a.active[id]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		return nil
	}
	unlock, err := a.Store.Lock(ctx, "run-state:"+id)
	if err != nil {
		return err
	}
	defer unlock()
	var r domain.Run
	if err := a.Store.Get(ctx, "run", id, &r); err != nil {
		return err
	}
	if r.Status == "queued" {
		r.Status = "cancelled"
		return a.Store.Put(ctx, "run", id, r)
	}
	return errors.New("run is not cancellable")
}
