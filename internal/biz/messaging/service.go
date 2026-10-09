package messaging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/xingexin/catbot/internal/config"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	convrepo "github.com/xingexin/catbot/internal/domain/conversation/repository"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepo "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	message "github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/domain/persona"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Conversation interface {
	Submit(context.Context, string, string, string) (conversation.Run, error)
}
type Service struct {
	Store        store.Store
	Options      config.Options
	Conversation Conversation
	Notifier     func(context.Context, message.NotificationDelivery) error
	channels     map[string]Channel
}

func New(s store.Store, o config.Options, c Conversation) *Service {
	a := &Service{Store: s, Options: o, Conversation: c, channels: map[string]Channel{}}
	a.Notifier = a.DeliverNotification
	return a
}

type ChannelStatus struct {
	message.ConnectionStatus
	Provider       string `json:"provider"`
	Implementation string `json:"implementation"`
}

func (a *Service) Channel(key string) (Channel, bool) { c, ok := a.channels[key]; return c, ok }

type Channel struct {
	Title          string
	Implementation string
	LoginURL       string
	Sender         message.Sender
	Status         message.StatusChecker
	Receive        http.Handler
	Binding        func(context.Context) (message.ChannelBinding, error)
}
type OneBotBinding struct {
	Enabled         bool     `json:"enabled"`
	SelfID          string   `json:"selfId"`
	AllowedUserIDs  []string `json:"allowedUserIds"`
	AllowedGroupIDs []string `json:"allowedGroupIds"`
	ConfigID        string   `json:"configId"`
	PersonaID       string   `json:"personaId"`
	GroupPersonaID  string   `json:"groupPersonaId"`
}

func (a *Service) HandleIncoming(ctx context.Context, in message.InboundMessage) error {
	channel, ok := a.channels[in.Route]
	if !ok {
		return errors.New("message channel is not registered: " + in.Route)
	}
	binding, err := channel.Binding(ctx)
	if err != nil {
		return err
	}
	if !binding.Allows(in.Account, in.Peer, in.RoomID, false) || (in.RoomID != "" && !in.Mentioned) {
		return nil
	}
	if in.MessageID == "" || strings.TrimSpace(in.Text) == "" {
		return nil
	}
	if binding.ConfigID == "" {
		return errors.New("select a channel execution configuration first")
	}
	personaID := binding.PersonaID
	identity := in.Route + ":" + in.Account + ":" + in.Peer
	if in.RoomID != "" {
		personaID = binding.RoomPersonaID
		identity = in.Route + ":" + in.Account + ":group:" + in.RoomID + ":" + in.Peer
	} else if personaID == "" {
		personaID = "secretary"
	}
	sum := sha256.Sum256([]byte(identity))
	sessionID := "qq-" + in.Route + "-" + hex.EncodeToString(sum[:16])
	sum = sha256.Sum256([]byte(identity + ":" + in.MessageID))
	requestID := "qq-" + hex.EncodeToString(sum[:16])
	// A late platform callback must not recreate a permanently removed turn.
	if gone, err := store.Purged(ctx, a.Store, "run", "run-"+requestID); err != nil {
		return err
	} else if gone {
		return nil
	}
	// Serialize duplicate callbacks without holding the conversation execution lock.
	unlock, err := a.Store.Lock(ctx, "qq-incoming:"+sessionID)
	if err != nil {
		return err
	}
	defer unlock()
	err = func() error {
		metaUnlock, err := a.Store.Lock(ctx, "session-meta:"+sessionID)
		if err != nil {
			return err
		}
		defer metaUnlock()
		referenceUnlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
		if err != nil {
			return err
		}
		defer referenceUnlock()
		if gone, err := store.Purged(ctx, a.Store, "run", "run-"+requestID); err != nil {
			return err
		} else if gone {
			return lifecycleRepo.ErrPurged
		}
		var session conversation.Session
		err = convrepo.New(a.Store).GetSession(ctx, sessionID, &session)
		newSession := errors.Is(err, store.ErrNotFound)
		if err != nil && !newSession {
			return err
		}
		if newSession {
			for resource, id := range map[lifecycle.Resource]string{lifecycle.ResourceConfig: binding.ConfigID, lifecycle.ResourcePersona: personaID} {
				if err := lifecycleRepo.RequireActive(ctx, a.Store, resource, id); err != nil {
					return err
				}
			}
			title := channel.Title + " · " + in.Peer
			if in.RoomID != "" {
				title = channel.Title + " · 群 " + in.RoomID + " · " + in.Peer
			}
			session = conversation.Session{ID: sessionID, Title: title, Channel: "qq",
				ChannelProvider: in.Route, ChannelAccount: in.Account, ChannelRoom: in.RoomID, Recipient: in.Peer,
				PersonaID: personaID, ConfigID: binding.ConfigID, Messages: []agent.Message{}, Native: map[string]string{}}
		}
		if in.RoomID != "" {
			var persona persona.Persona
			if session.PersonaID == "" {
				return errors.New("select a group persona with an explicit tool list first")
			}
			if err := a.Store.Get(ctx, "persona", session.PersonaID, &persona); err != nil {
				return err
			}
			if persona.Tools == nil {
				return errors.New("group persona must have an explicit tool list")
			}
		}
		if newSession {
			if err := convrepo.New(a.Store).SaveSession(ctx, session); err != nil {
				return err
			}
		}
		var receipt message.ReplyReference
		err = a.Store.Get(ctx, "qq-receipt", "run-"+requestID, &receipt)
		if errors.Is(err, store.ErrNotFound) {
			err = a.Store.Put(ctx, "qq-receipt", "run-"+requestID, message.ReplyReference{MessageID: in.MessageID, ReceivedAt: in.ReceivedAt})
		}
		if err != nil {
			return err
		}
		return nil
	}()
	if errors.Is(err, lifecycleRepo.ErrPurged) || errors.Is(err, store.ErrPurgedRecord) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = a.Conversation.Submit(ctx, sessionID, strings.TrimSpace(in.Text), requestID)
	if errors.Is(err, lifecycleRepo.ErrPurged) {
		return nil
	}
	return err
}

