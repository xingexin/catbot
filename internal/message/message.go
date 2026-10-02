// Package message defines the secretary's transport-independent text contract.
package message

import (
	"context"
	"time"
)

type SendStatus string

const (
	Sent      SendStatus = "sent"
	Failed    SendStatus = "failed"
	Uncertain SendStatus = "uncertain"
)

// ReplyReference identifies the incoming message without exposing wire fields.
type ReplyReference struct {
	MessageID  string    `json:"messageId"`
	ReceivedAt time.Time `json:"receivedAt"`
}

type OutboundMessage struct {
	Account     string
	Peer        string
	Text        string
	OperationID string
	ReplyTo     *ReplyReference
}

// An error after dispatch must use Uncertain unless failure is confirmed.
// OperationID stays stable on retries; not all remote services support deduplication.
type SendResult struct {
	Status    SendStatus
	MessageID string
}

type Sender interface {
	Send(context.Context, OutboundMessage) (SendResult, error)
}

type InboundMessage struct {
	Route      string
	Account    string
	Peer       string
	MessageID  string
	Text       string
	ReceivedAt time.Time
}

type IncomingHandler func(context.Context, InboundMessage) error

// StatusChecker is optional; senders need not implement management operations.
type StatusChecker interface {
	Status(context.Context) ConnectionStatus
}

type ConnectionStatus struct {
	State      string `json:"state"`
	Configured bool   `json:"configured"`
	Account    string `json:"account,omitempty"`
	Nickname   string `json:"nickname,omitempty"`
	Error      string `json:"error,omitempty"`
}
