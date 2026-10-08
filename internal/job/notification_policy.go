package job

import (
	"context"
	"errors"
	"time"

	"agentTest/internal/domain"
	"agentTest/internal/store"
)

type notificationIncident struct {
	ExecutionID string    `json:"executionId"`
	AttemptedAt time.Time `json:"attemptedAt"`
}

// A broken recurring monitor should remain visible in every execution record,
// but should not wake its owner every minute. Intent is saved before delivery;
// uncertain delivery is not treated as permission to send another incident.
func (e *Engine) allowNotification(ctx context.Context, t domain.Task, id, status string) (bool, error) {
	if t.Kind != "recurring" {
		return true, nil
	}
	unlock, err := e.Store.Lock(ctx, "notification-incident:"+t.ID)
	if err != nil {
		return false, err
	}
	defer unlock()
	if status == "completed" {
		return true, e.Store.Delete(ctx, "notification-incident", t.ID)
	}
	var incident notificationIncident
	err = e.Store.Get(ctx, "notification-incident", t.ID, &incident)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if err == nil && incident.ExecutionID != id && time.Since(incident.AttemptedAt) < time.Hour {
		return false, nil
	}
	if incident.ExecutionID != id {
		incident = notificationIncident{ExecutionID: id, AttemptedAt: time.Now().UTC()}
		if err := e.Store.Put(ctx, "notification-incident", t.ID, incident); err != nil {
			return false, err
		}
	}
	return true, nil
}
