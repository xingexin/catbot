package messaging

import (
	"time"
)

type Delivery struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	Provider  string    `json:"provider,omitempty"`
	MessageID string    `json:"messageId,omitempty"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type NotificationDelivery struct {
	SessionID   string
	Text        string
	OperationID string
	ReplyTo     *ReplyReference
}
type Notification struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"taskId,omitempty"`
	PluginID    string          `json:"pluginId,omitempty"`
	SessionID   string          `json:"sessionId"`
	Text        string          `json:"text"`
	Status      string          `json:"status"`
	Error       string          `json:"error,omitempty"`
	OperationID string          `json:"operationId"`
	Attempts    int             `json:"attempts"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	ReplyTo     *ReplyReference `json:"replyTo,omitempty"`
}
