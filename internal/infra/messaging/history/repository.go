package history

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/infra/store"
)

const namespace = "group-history"

var ErrUnsupported = errors.New("group history requires a time-range capable store")

// Repository stores only shared room observations; it has no Agent/session access.
type Repository struct {
	backend store.TimeSeriesStore
	now     func() time.Time
}

var _ messaging.GroupHistoryRepository = (*Repository)(nil)

func New(s store.Store) *Repository {
	backend, _ := s.(store.TimeSeriesStore)
	return &Repository{backend: backend, now: time.Now}
}

func scopeKey(scope messaging.GroupScope) string {
	encoded, _ := json.Marshal(scope)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func (r *Repository) ApplySettings(ctx context.Context, scope messaging.GroupScope, settings messaging.GroupHistorySettings) error {
	if r.backend == nil {
		return ErrUnsupported
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	settings, err := settings.Normalize()
	if err != nil {
		return err
	}
	return r.backend.ConfigureSeries(ctx, namespace, scopeKey(scope), policy(settings), precise(r.now()))
}

func policy(settings messaging.GroupHistorySettings) store.SeriesPolicy {
	return store.SeriesPolicy{Retention: time.Duration(settings.RetentionDays) * 24 * time.Hour, MaxRecords: settings.MaxMessages}
}

func precise(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func (r *Repository) Append(ctx context.Context, scope messaging.GroupScope, msg messaging.GroupMessage, settings messaging.GroupHistorySettings) error {
	if r.backend == nil {
		return ErrUnsupported
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	settings, err := settings.Normalize()
	if err != nil {
		return err
	}
	if !settings.AllowsRoom(scope.RoomID) {
		return errors.New("group history collection is disabled for this room")
	}
	if msg.ID == "" || len(msg.ID) > 256 || msg.SenderID == "" || len(msg.SenderID) > 256 || len(msg.SenderName) > 256 {
		return errors.New("invalid group message identity")
	}
	if !utf8.ValidString(msg.Text) || !utf8.ValidString(msg.ID) || !utf8.ValidString(msg.SenderID) || !utf8.ValidString(msg.SenderName) || strings.TrimSpace(msg.Text) == "" {
		return errors.New("group message must contain valid UTF-8 text")
	}
	if msg.SentAt.IsZero() || msg.SentAt.After(r.now().Add(time.Minute)) {
		return errors.New("invalid group message timestamp")
	}
	switch msg.Direction {
	case messaging.GroupMessageDirectionIncoming, messaging.GroupMessageDirectionOutgoing:
	default:
		return errors.New("invalid group message direction")
	}
	if len(msg.Text) > 16*1024 {
		msg.Text = utf8Prefix(msg.Text, 16*1024)
		msg.Truncated = true
	}
	msg.SentAt = precise(msg.SentAt)
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	entry := store.SeriesEntry{ID: msg.ID, Source: msg.SenderID, Text: msg.Text, Time: msg.SentAt, Data: data}
	return r.backend.AppendSeries(ctx, namespace, scopeKey(scope), entry, policy(settings), precise(r.now()))
}

func (r *Repository) Prune(ctx context.Context, now time.Time) error {
	if r.backend == nil {
		return ErrUnsupported
	}
	return r.backend.PruneSeries(ctx, namespace, precise(now))
}

type cursor struct {
	Scope    string    `json:"s"`
	After    time.Time `json:"a"`
	Before   time.Time `json:"b"`
	Time     time.Time `json:"t"`
	ID       string    `json:"i"`
	Keyword  string    `json:"k,omitempty"`
	SenderID string    `json:"u,omitempty"`
}

func queryCursor(scope messaging.GroupScope, q *messaging.GroupHistoryQuery, now time.Time) (cursor, error) {
	c := cursor{Scope: scopeKey(scope)}
	if q.Cursor != "" {
		if len(q.Cursor) > 4096 {
			return c, errors.New("invalid group history cursor")
		}
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(data, &c) != nil || c.Scope != scopeKey(scope) || c.Time.IsZero() || c.Before.IsZero() || c.ID == "" {
			return c, errors.New("invalid group history cursor")
		}
		if (!q.After.IsZero() && !precise(q.After).Equal(c.After)) || (!q.Before.IsZero() && !precise(q.Before).Equal(c.Before)) ||
			(q.Keyword != "" && q.Keyword != c.Keyword) || (q.SenderID != "" && q.SenderID != c.SenderID) {
			return c, errors.New("group history cursor does not match the query")
		}
		q.After, q.Before, q.Keyword, q.SenderID = c.After, c.Before, c.Keyword, c.SenderID
	}
	if q.Before.IsZero() {
		q.Before = now
	}
	q.Before, q.After = precise(q.Before), precise(q.After)
	if q.After.After(q.Before) || len(q.Keyword) > 256 || len(q.SenderID) > 256 || !utf8.ValidString(q.Keyword) {
		return c, errors.New("invalid group history time range or filter")
	}
	if q.Limit == 0 {
		q.Limit = 30
	}
	if q.MaxBytes == 0 {
		q.MaxBytes = 32 * 1024
	}
	if q.Limit < 1 || q.Limit > 200 || q.MaxBytes < 2048 || q.MaxBytes > 64*1024 {
		return c, errors.New("group history limit must be 1-200 and maxBytes 2048-65536")
	}
	c.After, c.Before, c.Keyword, c.SenderID = q.After, q.Before, q.Keyword, q.SenderID
	return c, nil
}

func (r *Repository) Query(ctx context.Context, scope messaging.GroupScope, q messaging.GroupHistoryQuery) (messaging.GroupHistoryPage, error) {
	page := messaging.GroupHistoryPage{Messages: []messaging.GroupMessage{}, Coverage: messaging.GroupHistoryCoverage{LocalOnly: true}}
	if r.backend == nil {
		return page, ErrUnsupported
	}
	if err := scope.Validate(); err != nil {
		return page, err
	}
	now := precise(r.now())
	c, err := queryCursor(scope, &q, now)
	if err != nil {
		return page, err
	}
	result, err := r.backend.QuerySeries(ctx, namespace, scopeKey(scope), store.SeriesQuery{
		From: q.After, Until: q.Before, Now: now, BeforeTime: c.Time, BeforeID: c.ID, Source: q.SenderID, Keyword: q.Keyword, Limit: q.Limit,
	})
	if err != nil {
		return page, err
	}
	page.Coverage.RecordedFrom = result.RecordedFrom
	page.Coverage.AvailableFrom, page.Coverage.AvailableUntil = result.AvailableFrom, result.AvailableUntil
	page.Coverage.RetentionDays, page.Coverage.MaxMessages = int(result.Policy.Retention/(24*time.Hour)), result.Policy.MaxRecords
	page.Coverage.Limited = result.RecordedFrom.IsZero() || q.After.Before(result.RecordedFrom) ||
		q.After.Before(now.Add(-result.Policy.Retention)) || !result.DroppedBefore.Before(q.After)
	page.HasMore = result.HasMore
	for _, entry := range result.Entries {
		var message messaging.GroupMessage
		if err := json.Unmarshal(entry.Data, &message); err != nil {
			return page, errors.New("invalid stored group message")
		}
		page.Messages = append(page.Messages, message)
		page.Truncated = page.Truncated || message.Truncated
	}
	if err := fitPage(&page, c, q.MaxBytes); err != nil {
		return messaging.GroupHistoryPage{}, err
	}
	return page, nil
}

func setNextCursor(page *messaging.GroupHistoryPage, c cursor) {
	page.NextCursor = ""
	if page.HasMore && len(page.Messages) > 0 {
		last := page.Messages[len(page.Messages)-1]
		c.ID, c.Time = last.ID, last.SentAt
		data, _ := json.Marshal(c)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
}

func fitPage(page *messaging.GroupHistoryPage, c cursor, maxBytes int) error {
	setNextCursor(page, c)
	for {
		data, _ := json.Marshal(page)
		if len(data) <= maxBytes {
			return nil
		}
		if len(page.Messages) == 0 {
			return errors.New("group history metadata exceeds maxBytes; increase the byte budget")
		}
		page.Truncated = true
		if len(page.Messages) > 1 {
			page.Messages = page.Messages[:len(page.Messages)-1]
			page.HasMore = true
			setNextCursor(page, c)
			continue
		}
		msg := &page.Messages[0]
		msg.Truncated = true
		// Escape expansion makes JSON size differ from raw UTF-8 size.
		low, high := 0, len(msg.Text)
		original := msg.Text
		for low < high {
			mid := (low + high + 1) / 2
			msg.Text = utf8Prefix(original, mid)
			data, _ := json.Marshal(page)
			if len(data) <= maxBytes {
				low = mid
			} else {
				high = mid - 1
			}
		}
		msg.Text = utf8Prefix(original, low)
		data, _ = json.Marshal(page)
		if len(data) > maxBytes {
			return errors.New("group history metadata exceeds maxBytes; increase the byte budget")
		}
		return nil
	}
}

func utf8Prefix(text string, size int) string {
	if size >= len(text) {
		return text
	}
	for size > 0 && !utf8.RuneStart(text[size]) {
		size--
	}
	return text[:size]
}
