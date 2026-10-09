package messaging_test

import (
	"context"
	"errors"
	lifecyclebiz "github.com/xingexin/catbot/internal/biz/lifecycle"
	messagingbiz "github.com/xingexin/catbot/internal/biz/messaging"
	"github.com/xingexin/catbot/internal/config"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	message "github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/infra/store"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockedSender struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func newBlockedSender(t *testing.T) *blockedSender {
	t.Helper()
	s := &blockedSender{entered: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(s.unblock)
	return s
}
func (s *blockedSender) unblock() { s.once.Do(func() { close(s.release) }) }
func (s *blockedSender) Send(ctx context.Context, _ message.OutboundMessage) (message.SendResult, error) {
	s.calls.Add(1)
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return message.SendResult{Status: message.Sent, MessageID: "fake-receipt"}, nil
	case <-ctx.Done():
		return message.SendResult{Status: message.Uncertain}, ctx.Err()
	}
}
func TestSessionPurgeWaitsForNotificationOrDeliveryOutcome(t *testing.T) {
	for _, notify := range []bool{false, true} {
		name := "delivery"
		if notify {
			name = "notification"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			s := store.NewMemory()
			sender := newBlockedSender(t)
			messaging, session := messagingLifecycleFixture(t, s, sender)
			service := &lifecyclebiz.Service{Store: s}
			archiveSession(t, ctx, service, session.ID)
			done := make(chan error, 1)
			go func() {
				if notify {
					done <- messaging.NotifyRecord(ctx, message.Notification{ID: "in-flight-notification", SessionID: session.ID, OperationID: "in-flight-send", Text: "private notification contents"}, false)
					return
				}
				done <- messaging.DeliverNotification(ctx, message.NotificationDelivery{SessionID: session.ID, OperationID: "in-flight-send", Text: "private delivery contents"})
			}()
			select {
			case <-sender.entered:
			case <-ctx.Done():
				t.Fatal("sender was not called", ctx.Err())
			}
			var delivery message.Delivery
			if err := s.Get(ctx, "delivery", "in-flight-send", &delivery); err != nil || lifecycle.ParseActivityState(delivery.Status) != lifecycle.ActivityUncertain {
				t.Fatal("missing uncertain intent", delivery, err)
			}
			request := lifecyclebiz.Request{Resource: lifecycle.ResourceSession, Action: lifecycle.ActionPurge, IDs: []string{session.ID}}
			result, err := service.Batch(ctx, request)
			if err != nil || len(result.Failed) != 1 || len(result.Succeeded) != 0 {
				t.Fatal("purged during external send", result, err)
			}
			var retained conversation.Session
			if err := s.Get(ctx, "session", session.ID, &retained); err != nil {
				t.Fatal("rejected purge removed session", err)
			}
			sender.unblock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("send did not finish", ctx.Err())
			}
			result, err = service.Batch(ctx, request)
			if err != nil || len(result.Failed) != 0 || len(result.Succeeded) != 1 {
				t.Fatal("completed delivery blocked purge", result, err)
			}
			if err := messaging.NotifyRecord(ctx, message.Notification{ID: "late-new-notification", SessionID: session.ID, OperationID: "late-new-notification-op", Text: "must never be stored"}, false); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("deleted target accepted notification", err)
			}
			if err := messaging.DeliverNotification(ctx, message.NotificationDelivery{SessionID: session.ID, OperationID: "late-new-send", Text: "must never be sent"}); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("deleted target accepted send", err)
			}
			for _, kind := range []string{"session", "notification", "delivery"} {
				rows, err := s.List(ctx, kind)
				if err != nil || len(rows) != 0 {
					t.Fatal("orphan content", kind, len(rows), err)
				}
			}
			if sender.calls.Load() != 1 {
				t.Fatal("extra external send", sender.calls.Load())
			}
		})
	}
}

// The first read precedes deletion. Intent publication must recheck the session
// under the reference lock, otherwise a stale reader can send after deletion.
func TestDeliveryReadBeforeSessionPurgeCannotCreateAnOrphanIntent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	memory := store.NewMemory()
	sender := newBlockedSender(t)
	sender.unblock()
	barrier := &sessionReadBarrier{Store: memory, reached: make(chan struct{}), resume: make(chan struct{})}
	messaging, session := messagingLifecycleFixture(t, barrier, sender)
	service := &lifecyclebiz.Service{Store: memory}
	archiveSession(t, ctx, service, session.ID)
	done := make(chan error, 1)
	go func() {
		done <- messaging.DeliverNotification(ctx, message.NotificationDelivery{SessionID: session.ID, OperationID: "stale-read-send", Text: "stale private body"})
	}()
	select {
	case <-barrier.reached:
	case <-ctx.Done():
		t.Fatal("stale-read barrier not reached", ctx.Err())
	}
	result, err := service.Batch(ctx, lifecyclebiz.Request{Resource: lifecycle.ResourceSession, Action: lifecycle.ActionPurge, IDs: []string{session.ID}})
	if err != nil || len(result.Failed) != 0 || len(result.Succeeded) != 1 {
		t.Fatal(result, err)
	}
	close(barrier.resume)
	select {
	case err := <-done:
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatal("stale read escaped deletion guard", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	rows, err := memory.List(ctx, "delivery")
	if err != nil || len(rows) != 0 || sender.calls.Load() != 0 {
		t.Fatal("stale reader created delivery after purge", len(rows), sender.calls.Load(), err)
	}
}

type sessionReadBarrier struct {
	store.Store
	reached, resume chan struct{}
	reads           atomic.Int32
}

func (s *sessionReadBarrier) Get(ctx context.Context, kind, id string, target any) error {
	err := s.Store.Get(ctx, kind, id, target)
	if kind == lifecycle.ResourceSession.StorageKind() && err == nil && s.reads.Add(1) == 1 {
		close(s.reached)
		select {
		case <-s.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func messagingLifecycleFixture(t *testing.T, s store.Store, sender message.Sender) (*messagingbiz.Service, conversation.Session) {
	t.Helper()
	session := conversation.Session{ID: "qq-fixture-session", Channel: "qq", ChannelProvider: "fake", ChannelAccount: "10000001", Recipient: "10000002", Title: "test conversation"}
	if err := s.Put(t.Context(), "session", session.ID, session); err != nil {
		t.Fatal(err)
	}
	messaging := messagingbiz.New(s, config.Options{}, nil)
	if err := messaging.RegisterChannel("fake", messagingbiz.Channel{Sender: sender, Binding: func(context.Context) (message.ChannelBinding, error) {
		return message.ChannelBinding{Enabled: true, Account: session.ChannelAccount, AllowedPeers: []string{session.Recipient}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	return messaging, session
}
func archiveSession(t *testing.T, ctx context.Context, s *lifecyclebiz.Service, id string) {
	t.Helper()
	result, err := s.Batch(ctx, lifecyclebiz.Request{Resource: lifecycle.ResourceSession, Action: lifecycle.ActionArchive, IDs: []string{id}})
	if err != nil || len(result.Failed) != 0 || len(result.Succeeded) != 1 {
		t.Fatal(result, err)
	}
}
