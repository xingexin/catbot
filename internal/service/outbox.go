package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/xingexin/catbot/internal/domain"
	"github.com/xingexin/catbot/internal/message"
	"github.com/xingexin/catbot/internal/store"
)

func (a *App) notificationReplyReference(ctx context.Context, n *notification) error {
	if n.ReplyTo != nil {
		return nil
	}
	// Notification identity remains stable across explicit delivery retries.
	runID := strings.TrimPrefix(n.ID, "notification-reply:")
	if runID == n.ID {
		return nil
	}
	var receipt message.ReplyReference
	err := a.Store.Get(ctx, "qq-receipt", runID, &receipt)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	n.ReplyTo = &receipt
	return nil
}

// ReplyPending is saved with the run before execution starts. A terminal run
// therefore remains discoverable even if the process exits before creating its
// notification. Old historical runs without the flag are never backfilled.
func (a *App) notifyReply(ctx context.Context, run domain.Run) error {
	if !run.ReplyPending || run.Status == "queued" || run.Status == "running" {
		return nil
	}
	text := run.Result
	if run.Status != "completed" {
		text = "这次请求未能完成（" + run.Status + "）。请在管理端运行记录查看原因；已执行的操作不会自动重做。"
	}
	n := notification{ID: "notification-reply:" + run.ID, SessionID: run.SessionID, Text: text, OperationID: "reply:" + run.ID}
	notifyErr := a.notify(ctx, n, false)
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.Store.Get(finish, "notification", n.ID, &n); err != nil {
		return errors.Join(notifyErr, err)
	}
	switch n.Status {
	case "sent", "saved", "failed", "uncertain":
		// Failed/uncertain outcomes now have a visible record; the latter must
		// never authorize another external send. Only known failures can be
		// manually retried with a new operation ID.
	default:
		return notifyErr
	}
	unlock, err := a.Store.Lock(finish, "run-state:"+run.ID)
	if err != nil {
		return errors.Join(notifyErr, err)
	}
	defer unlock()
	var current domain.Run
	if err := a.Store.Get(finish, "run", run.ID, &current); err != nil {
		return errors.Join(notifyErr, err)
	}
	if current.ReplyPending && current.Status != "running" && current.Status != "queued" {
		current.ReplyPending = false
		if err := a.Store.Put(finish, "run", current.ID, current); err != nil {
			return errors.Join(notifyErr, err)
		}
	}
	return notifyErr
}

func (a *App) reconcileNotifications(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	runs, err := store.PendingReplies(ctx, a.Store, 32)
	if err != nil {
		slog.Warn("load pending replies", "error", err)
		return
	}
	for _, run := range runs {
		if ctx.Err() != nil {
			return
		}
		attempt, stop := context.WithTimeout(ctx, 30*time.Second)
		err := a.notifyReply(attempt, run)
		stop()
		if err != nil {
			slog.Warn("reconcile reply", "runId", run.ID, "error", err)
		}
	}
	records, err := store.PendingNotifications(ctx, a.Store, 32)
	if err != nil {
		slog.Warn("load pending notifications", "error", err)
		return
	}
	for _, encoded := range records {
		if ctx.Err() != nil {
			return
		}
		var n notification
		if err := json.Unmarshal(encoded, &n); err != nil {
			slog.Warn("decode pending notification", "error", err)
			continue
		}
		attempt, stop := context.WithTimeout(ctx, 30*time.Second)
		err := a.notify(attempt, n, false)
		stop()
		if err != nil {
			slog.Warn("reconcile notification", "notificationId", n.ID, "error", err)
		}
	}
}
