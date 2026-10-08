package bootstrap

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/xingexin/catbot/internal/config"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/infra/store"
)

func TestLocalLoginOnlyForManagedNapCat(t *testing.T) {
	for _, tc := range []struct {
		enabled, endpoint string
		want              bool
	}{
		{"true", "http://napcat:3000", true},
		{"TRUE", "http://napcat:3000/", true},
		{"false", "http://napcat:3000", false},
		{"true", "http://external:3000", false},
		{"true", "http://napcat:4000", false},
		{"true", "http://napcat:3000/another-service", false},
		{"true", "", false},
	} {
		if got := usesLocalNapCat(tc.enabled, tc.endpoint); got != tc.want {
			t.Errorf("%s %s: %v", tc.enabled, tc.endpoint, got)
		}
	}
}

func TestStartupUsesConfiguredOneBotEndpoint(t *testing.T) {
	var counts [2]atomic.Int32
	var endpoints [2]string
	for i := range endpoints {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer fixture" {
				t.Error("token not configured")
			}
			var data any
			switch r.URL.Path {
			case "/get_login_info":
				data = map[string]any{"user_id": 10001}
			case "/get_status":
				data = map[string]bool{"online": true, "good": true}
			case "/send_private_msg":
				counts[i].Add(1)
				data = map[string]int{"message_id": 42}
			default:
				t.Error("unexpected action", r.URL.Path)
			}
			JSON(w, 200, map[string]any{"status": "ok", "retcode": 0, "data": data})
		}))
		t.Cleanup(s.Close)
		endpoints[i] = s.URL
	}
	data := store.NewMemory()
	if err := data.Put(t.Context(), "qq-binding", "onebot", map[string]any{"enabled": true, "selfId": "10001", "allowedUserIds": []string{"20002"}}); err != nil {
		t.Fatal(err)
	}
	if err := data.Put(t.Context(), "session", "existing", conversation.Session{ID: "existing", Channel: "qq", ChannelProvider: "onebot", ChannelAccount: "10001", Recipient: "20002"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONEBOT_TOKEN", "fixture")
	t.Setenv("NAPCAT_ENABLED", "false")
	for i, endpoint := range endpoints {
		t.Setenv("ONEBOT_URL", endpoint)
		a, err := New(data, config.Options{DataDir: t.TempDir(), PluginDir: t.TempDir(), MasterKey: base64.StdEncoding.EncodeToString(make([]byte, 32))})
		if err != nil {
			t.Fatal(err)
		}
		if err := registerChannels(a, config.Environment{OneBotToken: "fixture", NapCatEnabled: "false", OneBotURL: endpoint}); err != nil {
			t.Fatal(err)
		}
		if err := a.Messaging.SendMessage(t.Context(), "existing", "hello", endpoint); err != nil {
			t.Fatal(err)
		}
		a.Close()
		if counts[i].Load() != 1 {
			t.Fatal("configured server not called", i)
		}
		if i == 1 && counts[0].Load() != 1 {
			t.Fatal("replaced endpoint still used")
		}
	}
}
