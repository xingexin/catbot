package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/xingexin/catbot/internal/domain"
	"github.com/xingexin/catbot/internal/job"
	"github.com/xingexin/catbot/internal/message"
	"github.com/xingexin/catbot/internal/store"
)

type notification struct {
	ID          string                  `json:"id"`
	TaskID      string                  `json:"taskId,omitempty"`
	PluginID    string                  `json:"pluginId,omitempty"`
	SessionID   string                  `json:"sessionId"`
	Text        string                  `json:"text"`
	Status      string                  `json:"status"`
	Error       string                  `json:"error,omitempty"`
	OperationID string                  `json:"operationId"`
	Attempts    int                     `json:"attempts"`
	CreatedAt   time.Time               `json:"createdAt"`
	UpdatedAt   time.Time               `json:"updatedAt"`
	ReplyTo     *message.ReplyReference `json:"replyTo,omitempty"`
}

func (a *App) Notify(ctx context.Context, snapshot job.Snapshot, text, operationID string) error {
	if snapshot.Task.SessionID == "" {
		return nil
	}
	return a.notify(ctx, notification{ID: "notification-" + operationID, TaskID: snapshot.Task.ID, SessionID: snapshot.Task.SessionID, Text: text, OperationID: operationID}, false)
}

func (a *App) notify(ctx context.Context, n notification, retry bool) error {
	unlock, err := a.Store.Lock(ctx, "notification:"+n.ID)
	if err != nil {
		return err
	}
	defer unlock()
	var old notification
	if err := a.Store.Get(ctx, "notification", n.ID, &old); err == nil {
		if old.SessionID != n.SessionID || old.Text != n.Text || old.TaskID != n.TaskID || old.PluginID != n.PluginID {
			return errors.New("notification operation ID is already used for a different recipient or message")
		}
		if old.Status == "sent" || old.Status == "saved" {
			return nil
		}
		n = old
		if retry {
			if old.Status != "failed" {
				return errors.New("投递结果不确定，不能自动重发；请先核对接收方消息")
			}
			if err := a.notificationReplyReference(ctx, &n); err != nil {
				return err
			}
			n.OperationID = "notification-retry:" + n.ID + ":" + domain.ID()
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	var s domain.Session
	if err := a.Store.Get(ctx, "session", n.SessionID, &s); err != nil {
		return err
	}
	if err := a.notificationReplyReference(ctx, &n); err != nil {
		return err
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	n.UpdatedAt = time.Now().UTC()
	n.Status = "pending"
	n.Error = ""
	n.Attempts++
	if err := a.Store.Put(ctx, "notification", n.ID, n); err != nil {
		return err
	}
	if s.Channel != "qq" {
		n.Status = "saved"
	} else if a.Notifier == nil {
		n.Status = "failed"
		n.Error = "消息发送服务不可用"
		err = errors.New(n.Error)
	} else {
		err = a.Notifier(ctx, NotificationDelivery{SessionID: n.SessionID, Text: n.Text, OperationID: n.OperationID, ReplyTo: n.ReplyTo})
		n.Status = "sent"
		if err != nil {
			n.Status = "uncertain"
			n.Error = err.Error()
			var d delivery
			readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			deliveryErr := a.Store.Get(readCtx, "delivery", n.OperationID, &d)
			readCancel()
			if deliveryErr == nil && d.Status == "failed" {
				n.Status = "failed"
			}
		}
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	n.UpdatedAt = time.Now().UTC()
	if saveErr := a.Store.Put(finish, "notification", n.ID, n); saveErr != nil {
		return saveErr
	}
	return err
}

func (a *App) retryNotification(w http.ResponseWriter, r *http.Request) {
	n := notification{ID: r.PathValue("id")}
	if err := a.Store.Get(r.Context(), "notification", n.ID, &n); err != nil {
		fail(w, err)
		return
	}
	if n.Status != "failed" {
		fail(w, errors.New("仅可重试已明确失败的通知，结果不确定的投递需人工核对"))
		return
	}
	if err := a.notify(r.Context(), n, true); err != nil {
		fail(w, err)
		return
	}
	_ = a.Store.Get(r.Context(), "notification", n.ID, &n)
	JSON(w, 200, n)
}
