package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"agentTest/internal/domain"
	"agentTest/internal/message"
	"agentTest/internal/store"
)

// HandleIncoming applies the same authorization, deduplication and execution path
// to every registered transport. Unauthorized contacts are acknowledged and ignored.
func (a *App) HandleIncoming(ctx context.Context, in message.InboundMessage) error {
	channel, ok := a.channels[in.Route]
	if !ok {
		return errors.New("message channel is not registered: " + in.Route)
	}
	binding, err := channel.Binding(ctx)
	if err != nil {
		return err
	}
	if !binding.allows(in.Account, in.Peer, false) {
		return nil
	}
	if in.MessageID == "" || strings.TrimSpace(in.Text) == "" {
		return nil
	}
	if binding.ConfigID == "" {
		return errors.New("select a channel execution configuration first")
	}
	if binding.PersonaID == "" {
		binding.PersonaID = "secretary"
	}
	identity := in.Route + ":" + in.Account + ":" + in.Peer
	sum := sha256.Sum256([]byte(identity))
	sessionID := "qq-" + in.Route + "-" + hex.EncodeToString(sum[:16])
	sum = sha256.Sum256([]byte(identity + ":" + in.MessageID))
	requestID := "qq-" + hex.EncodeToString(sum[:16])
	// Serialize duplicate callbacks without holding the conversation execution lock.
	unlock, err := a.Store.Lock(ctx, "qq-incoming:"+sessionID)
	if err != nil {
		return err
	}
	defer unlock()
	var session domain.Session
	err = a.Store.Get(ctx, "session", sessionID, &session)
	if errors.Is(err, store.ErrNotFound) {
		title := channel.Title + " · " + in.Peer
		session = domain.Session{ID: sessionID, Title: title, Channel: "qq",
			ChannelProvider: in.Route, ChannelAccount: in.Account, Recipient: in.Peer,
			PersonaID: binding.PersonaID, ConfigID: binding.ConfigID, Messages: []domain.Message{}, Native: map[string]string{}}
		err = a.Store.Put(ctx, "session", sessionID, session)
	}
	if err != nil {
		return err
	}
	var receipt message.ReplyReference
	err = a.Store.Get(ctx, "qq-receipt", "run-"+requestID, &receipt)
	if errors.Is(err, store.ErrNotFound) {
		err = a.Store.Put(ctx, "qq-receipt", "run-"+requestID, message.ReplyReference{MessageID: in.MessageID, ReceivedAt: in.ReceivedAt})
	}
	if err != nil {
		return err
	}
	_, err = a.Submit(ctx, sessionID, strings.TrimSpace(in.Text), requestID)
	return err
}

type delivery struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	Provider  string    `json:"provider,omitempty"`
	MessageID string    `json:"messageId,omitempty"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// SendMessage persists delivery intent before invoking any registered Sender.
func (a *App) SendMessage(ctx context.Context, sessionID, text, operationID string) error {
	var session domain.Session
	if err := a.Store.Get(ctx, "session", sessionID, &session); err != nil {
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
	unlock, err := a.Store.Lock(ctx, "delivery:"+operationID)
	if err != nil {
		return err
	}
	defer unlock()
	var old delivery
	if err := a.Store.Get(ctx, "delivery", operationID, &old); err == nil {
		if old.SessionID != sessionID {
			return errors.New("delivery operation belongs to another session")
		}
		if old.Status == "sent" {
			return nil
		}
		return errors.New("message delivery has a recorded failure or uncertain outcome; inspect before sending again")
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	// Persist intent BEFORE contacting the platform. A crash cannot cause an
	// automatic second send whose first remote outcome is unknown.
	d := delivery{ID: operationID, SessionID: sessionID, Provider: provider, Status: "uncertain", CreatedAt: time.Now().UTC()}
	if err := a.Store.Put(ctx, "delivery", operationID, d); err != nil {
		return err
	}
	result := message.SendResult{Status: message.Failed}
	binding, sendErr := transport.Binding(ctx)
	if sendErr == nil && !binding.allows(session.ChannelAccount, session.Recipient, true) {
		sendErr = errors.New("message account or recipient is not bound")
	}
	if sendErr == nil {
		out := message.OutboundMessage{Account: session.ChannelAccount, Peer: session.Recipient, Text: text, OperationID: operationID}
		if out.Account == "" && binding.AllowLegacyAccount {
			out.Account = binding.Account
		}
		if strings.HasPrefix(operationID, "reply:") {
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
	// A missing/invalid result or contradictory success is never safe to retry.
	switch result.Status {
	case message.Sent:
		if sendErr != nil {
			result.Status = message.Uncertain
		}
	case message.Failed, message.Uncertain:
	default:
		result.Status = message.Uncertain
	}
	d.Status, d.MessageID = string(result.Status), result.MessageID
	if sendErr != nil {
		d.Error = sendErr.Error()
	} else if result.Status != message.Sent {
		d.Error = "message delivery " + d.Status
	}
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
