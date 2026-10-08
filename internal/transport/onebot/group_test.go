package onebot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"agentTest/internal/message"
)

func TestGroupMentionsAreStructuredAndAddressedToBot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		message  string
		accepted bool
		text     string
	}{
		{"string bot ID", `[{"type":"at","data":{"qq":"10001"}},{"type":"text","data":{"text":"hello"}}]`, true, "hello"},
		{"numeric bot ID", `[{"type":"at","data":{"qq":10001}},{"type":"text","data":{"text":"hello"}}]`, true, "hello"},
		{"all members", `[{"type":"at","data":{"qq":"all"}},{"type":"text","data":{"text":"hello"}}]`, false, ""},
		{"another member", `[{"type":"at","data":{"qq":"99999"}},{"type":"text","data":{"text":"hello"}}]`, false, ""},
		{"ordinary group text", `[{"type":"text","data":{"text":"hello @10001"}}]`, false, ""},
		{"only reply reference", `[{"type":"reply","data":{"id":"123"}},{"type":"text","data":{"text":"hello"}}]`, false, ""},
		{"empty mention", `[{"type":"at","data":{"qq":"10001"}},{"type":"text","data":{"text":" "}}]`, false, ""},
		{"reply with mention", `[{"type":"reply","data":{"id":"123"}},{"type":"at","data":{"qq":"10001"}},{"type":"text","data":{"text":"hello"}}]`, true, "hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received []message.InboundMessage
			a := New(Options{Token: "fixture"}, func(_ context.Context, in message.InboundMessage) error {
				received = append(received, in)
				return nil
			})
			body, _ := json.Marshal(map[string]any{"time": time.Now().Unix(), "post_type": "message", "message_type": "group", "self_id": 10001, "user_id": 20002, "group_id": 30003, "message_id": -42, "message": json.RawMessage(tc.message)})
			mac := hmac.New(sha1.New, []byte("fixture"))
			_, _ = mac.Write(body)
			r := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(body))
			r.Header.Set("X-Signature", "sha1="+hex.EncodeToString(mac.Sum(nil)))
			w := httptest.NewRecorder()
			a.Receive(w, r)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if !tc.accepted {
				if len(received) != 0 {
					t.Fatal("non-addressed message invoked the agent")
				}
				return
			}
			if len(received) != 1 {
				t.Fatalf("received %d messages", len(received))
			}
			got := received[0]
			if got.RoomID != "30003" || got.Peer != "20002" || got.Account != "10001" || !got.Mentioned || got.Text != tc.text || got.MessageID != "-42" {
				t.Fatalf("bad normalized message: %#v", got)
			}
		})
	}
}

func TestGroupSendUsesRoomAndStructuredMention(t *testing.T) {
	for _, tc := range []struct {
		name, reply, status string
		code                int
		expected            message.SendStatus
	}{
		{"reply", "-42", "ok", 200, message.Sent},
		{"scheduled reminder", "", "ok", 200, message.Sent},
		{"explicit rejection", "", "failed", 200, message.Failed},
		{"unknown server result", "", "ok", 502, message.Uncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sends atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("missing auth")
				}
				switch r.URL.Path {
				case "/get_login_info":
					_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"user_id":10001}}`))
				case "/get_status":
					_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"online":true,"good":true}}`))
				case "/send_group_msg":
					sends.Add(1)
					var body struct {
						GroupID int64  `json:"group_id"`
						UserID  *int64 `json:"user_id"`
						Message []struct {
							Type string            `json:"type"`
							Data map[string]string `json:"data"`
						} `json:"message"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					if body.GroupID != 30003 || body.UserID != nil {
						t.Errorf("wrong destination: %#v", body)
					}
					segments := body.Message
					if tc.reply != "" {
						if len(segments) != 3 || segments[0].Type != "reply" || segments[0].Data["id"] != tc.reply {
							t.Errorf("missing reply reference: %#v", segments)
							return
						}
						segments = segments[1:]
					}
					if len(segments) != 2 || segments[0].Type != "at" || segments[0].Data["qq"] != "20002" || segments[1].Type != "text" || segments[1].Data["text"] != " literal [CQ:at,qq=all]" {
						t.Errorf("unsafe content: %#v", segments)
					}
					w.WriteHeader(tc.code)
					_ = json.NewEncoder(w).Encode(map[string]any{"status": tc.status, "retcode": 0, "data": map[string]any{"message_id": -789}})
				default:
					t.Errorf("unexpected action %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer s.Close()
			a := New(Options{URL: s.URL, Token: "fixture"}, nil)
			out := message.OutboundMessage{Account: "10001", RoomID: "30003", Peer: "20002", Text: "literal [CQ:at,qq=all]", OperationID: "stable"}
			if tc.reply != "" {
				out.ReplyTo = &message.ReplyReference{MessageID: tc.reply}
			}
			result, err := a.Send(t.Context(), out)
			if result.Status != tc.expected || (err == nil) != (tc.expected == message.Sent) {
				t.Fatalf("%#v %v", result, err)
			}
			if sends.Load() != 1 {
				t.Fatal("send duplicated")
			}
			if tc.expected == message.Sent && result.MessageID != "-789" {
				t.Fatal("lost platform message ID")
			}
		})
	}
}
