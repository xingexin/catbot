package conversation

import (
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
	"time"
)

type Session struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	PersonaID       string            `json:"personaId"`
	ConfigID        string            `json:"configId"`
	Channel         string            `json:"channel"`
	ChannelProvider string            `json:"channelProvider,omitempty"`
	ChannelAccount  string            `json:"channelAccount,omitempty"`
	ChannelRoom     string            `json:"channelRoom,omitempty"`
	Recipient       string            `json:"recipient,omitempty"`
	OriginSessionID string            `json:"originSessionId,omitempty"`
	Messages        []agent.Message   `json:"messages"`
	Summary         string            `json:"summary"`
	Native          map[string]string `json:"native"`
	ActiveConfig    string            `json:"activeConfig,omitempty"`
}

type Run struct {
	ID           string            `json:"id"`
	SessionID    string            `json:"sessionId"`
	Prompt       string            `json:"prompt"`
	Status       string            `json:"status"`
	Config       agent.Config      `json:"config"`
	Persona      persona.Persona   `json:"persona"`
	Versions     map[string]string `json:"versions"`
	Result       string            `json:"result"`
	Error        string            `json:"error,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
	FinishedAt   *time.Time        `json:"finishedAt,omitempty"`
	Usage        map[string]int    `json:"usage,omitempty"`
	NativeID     string            `json:"nativeId,omitempty"`
	ReplyPending bool              `json:"replyPending,omitempty"`
}

type Event struct {
	Sequence int64          `json:"sequence"`
	RunID    string         `json:"runId"`
	Type     string         `json:"type"`
	Data     map[string]any `json:"data"`
	Time     time.Time      `json:"time"`
}
