package messaging

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

// GroupHistorySettings controls collection independently of Agent sessions.
type GroupHistorySettings struct {
	Enabled        bool     `json:"enabled"`
	RoomIDs        []string `json:"roomIds"`
	RetentionDays  int      `json:"retentionDays"`
	RecentMessages int      `json:"recentMessages"`
	MaxMessages    int      `json:"maxMessages"`
}

func DefaultGroupHistorySettings() GroupHistorySettings {
	return GroupHistorySettings{RetentionDays: 7, RecentMessages: 30, MaxMessages: 10000}
}

// Normalize preserves an explicit zero recent-message count (no automatic context).
func (s GroupHistorySettings) Normalize() (GroupHistorySettings, error) {
	if s.RetentionDays == 0 {
		s.RetentionDays = 7
	}
	if s.MaxMessages == 0 {
		s.MaxMessages = 10000
	}
	if s.RetentionDays < 1 || s.RetentionDays > 365 {
		return s, errors.New("group history retentionDays must be between 1 and 365")
	}
	if s.RecentMessages < 0 || s.RecentMessages > 100 {
		return s, errors.New("group history recentMessages must be between 0 and 100")
	}
	if s.MaxMessages < 100 || s.MaxMessages > 100000 {
		return s, errors.New("group history maxMessages must be between 100 and 100000")
	}
	rooms := make([]string, 0, len(s.RoomIDs))
	for _, room := range s.RoomIDs {
		room = strings.TrimSpace(room)
		if room == "" || len(room) > 256 {
			return s, errors.New("group history room ID must contain 1 to 256 bytes")
		}
		if !slices.Contains(rooms, room) {
			rooms = append(rooms, room)
		}
	}
	if len(rooms) > 1000 {
		return s, errors.New("group history supports at most 1000 rooms")
	}
	s.RoomIDs = rooms
	return s, nil
}

func (s GroupHistorySettings) AllowsRoom(roomID string) bool {
	return s.Enabled && slices.Contains(s.RoomIDs, roomID)
}

// GroupScope is shared within a room, unlike the per-speaker Agent session.
type GroupScope struct {
	Route   string `json:"route"`
	Account string `json:"account"`
	RoomID  string `json:"roomId"`
}

func (s GroupScope) Validate() error {
	for _, value := range []string{s.Route, s.Account, s.RoomID} {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return errors.New("group history requires a route, bot account and room ID (1 to 256 bytes each)")
		}
	}
	return nil
}

type GroupMessageDirection int

const (
	GroupMessageDirectionUnknown  GroupMessageDirection = 0
	GroupMessageDirectionIncoming GroupMessageDirection = 1
	GroupMessageDirectionOutgoing GroupMessageDirection = 2
)

type GroupMessage struct {
	ID         string                `json:"id"`
	SenderID   string                `json:"senderId"`
	SenderName string                `json:"senderName,omitempty"`
	Text       string                `json:"text"`
	SentAt     time.Time             `json:"sentAt"`
	Direction  GroupMessageDirection `json:"direction"`
	Truncated  bool                  `json:"truncated,omitempty"`
}

// GroupHistoryQuery uses inclusive time bounds and a descending (SentAt, ID)
// cursor. Scope always comes from trusted server-side authorization, not a cursor.
type GroupHistoryQuery struct {
	After    time.Time `json:"after,omitempty"`
	Before   time.Time `json:"before,omitempty"`
	Cursor   string    `json:"cursor,omitempty"`
	Keyword  string    `json:"keyword,omitempty"`
	SenderID string    `json:"senderId,omitempty"`
	Limit    int       `json:"limit,omitempty"`
	MaxBytes int       `json:"maxBytes,omitempty"`
}

type GroupHistoryCoverage struct {
	RecordedFrom   time.Time `json:"recordedFrom"`
	AvailableFrom  time.Time `json:"availableFrom"`
	AvailableUntil time.Time `json:"availableUntil"`
	RetentionDays  int       `json:"retentionDays"`
	MaxMessages    int       `json:"maxMessages"`
	LocalOnly      bool      `json:"localOnly"`
	Limited        bool      `json:"limited"`
}

type GroupHistoryPage struct {
	Messages   []GroupMessage       `json:"messages"`
	HasMore    bool                 `json:"hasMore"`
	NextCursor string               `json:"nextCursor,omitempty"`
	Coverage   GroupHistoryCoverage `json:"coverage"`
	Truncated  bool                 `json:"truncated"`
}

type GroupHistoryRepository interface {
	ApplySettings(context.Context, GroupScope, GroupHistorySettings) error
	Append(context.Context, GroupScope, GroupMessage, GroupHistorySettings) error
	Query(context.Context, GroupScope, GroupHistoryQuery) (GroupHistoryPage, error)
	Prune(context.Context, time.Time) error
}
