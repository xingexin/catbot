package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"agentTest/internal/domain"
	"agentTest/internal/job"
	"agentTest/internal/store"
)

func (a *App) Step(ctx context.Context, in job.StepInput) (any, error) {
	opID := in.ExecutionID + ":" + in.Step.ID
	unlock, err := a.Store.Lock(ctx, "task-step:"+opID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if in.Step.Kind == "tool" {
		parts := strings.SplitN(in.Step.Tool, "__", 2)
		if len(parts) != 2 {
			return nil, errors.New("invalid task tool")
		}
		value, err := job.Resolve(in.Step.Arguments, in.Results)
		if err != nil {
			return nil, err
		}
		args, _ := value.(map[string]any)
		key := in.Snapshot.Task.Versions[parts[0]]
		if key == "" {
			return nil, errors.New("task plugin is not pinned")
		}
		return a.Plugins.CallPinned(ctx, key, parts[1], args, opID)
	}
	id := "background-" + opID
	var existing domain.Run
	if err := a.Store.Get(ctx, "run", id, &existing); err == nil {
		if existing.Status == "completed" {
			return map[string]any{"text": existing.Result, "runId": id}, nil
		}
		if existing.Status != "queued" {
			return nil, errors.New("previous agent step did not complete; inspect the run before retrying")
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	} else {
		sessionID := "task-session-" + in.ExecutionID
		session := domain.Session{ID: sessionID, Title: in.Snapshot.Task.Name, ConfigID: in.Snapshot.Config.ID, PersonaID: in.Snapshot.Persona.ID, Channel: "task", Messages: []domain.Message{}}
		var found domain.Session
		if err := a.Store.Get(ctx, "session", sessionID, &found); errors.Is(err, store.ErrNotFound) {
			if err := a.Store.Put(ctx, "session", sessionID, session); err != nil {
				return nil, err
			}
		}
		b, _ := json.Marshal(in.Results)
		run := domain.Run{ID: id, SessionID: sessionID, Prompt: in.Step.Prompt + "\nPrevious task step results:\n" + string(b), Status: "queued", Config: in.Snapshot.Config, Persona: in.Snapshot.Persona, Versions: in.Snapshot.Task.Versions, CreatedAt: time.Now().UTC()}
		if err := a.Store.Put(ctx, "run", id, run); err != nil {
			return nil, err
		}
	}
	run, err := a.Execute(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": run.Result, "runId": run.ID}, nil
}
func (a *App) Notify(ctx context.Context, snapshot job.Snapshot, text, operationID string) error {
	if snapshot.Task.SessionID == "" {
		return nil
	}
	id := "notification-" + operationID
	value := map[string]any{"id": id, "taskId": snapshot.Task.ID, "sessionId": snapshot.Task.SessionID, "text": text, "createdAt": time.Now().UTC(), "status": "saved"}
	if err := a.Store.Put(ctx, "notification", id, value); err != nil {
		return err
	}
	if a.Notifier != nil {
		return a.Notifier(ctx, snapshot.Task.SessionID, text, operationID)
	}
	return nil
}
