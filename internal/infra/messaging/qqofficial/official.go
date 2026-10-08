// Package qqofficial implements the official QQ private-message HTTP protocol.
package qqofficial

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	message "github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/infra/messaging/httpio"
)

type Options struct{ AppID, Secret, BaseURL string }

// TokenCache stores credentials outside the adapter and serializes refreshes.
// Get returns an empty token and nil error for an absent cache entry.
type TokenCache interface {
	Lock(context.Context) (func(), error)
	Get(context.Context) (string, time.Time, error)
	Put(context.Context, string, time.Time) error
}
type Adapter struct {
	options  Options
	cache    TokenCache
	incoming message.IncomingHandler
}

func New(options Options, cache TokenCache, incoming message.IncomingHandler) *Adapter {
	return &Adapter{options: options, cache: cache, incoming: incoming}
}

var _ message.Sender = (*Adapter)(nil)
var _ message.StatusChecker = (*Adapter)(nil)

func (q *Adapter) Status(context.Context) message.ConnectionStatus {
	configured := q.options.AppID != "" && q.options.Secret != ""
	state := "unconfigured"
	if configured {
		state = "configured"
	}
	return message.ConnectionStatus{State: state, Configured: configured, Account: q.options.AppID}
}
func qqKey(secret string) ed25519.PrivateKey {
	if secret == "" {
		return nil
	}
	seed := []byte(secret)
	for len(seed) < ed25519.SeedSize {
		seed = append(seed, seed...)
	}
	return ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
}
func verifyQQ(secret, timestamp string, body []byte, signature string) bool {
	key := qqKey(secret)
	sig, err := hex.DecodeString(signature)
	if key == nil || timestamp == "" || err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(key.Public().(ed25519.PublicKey), append([]byte(timestamp), body...), sig)
}

func (q *Adapter) Receive(w http.ResponseWriter, r *http.Request) {
	if q.options.Secret == "" {
		httpio.JSON(w, 503, map[string]string{"error": "QQ is not configured"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		httpio.Fail(w, err)
		return
	}
	if !verifyQQ(q.options.Secret, r.Header.Get("X-Signature-Timestamp"), body, r.Header.Get("X-Signature-Ed25519")) {
		httpio.JSON(w, 401, map[string]string{"error": "invalid callback signature"})
		return
	}
	var event struct {
		Op   int    `json:"op"`
		Type string `json:"t"`
		ID   string `json:"id"`
		Data struct {
			PlainToken string `json:"plain_token"`
			EventTS    string `json:"event_ts"`
			ID         string `json:"id"`
			Content    string `json:"content"`
			Author     struct {
				OpenID string `json:"user_openid"`
			} `json:"author"`
		} `json:"d"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		httpio.Fail(w, err)
		return
	}
	if event.Op == 13 {
		sig := ed25519.Sign(qqKey(q.options.Secret), []byte(event.Data.EventTS+event.Data.PlainToken))
		httpio.JSON(w, 200, map[string]string{"plain_token": event.Data.PlainToken, "signature": hex.EncodeToString(sig)})
		return
	}
	if event.Type != "C2C_MESSAGE_CREATE" {
		httpio.JSON(w, 200, map[string]int{"op": 12})
		return
	}
	if event.Data.ID == "" || strings.TrimSpace(event.Data.Content) == "" {
		httpio.JSON(w, 200, map[string]int{"op": 12})
		return
	}
	if err := q.incoming(r.Context(), message.InboundMessage{
		Account: q.options.AppID, Peer: event.Data.Author.OpenID,
		MessageID: event.Data.ID, Text: event.Data.Content, ReceivedAt: time.Now().UTC(),
	}); err != nil {
		httpio.Fail(w, err)
		return
	}
	httpio.JSON(w, 200, map[string]int{"op": 12})
}

func (q *Adapter) accessToken(ctx context.Context) (string, error) {
	unlock, err := q.cache.Lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	cached, expires, err := q.cache.Get(ctx)
	if err != nil {
		return "", err
	}
	if cached != "" && time.Until(expires) > time.Minute {
		return cached, nil
	}
	body, _ := json.Marshal(map[string]string{"appId": q.options.AppID, "clientSecret": q.options.Secret})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://bots.qq.com/app/getAppAccessToken", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return "", errors.New("QQ access token request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("QQ token HTTP %d", resp.StatusCode)
	}
	var out struct {
		Token   string          `json:"access_token"`
		Expires json.RawMessage `json:"expires_in"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.Token == "" {
		return "", errors.New("invalid QQ token response")
	}
	seconds, _ := strconv.Atoi(strings.Trim(string(out.Expires), "\""))
	if seconds <= 0 {
		seconds = 300
	}
	err = q.cache.Put(ctx, out.Token, time.Now().Add(time.Duration(seconds)*time.Second))
	return out.Token, err
}
func (q *Adapter) Send(ctx context.Context, in message.OutboundMessage) (message.SendResult, error) {
	if in.RoomID != "" {
		return message.SendResult{Status: message.Failed}, errors.New("official QQ group delivery is not supported by this adapter")
	}
	token, err := q.accessToken(ctx)
	if err != nil {
		return message.SendResult{Status: message.Failed}, err
	}
	body := map[string]any{"content": in.Text, "msg_type": 0}
	if receipt := in.ReplyTo; receipt != nil && time.Since(receipt.ReceivedAt) >= 0 && time.Since(receipt.ReceivedAt) < 5*time.Minute {
		body["msg_id"] = receipt.MessageID
		body["msg_seq"] = 1
	}
	b, _ := json.Marshal(body)
	base := q.options.BaseURL
	if base == "" {
		base = "https://api.bot.qq.com"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/v2/users/"+url.PathEscape(in.Peer)+"/messages", bytes.NewReader(b))
	if err != nil {
		return message.SendResult{Status: message.Failed}, errors.New("invalid official QQ endpoint")
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("X-Union-Appid", q.options.AppID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpio.Client().Do(req)
	if err != nil {
		return message.SendResult{Status: message.Uncertain}, errors.New("QQ transport interrupted; remote outcome unknown")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		state := message.Failed
		if resp.StatusCode >= 500 {
			state = message.Uncertain
		}
		return message.SendResult{Status: state}, fmt.Errorf("QQ HTTP %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.ID == "" {
		return message.SendResult{Status: message.Uncertain}, errors.New("QQ omitted message ID; remote outcome unknown")
	}
	return message.SendResult{Status: message.Sent, MessageID: out.ID}, nil
}
