package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"agentTest/internal/agent"
	"agentTest/internal/domain"
	"agentTest/internal/message"
	"agentTest/internal/store"
)

func outboxApp(t *testing.T, send senderFunc) *App {
	t.Helper()
	a := setupOneBot(t)
	s := domain.Session{ID: "recipient", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "10001", Recipient: "20002", ConfigID: "config", PersonaID: "secretary"}
	if err := a.Store.Put(t.Context(), "session", s.ID, s); err != nil {
		t.Fatal(err)
	}
	c := a.channels["onebot"]
	c.Sender = send
	a.channels["onebot"] = c
	a.Notifier = a.DeliverNotification
	a.Direct = execFunc(func(context.Context, agent.Request, agent.Emit) (agent.Result, error) {
		t.Fatal("recovering a reply must not rerun the model")
		return agent.Result{}, nil
	})
	return a
}

func TestOutboxRecoversTerminalReplyWithoutReexecutingModel(t *testing.T) {
	var sends atomic.Int32
	a := outboxApp(t, func(_ context.Context, in message.OutboundMessage) (message.SendResult, error) {
		sends.Add(1)
		if in.Text != "persisted answer" || in.OperationID != "reply:finished" {
			t.Error("reply changed during recovery", in)
		}
		return message.SendResult{Status: message.Sent, MessageID: "sent"}, nil
	})
	for _, r := range []domain.Run{
		{ID: "finished", SessionID: "recipient", Status: "completed", Result: "persisted answer", ReplyPending: true},
		{ID: "legacy", SessionID: "recipient", Status: "completed", Result: "must not backfill old replies"},
	} {
		if err := a.Store.Put(t.Context(), "run", r.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		a.reconcileNotifications(t.Context())
	}
	var r domain.Run
	if err := a.Store.Get(t.Context(), "run", "finished", &r); err != nil || r.ReplyPending || r.Result != "persisted answer" {
		t.Fatal("reply did not settle", r, err)
	}
	if sends.Load() != 1 {
		t.Fatal("reply duplicated or historical reply backfilled", sends.Load())
	}
}

func TestOutboxRecoversPendingDeliveryConservatively(t *testing.T) {
	for _, outcome := range []string{"not-started", "sent", "uncertain", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			var sends atomic.Int32
			a := outboxApp(t, func(context.Context, message.OutboundMessage) (message.SendResult, error) {
				sends.Add(1)
				return message.SendResult{Status: message.Sent}, nil
			})
			n := notification{ID: "notification-task", SessionID: "recipient", TaskID: "watch", Text: "new mail", Status: "pending", OperationID: "stable"}
			if err := a.Store.Put(t.Context(), "notification", n.ID, n); err != nil {
				t.Fatal(err)
			}
			if outcome != "not-started" {
				if err := a.Store.Put(t.Context(), "delivery", "stable", delivery{ID: "stable", SessionID: "recipient", Status: outcome}); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				a.reconcileNotifications(t.Context())
			}
			wantStatus, wantSends := outcome, int32(0)
			if outcome == "not-started" {
				wantStatus, wantSends = "sent", 1
			}
			if err := a.Store.Get(t.Context(), "notification", n.ID, &n); err != nil || n.Status != wantStatus || sends.Load() != wantSends {
				t.Fatal("unsafe recovery", n, sends.Load(), err)
			}
		})
	}
}

func TestReplyRetryRetainsReferenceWithNewOperation(t *testing.T) {
	var delivered []message.OutboundMessage
	a := outboxApp(t, func(_ context.Context, in message.OutboundMessage) (message.SendResult, error) {
		delivered = append(delivered, in)
		if len(delivered) == 1 {
			return message.SendResult{Status: message.Failed}, errors.New("explicit refusal")
		}
		return message.SendResult{Status: message.Sent}, nil
	})
	reference := message.ReplyReference{MessageID: "incoming-message", ReceivedAt: time.Now().UTC()}
	if err := a.Store.Put(t.Context(), "qq-receipt", "answer", reference); err != nil {
		t.Fatal(err)
	}
	n := notification{ID: "notification-reply:answer", SessionID: "recipient", Text: "answer", OperationID: "reply:answer"}
	if err := a.notify(t.Context(), n, false); err == nil {
		t.Fatal("expected initial failure")
	}
	if err := a.notify(t.Context(), n, true); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 || delivered[0].OperationID == delivered[1].OperationID {
		t.Fatal("retry did not get its own operation", delivered)
	}
	for _, d := range delivered {
		if d.ReplyTo == nil || *d.ReplyTo != reference {
			t.Fatal("retry lost reply reference", d)
		}
	}
}

func TestRestartInterruptedRunKeepsRecoverableReply(t *testing.T) {
	var sends atomic.Int32
	a := outboxApp(t, func(_ context.Context, in message.OutboundMessage) (message.SendResult, error) {
		sends.Add(1)
		if in.Text == "" {
			t.Error("interruption must be explained")
		}
		return message.SendResult{Status: message.Sent}, nil
	})
	r := domain.Run{ID: "interrupted", SessionID: "recipient", Status: "running", ReplyPending: true}
	if err := a.Store.Put(t.Context(), "run", r.ID, r); err != nil {
		t.Fatal(err)
	}
	if err := a.Bootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	a.reconcileNotifications(t.Context())
	r = domain.Run{}
	if err := a.Store.Get(t.Context(), "run", "interrupted", &r); err != nil || r.Status != "interrupted" || r.ReplyPending || sends.Load() != 1 {
		t.Fatal(r, sends.Load(), err)
	}
	if err := a.Store.Get(t.Context(), "notification", "notification-reply:interrupted", &notification{}); errors.Is(err, store.ErrNotFound) {
		t.Fatal("missing visible notification")
	}
}
