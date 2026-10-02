// Package onebot adapts the OneBot 11 HTTP protocol, regardless of server implementation.
package onebot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentTest/internal/message"
	"agentTest/internal/transport/httpio"
)

type Options struct{ URL, Token string }
type Adapter struct {
	options  Options
	incoming message.IncomingHandler
}

func New(options Options, incoming message.IncomingHandler) *Adapter {
	return &Adapter{options: options, incoming: incoming}
}

var _ message.Sender = (*Adapter)(nil)
var _ message.StatusChecker = (*Adapter)(nil)

// OneBot IDs can be JSON numbers or strings; avoid float64 precision loss.
type oneBotID string

func (id *oneBotID) UnmarshalJSON(b []byte) error {
	var s string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else {
		s = string(b)
	}
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		return errors.New("invalid OneBot ID")
	}
	*id = oneBotID(s)
	return nil
}

func validQQID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

type oneBotReply struct {
	Status  string          `json:"status"`
	Retcode *int            `json:"retcode"`
	Data    json.RawMessage `json:"data"`
}

// Call returns a failure classification as well as an error: malformed replies
// and transport failures after dispatch cannot prove whether a send succeeded.
func (q *Adapter) call(ctx context.Context, action string, args any, out any) (string, error) {
	o := q.options
	if o.URL == "" || o.Token == "" {
		return "failed", errors.New("OneBot endpoint and token are required")
	}
	body, err := json.Marshal(args)
	if err != nil {
		return "failed", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(o.URL, "/")+"/"+action, bytes.NewReader(body))
	if err != nil {
		return "failed", errors.New("invalid OneBot endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+o.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpio.Client().Do(req)
	if err != nil {
		return "uncertain", errors.New("OneBot transport unavailable; remote outcome unknown")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		state := "uncertain"
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			state = "failed"
		}
		return state, fmt.Errorf("OneBot HTTP %d", resp.StatusCode)
	}
	var reply oneBotReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply); err != nil || reply.Retcode == nil {
		return "uncertain", errors.New("invalid OneBot response; remote outcome unknown")
	}
	if reply.Status == "failed" {
		return "failed", fmt.Errorf("OneBot action rejected (retcode %d)", *reply.Retcode)
	}
	if reply.Status != "ok" || *reply.Retcode != 0 {
		return "uncertain", errors.New("OneBot action did not confirm completion")
	}
	if out != nil {
		if len(reply.Data) == 0 || string(reply.Data) == "null" || json.Unmarshal(reply.Data, out) != nil {
			return "uncertain", errors.New("invalid OneBot result; remote outcome unknown")
		}
	}
	return "sent", nil
}

func (q *Adapter) login(ctx context.Context) (string, string, error) {
	var login struct {
		UserID   oneBotID `json:"user_id"`
		Nickname string   `json:"nickname"`
	}
	if _, err := q.call(ctx, "get_login_info", map[string]any{}, &login); err != nil {
		return "", "", err
	}
	if !validQQID(string(login.UserID)) {
		return "", "", errors.New("OneBot has no logged-in account")
	}
	var health struct {
		Online bool `json:"online"`
		Good   bool `json:"good"`
	}
	if _, err := q.call(ctx, "get_status", map[string]any{}, &health); err != nil {
		return "", "", err
	}
	if !health.Online || !health.Good {
		return "", "", errors.New("OneBot account is offline")
	}
	return string(login.UserID), login.Nickname, nil
}

