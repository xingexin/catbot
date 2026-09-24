package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
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

	"agentTest/internal/domain"
	"agentTest/internal/store"
)

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

type qqReceipt struct {
	MessageID  string    `json:"messageId"`
	ReceivedAt time.Time `json:"receivedAt"`
}

func (a *App) qqWebhook(w http.ResponseWriter, r *http.Request) {
	if a.Options.QQSecret == "" {
		JSON(w, 503, map[string]string{"error": "QQ is not configured"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		fail(w, err)
		return
	}
	if !verifyQQ(a.Options.QQSecret, r.Header.Get("X-Signature-Timestamp"), body, r.Header.Get("X-Signature-Ed25519")) {
		JSON(w, 401, map[string]string{"error": "invalid callback signature"})
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
		fail(w, err)
		return
	}
	if event.Op == 13 {
		sig := ed25519.Sign(qqKey(a.Options.QQSecret), []byte(event.Data.EventTS+event.Data.PlainToken))
		JSON(w, 200, map[string]string{"plain_token": event.Data.PlainToken, "signature": hex.EncodeToString(sig)})
		return
	}
	if event.Type != "C2C_MESSAGE_CREATE" {
		JSON(w, 200, map[string]int{"op": 12})
		return
	}
	if a.Options.QQUser == "" || event.Data.Author.OpenID != a.Options.QQUser {
		JSON(w, 200, map[string]int{"op": 12})
		return
	}
	if event.Data.ID == "" || strings.TrimSpace(event.Data.Content) == "" {
		JSON(w, 200, map[string]int{"op": 12})
		return
	}
	// Signed duplicate events deliberately receive the same run ID.
	sum := sha256.Sum256([]byte(event.Data.Author.OpenID + ":" + event.Data.ID))
	requestID := "qq-" + hex.EncodeToString(sum[:16])
	sessionID := "qq-" + a.Options.QQUser
	unlock, err := a.Store.Lock(r.Context(), "qq-session:"+sessionID)
	if err != nil {
		fail(w, err)
		return
	}
	var session domain.Session
	if err = a.Store.Get(r.Context(), "session", sessionID, &session); errors.Is(err, store.ErrNotFound) {
		if a.Options.QQConfigID == "" {
			unlock()
			JSON(w, 503, map[string]string{"error": "set QQ_CONFIG_ID before receiving messages"})
			return
		}
		personaID := a.Options.QQPersonaID
		if personaID == "" {
			personaID = "secretary"
		}
		session = domain.Session{ID: sessionID, Title: "QQ 私聊", Channel: "qq", Recipient: a.Options.QQUser, PersonaID: personaID, ConfigID: a.Options.QQConfigID, Messages: []domain.Message{}, Native: map[string]string{}}
		err = a.Store.Put(r.Context(), "session", sessionID, session)
	}
	unlock()
	if err != nil {
		fail(w, err)
		return
	}
	if err := a.Store.Put(r.Context(), "qq-receipt", "run-"+requestID, qqReceipt{MessageID: event.Data.ID, ReceivedAt: time.Now().UTC()}); err != nil {
		fail(w, err)
		return
	}
	if _, err := a.Submit(r.Context(), sessionID, strings.TrimSpace(event.Data.Content), requestID); err != nil {
		fail(w, err)
		return
	}
	JSON(w, 200, map[string]int{"op": 12})
}

type qqAccess struct {
	ID      string    `json:"id"`
	Expires time.Time `json:"expires"`
}

func (a *App) qqAccessToken(ctx context.Context) (string, error) {
	unlock, err := a.Store.Lock(ctx, "qq-access")
	if err != nil {
		return "", err
	}
	defer unlock()
	var existing qqAccess
	if a.Store.Get(ctx, "qq-access", "current", &existing) == nil && time.Until(existing.Expires) > time.Minute {
		return a.Vault.Get(ctx, existing.ID)
	}
	body, _ := json.Marshal(map[string]string{"appId": a.Options.QQAppID, "clientSecret": a.Options.QQSecret})
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
	if err := a.Vault.Set(ctx, "qq-access-token", "QQ access token", out.Token); err != nil {
		return "", err
	}
	err = a.Store.Put(ctx, "qq-access", "current", qqAccess{ID: "qq-access-token", Expires: time.Now().Add(time.Duration(seconds) * time.Second)})
	return out.Token, err
}

type delivery struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

func (a *App) SendQQ(ctx context.Context, sessionID, text, operationID string) error {
	var session domain.Session
	if err := a.Store.Get(ctx, "session", sessionID, &session); err != nil {
		return err
	}
	if session.Channel != "qq" {
		return nil
	}
	if session.Recipient != a.Options.QQUser || a.Options.QQUser == "" {
		return errors.New("QQ recipient is not bound")
	}
	// A large result stays available in Web; each reply is a single bounded send.
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
		if old.Status == "sent" {
			return nil
		}
		return errors.New("QQ delivery has a recorded failure or uncertain outcome; inspect before sending again")
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	token, err := a.qqAccessToken(ctx)
	if err != nil {
		_ = a.Store.Put(ctx, "delivery", operationID, delivery{ID: operationID, SessionID: sessionID, Status: "failed", Error: err.Error(), CreatedAt: time.Now().UTC()})
		return err
	}
	body := map[string]any{"content": text, "msg_type": 0}
	if strings.HasPrefix(operationID, "reply:") {
		var receipt qqReceipt
		if a.Store.Get(ctx, "qq-receipt", strings.TrimPrefix(operationID, "reply:"), &receipt) == nil && time.Since(receipt.ReceivedAt) < 5*time.Minute {
			body["msg_id"] = receipt.MessageID
			body["msg_seq"] = 1
		}
	}
	b, _ := json.Marshal(body)
	base := a.Options.QQBaseURL
	if base == "" {
		base = "https://api.bot.qq.com"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/v2/users/"+url.PathEscape(session.Recipient)+"/messages", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("X-Union-Appid", a.Options.QQAppID)
	req.Header.Set("Content-Type", "application/json")
	d := delivery{ID: operationID, SessionID: sessionID, Status: "uncertain", CreatedAt: time.Now().UTC()}
	if err := a.Store.Put(ctx, "delivery", operationID, d); err != nil {
		return err
	}
	resp, sendErr := (&http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if sendErr != nil {
		d.Error = "QQ transport interrupted; remote outcome unknown"
	} else {
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			d.Status = "sent"
		} else {
			d.Status = "failed"
			d.Error = fmt.Sprintf("QQ HTTP %d", resp.StatusCode)
		}
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