func (a *Service) SendMessage(ctx context.Context, sessionID, text, operationID string) error {
	return a.DeliverNotification(ctx, message.NotificationDelivery{SessionID: sessionID, Text: text, OperationID: operationID})
}

func (a *Service) DeliverNotification(ctx context.Context, in message.NotificationDelivery) error {
	sessionID, text, operationID := in.SessionID, in.Text, in.OperationID
	var session conversation.Session
	if err := convrepo.New(a.Store).GetSession(ctx, sessionID, &session); err != nil {
		return err
	}
	if session.Channel != "qq" {
		return nil
	}
	if operationID == "" {
		return errors.New("message delivery requires an operation ID")
	}
	provider := session.ChannelProvider
	if provider == "" {
		provider = "official"
	} // Existing official sessions.
	transport, ok := a.channels[provider]
	if !ok {
		return errors.New("unsupported message channel: " + provider)
	}
	runes := []rune(text)
	if len(runes) > 1800 {
		text = string(runes[:1800]) + "\n…完整结果请在 Web 查看。"
	}
	if gone, err := store.Purged(ctx, a.Store, "delivery", operationID); err != nil {
		return err
	} else if gone {
		return lifecycleRepo.ErrPurged
	}
	unlock, err := a.Store.Lock(ctx, "delivery:"+operationID)
	if err != nil {
		return err
	}
	defer unlock()
	if gone, err := store.Purged(ctx, a.Store, "delivery", operationID); err != nil {
		return err
	} else if gone {
		return lifecycleRepo.ErrPurged
	}
	var old message.Delivery
	if err := a.Store.Get(ctx, "delivery", operationID, &old); err == nil {
		_, err := old.CheckReuse(sessionID)
		return err
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	// Persist intent BEFORE contacting the platform. A crash cannot cause an
	// automatic second send whose first remote outcome is unknown.
	d := message.Delivery{ID: operationID, SessionID: sessionID, Provider: provider, Status: "uncertain", CreatedAt: time.Now().UTC()}
	if err := func() error {
		referenceUnlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
		if err != nil {
			return err
		}
		defer referenceUnlock()
		if err := convrepo.New(a.Store).GetSession(ctx, sessionID, &session); err != nil {
			return err
		}
		return a.Store.Put(ctx, "delivery", operationID, d)
	}(); err != nil {
		return err
	}
	result := message.SendResult{Status: message.Failed}
	binding, sendErr := transport.Binding(ctx)
	if sendErr == nil && !binding.Allows(session.ChannelAccount, session.Recipient, session.ChannelRoom, true) {
		sendErr = errors.New("message account or recipient is not bound")
	}
	if sendErr == nil {
		out := message.OutboundMessage{Account: session.ChannelAccount, Peer: session.Recipient, RoomID: session.ChannelRoom, Text: text, OperationID: operationID, ReplyTo: in.ReplyTo}
		if out.Account == "" && binding.AllowLegacyAccount {
			out.Account = binding.Account
		}
		if out.ReplyTo == nil && strings.HasPrefix(operationID, "reply:") {
			var receipt message.ReplyReference
			receiptErr := a.Store.Get(ctx, "qq-receipt", strings.TrimPrefix(operationID, "reply:"), &receipt)
			if receiptErr == nil {
				out.ReplyTo = &receipt
			} else if !errors.Is(receiptErr, store.ErrNotFound) {
				sendErr = receiptErr
			}
		}
		if sendErr == nil {
			result, sendErr = transport.Sender.Send(ctx, out)
		}
	}
	d.RecordResult(result, sendErr)
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.Store.Put(finish, "delivery", operationID, d); err != nil {
		return err
	}
	if d.Status != "sent" {
		return errors.New(d.Error)
	}
	return nil
}
func (a *Service) Notify(ctx context.Context, snapshot taskentity.Snapshot, text, operationID string) error {
	if snapshot.Task.SessionID == "" {
		return nil
	}
	return a.NotifyRecord(ctx, message.Notification{ID: "notification-" + operationID, TaskID: snapshot.Task.ID, SessionID: snapshot.Task.SessionID, Text: text, OperationID: operationID}, false)
}

func (a *Service) NotifyRecord(ctx context.Context, n message.Notification, retry bool) error {
	unlock, err := a.Store.Lock(ctx, "notification:"+n.ID)
	if err != nil {
		return err
	}
	defer unlock()
	if gone, err := store.Purged(ctx, a.Store, "notification", n.ID); err != nil {
		return err
	} else if gone {
		return lifecycleRepo.ErrPurged
	}
	var old message.Notification
	if err := a.Store.Get(ctx, "notification", n.ID, &old); err == nil {
		done, err := old.CheckDuplicate(n)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		n = old
		if retry {
			if err := old.CheckRetry(); err != nil {
				return err
			}
			if err := a.notificationReplyReference(ctx, &n); err != nil {
				return err
			}
			n.OperationID = "notification-retry:" + n.ID + ":" + idgen.New()
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	var s conversation.Session
	if err := func() error {
		referenceUnlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
		if err != nil {
			return err
		}
		defer referenceUnlock()
		if err := convrepo.New(a.Store).GetSession(ctx, n.SessionID, &s); err != nil {
			return err
		}
		if err := a.notificationReplyReference(ctx, &n); err != nil {
			return err
		}
		n.BeginAttempt(time.Now().UTC())
		return a.Store.Put(ctx, "notification", n.ID, n)

	}(); err != nil {
		return err
	}
	if s.Channel != "qq" {
		n.Status = "saved"
	} else if a.Notifier == nil {
		n.Status = "failed"
		n.Error = "消息发送服务不可用"
		err = errors.New(n.Error)
	} else {
		err = a.Notifier(ctx, message.NotificationDelivery{SessionID: n.SessionID, Text: n.Text, OperationID: n.OperationID, ReplyTo: n.ReplyTo})
		deliveryStatus := ""
		if err != nil {
			var d message.Delivery
			readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			deliveryErr := a.Store.Get(readCtx, "delivery", n.OperationID, &d)
			readCancel()
			if deliveryErr == nil {
				deliveryStatus = d.Status
			}
		}
		n.RecordDelivery(err, deliveryStatus)
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	n.UpdatedAt = time.Now().UTC()
	if saveErr := a.Store.Put(finish, "notification", n.ID, n); saveErr != nil {
		return saveErr
	}
	return err
}
func (a *Service) notificationReplyReference(ctx context.Context, n *message.Notification) error {
	if n.ReplyTo != nil {
		return nil
	}
	// Notification identity remains stable across explicit message.Delivery retries.
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

func (a *Service) NotifyReply(ctx context.Context, run conversation.Run) error {
	if !run.NeedsReply() {
		return nil
	}
	text := run.ReplyText()
	n := message.Notification{ID: "notification-reply:" + run.ID, SessionID: run.SessionID, Text: text, OperationID: "reply:" + run.ID}
	notifyErr := a.NotifyRecord(ctx, n, false)
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.Store.Get(finish, "notification", n.ID, &n); err != nil {
		return errors.Join(notifyErr, err)
	}
	// Terminal outcomes have a visible record. Uncertain results never authorize a resend.
	if !n.Terminal() {
		return notifyErr
	}
	unlock, err := a.Store.Lock(finish, "run-state:"+run.ID)
	if err != nil {
		return errors.Join(notifyErr, err)
	}
	defer unlock()
	var current conversation.Run
	if err := convrepo.New(a.Store).GetRun(finish, run.ID, &current); err != nil {
		return errors.Join(notifyErr, err)
	}
	if current.NeedsReply() {
		current.ReplyPending = false
		if err := convrepo.New(a.Store).SaveRun(finish, current); err != nil {
			return errors.Join(notifyErr, err)
		}
	}
	return notifyErr
}

func (a *Service) ReconcileNotifications(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	runs, err := convrepo.New(a.Store).PendingReplies(ctx, 32)
	if err != nil {
		slog.Warn("load pending replies", "error", err)
		return
	}
	for _, run := range runs {
		if ctx.Err() != nil {
			return
		}
		attempt, stop := context.WithTimeout(ctx, 30*time.Second)
		err := a.NotifyReply(attempt, run)
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
		var n message.Notification
		if err := json.Unmarshal(encoded, &n); err != nil {
			slog.Warn("decode pending notification", "error", err)
			continue
		}
		attempt, stop := context.WithTimeout(ctx, 30*time.Second)
		err := a.NotifyRecord(attempt, n, false)
		stop()
		if err != nil {
			slog.Warn("reconcile notification", "notificationId", n.ID, "error", err)
		}
	}
}
func (a *Service) RegisterChannel(key string, c Channel) error {
	if key == "" || c.Sender == nil || c.Binding == nil {
		return errors.New("channel requires a key, sender and binding policy")
	}
	if _, exists := a.channels[key]; exists {
		return errors.New("channel already registered: " + key)
	}
	return a.ConfigureChannel(key, c)
}

// ConfigureChannel registers or replaces an adapter before serving requests or
// starting workers. It is not a concurrent runtime hot-switch operation.
func (a *Service) ConfigureChannel(key string, c Channel) error {
	if key == "" || c.Sender == nil || c.Binding == nil {
		return errors.New("channel requires a key, sender and binding policy")
	}
	if a.channels == nil {
		a.channels = map[string]Channel{}
	}
	a.channels[key] = c
	return nil
}

func (a *Service) IncomingHandler(route string) message.IncomingHandler {
	return func(ctx context.Context, in message.InboundMessage) error {
		in.Route = route
		return a.HandleIncoming(ctx, in)
	}
}

func (a *Service) ChannelStatus(ctx context.Context, route string) ChannelStatus {
	c, ok := a.channels[route]
	s := ChannelStatus{Provider: route, Implementation: c.Implementation,
		ConnectionStatus: message.ConnectionStatus{State: "unconfigured"}}
	if !ok {
		return s
	}
	if c.Status != nil {
		s.ConnectionStatus = c.Status.Status(ctx)
	} else {
		s.State, s.Configured = "unknown", true
	}
	b, err := c.Binding(ctx)
	switch {
	case err != nil:
		s.State, s.Error = "error", "无法读取绑定配置"
	case s.State == "configured" && (!b.Enabled || b.ConfigID == ""):
		s.State, s.Configured = "unconfigured", false
	case s.State == "online":
		if !b.Enabled {
			s.State = "disabled"
		} else if s.Account != "" && s.Account != b.Account {
			s.State = "account_mismatch"
		}
	}
	return s
}
func (a *Service) OneBotBinding(ctx context.Context) (OneBotBinding, error) {
	b := OneBotBinding{AllowedUserIDs: []string{}, AllowedGroupIDs: []string{}, ConfigID: a.Options.QQConfigID, PersonaID: a.Options.QQPersonaID}
	if b.PersonaID == "" {
		b.PersonaID = "secretary"
	}
	err := a.Store.Get(ctx, "qq-binding", "onebot", &b)
	if errors.Is(err, store.ErrNotFound) {
		err = nil
	}
	return b, err
}

func (a *Service) OneBotChannelBinding(ctx context.Context) (message.ChannelBinding, error) {
	b, err := a.OneBotBinding(ctx)
	return message.ChannelBinding{Enabled: b.Enabled, Account: b.SelfID, AllowedPeers: b.AllowedUserIDs, AllowedRooms: b.AllowedGroupIDs, ConfigID: b.ConfigID, PersonaID: b.PersonaID, RoomPersonaID: b.GroupPersonaID}, err
}

func (a *Service) OfficialChannelBinding(context.Context) (message.ChannelBinding, error) {
	return message.ChannelBinding{Enabled: a.Options.QQUser != "", Account: a.Options.QQAppID, AllowedPeers: []string{a.Options.QQUser}, ConfigID: a.Options.QQConfigID, PersonaID: a.Options.QQPersonaID, AllowLegacyAccount: true}, nil
}