func (q *Adapter) Status(ctx context.Context) message.ConnectionStatus {
	s := message.ConnectionStatus{State: "unconfigured", Configured: q.options.URL != "" && q.options.Token != ""}
	if !s.Configured {
		return s
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	account, nickname, err := q.login(ctx)
	if err != nil {
		s.State, s.Error = "unavailable", "消息接入服务尚未登录、离线或接口不可用"
		return s
	}
	s.Account, s.Nickname = account, nickname
	s.State = "online"
	return s
}

func (q *Adapter) Receive(w http.ResponseWriter, r *http.Request) {
	if q.options.Token == "" {
		httpio.JSON(w, 503, map[string]string{"error": "OneBot is not configured"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		httpio.Fail(w, err)
		return
	}
	mac := hmac.New(sha1.New, []byte(q.options.Token))
	_, _ = mac.Write(body)
	signature := r.Header.Get("X-Signature")
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "sha1="))
	if err != nil || !strings.HasPrefix(signature, "sha1=") || !hmac.Equal(sig, mac.Sum(nil)) {
		httpio.JSON(w, 401, map[string]string{"error": "invalid OneBot callback signature"})
		return
	}
	var event struct {
		Time        int64           `json:"time"`
		SelfID      oneBotID        `json:"self_id"`
		UserID      oneBotID        `json:"user_id"`
		MessageID   oneBotID        `json:"message_id"`
		PostType    string          `json:"post_type"`
		MessageType string          `json:"message_type"`
		Message     json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		httpio.Fail(w, errors.New("invalid OneBot event"))
		return
	}
	// Meta events, groups and our own outbound echoes never invoke the agent.
	if event.PostType != "message" || event.MessageType != "private" || event.UserID == event.SelfID {
		httpio.JSON(w, 200, map[string]any{})
		return
	}
	// Authenticated but stale events must not start fresh work after a long outage.
	if event.Time <= 0 || time.Since(time.Unix(event.Time, 0)) > 10*time.Minute || time.Until(time.Unix(event.Time, 0)) > time.Minute {
		httpio.JSON(w, 200, map[string]any{})
		return
	}
	text, err := oneBotText(event.Message)
	if err != nil {
		httpio.Fail(w, err)
		return
	}
	if strings.TrimSpace(text) == "" || event.MessageID == "" {
		httpio.JSON(w, 200, map[string]any{})
		return
	}
	if err := q.incoming(r.Context(), message.InboundMessage{
		Account: string(event.SelfID), Peer: string(event.UserID), MessageID: string(event.MessageID),
		Text: text, ReceivedAt: time.Unix(event.Time, 0),
	}); err != nil {
		httpio.Fail(w, err)
		return
	}
	httpio.JSON(w, 200, map[string]any{})
}

func oneBotText(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		// CQ codes are not text content. Our managed deployment uses array format.
		if strings.Contains(text, "[CQ:") {
			return "", errors.New("use OneBot array messagePostFormat for mixed messages")
		}
		return text, nil
	}
	var segments []struct {
		Type string `json:"type"`
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &segments); err != nil {
		return "", errors.New("invalid OneBot message segments")
	}
	var b strings.Builder
	for _, segment := range segments {
		if segment.Type == "text" {
			b.WriteString(segment.Data.Text)
		} else {
			// Do not silently treat an attachment as though it was understood.
			b.WriteString("[QQ 非文本内容未解析]")
		}
	}
	return b.String(), nil
}

func (q *Adapter) Send(ctx context.Context, in message.OutboundMessage) (message.SendResult, error) {
	account, _, err := q.login(ctx)
	if err != nil {
		return message.SendResult{Status: message.Failed}, errors.New("OneBot login could not be verified; message not sent")
	}
	if account != in.Account {
		return message.SendResult{Status: message.Failed}, errors.New("OneBot logged-in account changed; message not sent")
	}
	if !validQQID(in.Peer) {
		return message.SendResult{Status: message.Failed}, errors.New("invalid OneBot recipient")
	}
	var out struct {
		MessageID oneBotID `json:"message_id"`
	}
	state, err := q.call(ctx, "send_private_msg", map[string]any{
		"user_id": json.Number(in.Peer),
		"message": []any{map[string]any{"type": "text", "data": map[string]string{"text": in.Text}}},
	}, &out)
	if err != nil {
		return message.SendResult{Status: message.SendStatus(state)}, err
	}
	if out.MessageID == "" {
		return message.SendResult{Status: message.Uncertain}, errors.New("OneBot omitted message_id; remote outcome unknown")
	}
	return message.SendResult{Status: message.Sent, MessageID: string(out.MessageID)}, nil
}
