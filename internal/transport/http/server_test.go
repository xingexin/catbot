package httptransport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentbiz "github.com/xingexin/catbot/internal/biz/agent"
	artifactbiz "github.com/xingexin/catbot/internal/biz/artifact"
	conversationbiz "github.com/xingexin/catbot/internal/biz/conversation"
	lifecyclebiz "github.com/xingexin/catbot/internal/biz/lifecycle"
	mailbiz "github.com/xingexin/catbot/internal/biz/mail"
	messagingbiz "github.com/xingexin/catbot/internal/biz/messaging"
	personabiz "github.com/xingexin/catbot/internal/biz/persona"
	pluginbiz "github.com/xingexin/catbot/internal/biz/plugin"
	systembiz "github.com/xingexin/catbot/internal/biz/system"
	taskbiz "github.com/xingexin/catbot/internal/biz/task"
	"github.com/xingexin/catbot/internal/config"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/infra/filestore"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
)

type fixture struct {
	handler http.Handler
	store   store.Store
	cookie  *http.Cookie
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	state := store.NewMemory()
	secrets, err := vault.New(state, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	options := config.Options{AdminPassword: "fixture-password", MaxUploadMB: 1}
	plugins := pluginbiz.NewWithRuntime(state, secrets, nil, nil)
	tasks := &taskbiz.Commands{Store: state}
	messaging := messagingbiz.New(state, options, nil)
	files := filestore.Store{Root: t.TempDir()}
	if err := files.Prepare(); err != nil {
		t.Fatal(err)
	}
	services := Services{
		Lifecycle:     &lifecyclebiz.Service{Store: state, Files: files},
		System:        &systembiz.Service{Store: state, Vault: secrets, Options: options},
		Agent:         &agentbiz.Service{Store: state, Vault: secrets},
		Personas:      &personabiz.Service{Store: state},
		Conversations: &conversationbiz.Service{Store: state, Plugins: plugins},
		Tasks:         tasks, Plugins: plugins, Messaging: messaging,
		Artifacts: &artifactbiz.Service{Store: state, Files: files, MaxUploadMB: 1},
		Mail:      &mailbiz.Service{Store: state, Tasks: tasks, Plugins: plugins, AuthorizeNotification: messaging.AuthorizeNotification},
	}
	server := New(services, Options{CookieSecure: true, MaxUploadMB: 1}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fixture-Internal", "passed")
		w.WriteHeader(418)
	}))
	fixture := fixture{handler: server.Handler(), store: state}
	response := fixture.request("POST", "/api/login", `{"password":"fixture-password"}`)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("login did not set session cookie")
	}
	fixture.cookie = cookies[0]
	return fixture
}
func (f fixture) request(method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:43210"
	if f.cookie != nil {
		request.AddCookie(f.cookie)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func TestHTTPAuthCookiesAndOriginContract(t *testing.T) {
	f := newFixture(t)
	if f.cookie.Name != "secretary_session" || !f.cookie.HttpOnly || !f.cookie.Secure || f.cookie.SameSite != http.SameSiteStrictMode || f.cookie.MaxAge != 86400 {
		t.Fatalf("cookie contract changed: %+v", f.cookie)
	}
	noCookie := f
	noCookie.cookie = nil
	if response := noCookie.request("GET", "/api/me", ""); response.Code != 401 || !strings.Contains(response.Body.String(), "login required") {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := f.request("GET", "/api/me", ""); response.Code != 200 || !strings.Contains(response.Body.String(), `"username":"admin"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	req := httptest.NewRequest("POST", "http://localhost/api/personas", strings.NewReader(`{}`))
	req.AddCookie(f.cookie)
	req.Header.Set("Origin", "https://other.invalid")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatal("cross-origin mutation accepted", response.Code)
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("security headers lost")
	}
	response = f.request("POST", "/api/logout", "")
	if response.Code != 200 || response.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout cookie changed")
	}
	if response = f.request("GET", "/api/me", ""); response.Code != 401 || !strings.Contains(response.Body.String(), "session expired") {
		t.Fatal("logout failed to revoke persisted session")
	}
}

func TestHTTPListsCredentialsAndSingleJSONValue(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"configs", "personas", "sessions", "runs", "tasks", "executions", "plugins", "artifacts", "notifications", "deliveries", "model-calls", "secrets", "tools"} {
		response := f.request("GET", "/api/"+path, "")
		if response.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, response.Code, response.Body.String())
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
			t.Fatalf("GET %s not a JSON array: %v", path, err)
		}
	}
	response := f.request("POST", "/api/secrets", `{"name":"fixture","value":"hidden-credential"}`)
	if response.Code != 200 || strings.Contains(response.Body.String(), "hidden-credential") {
		t.Fatal("credential write response leaked value")
	}
	response = f.request("GET", "/api/secrets", "")
	if response.Code != 200 || strings.Contains(response.Body.String(), "hidden-credential") || strings.Contains(response.Body.String(), "ciphertext") {
		t.Fatal("credential list leaked value")
	}
	response = f.request("POST", "/api/personas", `{} {}`)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "one JSON value") {
		t.Fatal(response.Code, response.Body.String())
	}
	response = f.request("POST", "/api/personas", `{"name":"fixture","systemPrompt":"fixture"}`)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = f.request("GET", "/api/runs/missing", "")
	if response.Code != 404 || response.Header().Get("Content-Type") != "application/json" {
		t.Fatal("not-found contract changed")
	}
}

func TestHTTPSSESupportsResumeAndTerminalCompletion(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Put(t.Context(), "run", "fixture", conversation.Run{ID: "fixture", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"text.delta", "completed"} {
		if err := f.store.Append(t.Context(), store.EventRecord{RunID: "fixture", Type: kind, Data: map[string]any{"text": kind}, Time: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("GET", "/api/runs/fixture/events?after=1", nil)
	req.AddCookie(f.cookie)
	req.Header.Set("Last-Event-ID", "0")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, req)
	if response.Code != 200 || response.Header().Get("Content-Type") != "text/event-stream" || response.Header().Get("Cache-Control") != "no-cache" || response.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatal("SSE headers changed")
	}
	if strings.Contains(response.Body.String(), "text.delta") || !strings.Contains(response.Body.String(), "id: 2\ndata: ") || !strings.Contains(response.Body.String(), `"type":"completed"`) {
		t.Fatal("SSE resume changed", response.Body.String())
	}
}

func TestHTTPUploadDownloadAndInternalDelegation(t *testing.T) {
	f := newFixture(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "fixture.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(file, "fixture file contents")
	_ = writer.Close()
	req := httptest.NewRequest("POST", "/api/files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.AddCookie(f.cookie)
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, req)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	var artifact struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &artifact); err != nil {
		t.Fatal(err)
	}
	response = f.request("GET", "/api/files/"+artifact.ID, "")
	if response.Code != 200 || response.Body.String() != "fixture file contents" || response.Header().Get("Content-Type") != "application/octet-stream" || !strings.Contains(response.Header().Get("Content-Disposition"), "fixture.txt") {
		t.Fatal("download contract changed", response.Code, response.Body.String())
	}
	response = f.request("GET", "/internal/example", "")
	if response.Code != 418 || response.Header().Get("X-Fixture-Internal") != "passed" {
		t.Fatal("internal handler was not delegated")
	}
	response = f.request("POST", "/qq/onebot/events", `{}`)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "receiver is not configured") {
		t.Fatal("unconfigured webhook contract changed")
	}
}

func TestHTTPLoginRateLimitPrecedesDecodeAndCountsPasswordFailures(t *testing.T) {
	f := newFixture(t)
	for range 3 {
		if response := f.request("POST", "/api/login", `{`); response.Code != 400 {
			t.Fatalf("malformed input: %d %s", response.Code, response.Body.String())
		}
	}
	for attempt := 1; attempt <= 20; attempt++ {
		if response := f.request("POST", "/api/login", `{"password":"wrong"}`); response.Code != 401 {
			t.Fatalf("password attempt %d: %d %s", attempt, response.Code, response.Body.String())
		}
	}
	for _, body := range []string{`{`, `{"password":"wrong"}`, `{"password":"fixture-password"}`} {
		response := f.request("POST", "/api/login", body)
		if response.Code != 429 || len(response.Result().Cookies()) != 0 {
			t.Fatalf("limited request: %d %s", response.Code, response.Body.String())
		}
	}
	var rate systembiz.LoginRate
	if err := f.store.Get(t.Context(), "login-rate", "127.0.0.1", &rate); err != nil || rate.Attempts != 20 {
		t.Fatalf("attempts = %d, err = %v", rate.Attempts, err)
	}
	rate.Since = time.Now().Add(-6 * time.Minute)
	if err := f.store.Put(t.Context(), "login-rate", "127.0.0.1", rate); err != nil {
		t.Fatal(err)
	}
	if response := f.request("POST", "/api/login", `{"password":"fixture-password"}`); response.Code != 200 {
		t.Fatalf("expired limit: %d %s", response.Code, response.Body.String())
	}
}
