package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xingexin/catbot/internal/domain"
)

func savedConfig(t *testing.T, a *App, cookie *http.Cookie, id string) domain.Config {
	t.Helper()
	response := request(t, a, http.MethodGet, "/api/configs", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var configs []domain.Config
	if err := json.Unmarshal(response.Body.Bytes(), &configs); err != nil {
		t.Fatal(err)
	}
	for _, config := range configs {
		if config.ID == id {
			return config
		}
	}
	t.Fatalf("saved config %q was not returned by the management API", id)
	return domain.Config{}
}

func TestSDKConfigSavePreservesDisabledToolsAndHostEnforcesIt(t *testing.T) {
	for _, provider := range []string{"codebuddy", "claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			login := request(t, a, http.MethodPost, "/api/login", map[string]string{"password": "test-password"}, nil)
			cookie := login.Result().Cookies()[0]
			config := domain.Config{ID: "sdk-config", Name: "SDK capability regression", Kind: "sdk", Provider: provider, Model: "fixture", Capabilities: domain.Capabilities{Tools: true}}
			for _, enabled := range []bool{true, false} {
				config.Capabilities.Tools = enabled
				response := request(t, a, http.MethodPost, "/api/configs", config, cookie)
				if response.Code != http.StatusOK {
					t.Fatal(response.Body.String())
				}
				var returned domain.Config
				if err := json.Unmarshal(response.Body.Bytes(), &returned); err != nil {
					t.Fatal(err)
				}
				config = savedConfig(t, a, cookie, config.ID)
				if returned.Capabilities.Tools != enabled || config.Capabilities.Tools != enabled {
					t.Fatalf("save changed tools=%t into response=%t stored=%t", enabled, returned.Capabilities.Tools, config.Capabilities.Tools)
				}
				if !config.Capabilities.Stream || !config.Capabilities.Resume {
					t.Fatal("SDK stream and resume capability declarations were lost")
				}
				run := domain.Run{ID: "sdk-capability-run", SessionID: "session", Status: "running", Config: config, Persona: domain.Persona{Tools: []string{"system__task_list"}}}
				if err := a.Store.Put(t.Context(), "run", run.ID, run); err != nil {
					t.Fatal(err)
				}
				tools, err := a.Tools(t.Context(), run)
				if err != nil {
					t.Fatal(err)
				}
				_, callErr := a.Call(t.Context(), run.ID, "system__task_list", map[string]any{}, "sdk-capability-check")
				if enabled {
					if len(tools) != 1 || callErr != nil {
						t.Fatalf("enabled tools unavailable: tools=%v error=%v", tools, callErr)
					}
				} else if len(tools) != 0 || callErr == nil || !strings.Contains(callErr.Error(), "not authorized") {
					t.Fatalf("saved tool opt-out bypassed by host: tools=%v error=%v", tools, callErr)
				}
			}
		})
	}
}

func TestSDKConfigRejectsImageCapability(t *testing.T) {
	for _, provider := range []string{"codebuddy", "claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			login := request(t, a, http.MethodPost, "/api/login", map[string]string{"password": "test-password"}, nil)
			cookie := login.Result().Cookies()[0]
			config := domain.Config{ID: "sdk-images", Name: "Unsupported image input", Kind: "sdk", Provider: provider, Model: "fixture", Capabilities: domain.Capabilities{Images: true}}
			response := request(t, a, http.MethodPost, "/api/configs", config, cookie)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "image input") {
				t.Fatalf("unsupported SDK images accepted: %d %s", response.Code, response.Body.String())
			}
			var stored domain.Config
			if err := a.Store.Get(t.Context(), "config", config.ID, &stored); err == nil {
				t.Fatal("invalid SDK image config persisted")
			}
		})
	}
}

func TestAPIConfigCapabilityCheckboxesRemainUserControlled(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			login := request(t, a, http.MethodPost, "/api/login", map[string]string{"password": "test-password"}, nil)
			cookie := login.Result().Cookies()[0]
			for _, enabled := range []bool{true, false} {
				capabilities := domain.Capabilities{Tools: enabled, Stream: enabled, Images: enabled}
				config := domain.Config{ID: "api-capabilities", Name: "API capability regression", Kind: "api", Protocol: protocol, Model: "fixture", BaseURL: "http://127.0.0.1:1", Capabilities: capabilities}
				response := request(t, a, http.MethodPost, "/api/configs", config, cookie)
				if response.Code != http.StatusOK {
					t.Fatal(response.Body.String())
				}
				if got := savedConfig(t, a, cookie, config.ID); got.Capabilities != capabilities {
					t.Fatalf("API capability selection changed: got %+v want %+v", got.Capabilities, capabilities)
				}
			}
		})
	}
}
