package pluginsdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	pluginsdk "github.com/xingexin/catbot/packages/plugin-sdk-go"
)

func TestHostCapabilityWireContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		method   string
		path     string
		body     any
		response string
		call     func(context.Context, *pluginsdk.Host) error
	}{
		{name: "get", method: "GET", path: "/kv/a%2Fb", response: `{"value":{"answer":42}}`, call: func(ctx context.Context, h *pluginsdk.Host) error {
			result, err := h.Get(ctx, "a/b")
			if err == nil && !reflect.DeepEqual(result, map[string]any{"answer": float64(42)}) {
				t.Errorf("get result=%v", result)
			}
			return err
		}},
		{name: "set", method: "PUT", path: "/kv/name", body: map[string]any{"answer": float64(42)}, response: `{"ok":true}`, call: func(ctx context.Context, h *pluginsdk.Host) error {
			return h.Set(ctx, "name", map[string]any{"answer": 42})
		}},
		{name: "save", method: "POST", path: "/artifacts", body: map[string]any{"name": "report", "data": map[string]any{"done": true}}, response: `{"id":"artifact-1"}`, call: func(ctx context.Context, h *pluginsdk.Host) error {
			result, err := h.Save(ctx, "report", map[string]any{"done": true})
			if err == nil && result["id"] != "artifact-1" {
				t.Errorf("save=%v", result)
			}
			return err
		}},
		{name: "generate", method: "POST", path: "/generate", body: map[string]any{"configId": "model-1", "prompt": "Summarize", "images": []any{"artifact-1"}}, response: `{"text":"summary","usage":{"tokens":7}}`, call: func(ctx context.Context, h *pluginsdk.Host) error {
			result, err := h.Generate(ctx, pluginsdk.GenerateRequest{ConfigID: "model-1", Prompt: "Summarize", Images: []string{"artifact-1"}})
			if err == nil && (result.Text != "summary" || result.Usage == nil) {
				t.Errorf("generate=%v", result)
			}
			return err
		}},
		{name: "transcribe", method: "POST", path: "/transcribe", body: map[string]any{"configId": "model-1", "artifactId": "audio-1", "model": "whisper"}, response: `{"text":"spoken words"}`, call: func(ctx context.Context, h *pluginsdk.Host) error {
			result, err := h.Transcribe(ctx, pluginsdk.TranscribeRequest{ConfigID: "model-1", ArtifactID: "audio-1", Model: "whisper"})
			if err == nil && result.Text != "spoken words" {
				t.Errorf("transcribe=%v", result)
			}
			return err
		}},
		{name: "task", method: "POST", path: "/tasks", body: map[string]any{"name": "reminder", "operationId": "run-1/task"}, response: `{"id":"task-1"}`, call: func(ctx context.Context, h *pluginsdk.Host) error {
			task := map[string]any{"name": "reminder"}
			result, err := h.Task(ctx, task, "run-1/task")
			if _, ok := task["operationId"]; ok {
				t.Error("Task mutated caller map")
			}
			if err == nil && result["id"] != "task-1" {
				t.Errorf("task=%v", result)
			}
			return err
		}},
		{name: "notify", method: "POST", path: "/notifications", body: map[string]any{"sessionId": "session-1", "text": "hello", "operationId": "run-1/notify"}, response: `{"id":"notification-1","status":"unknown","error":"connection lost"}`, call: func(ctx context.Context, h *pluginsdk.Host) error {
			result, err := h.Notify(ctx, "session-1", "hello", "run-1/notify")
			if err == nil && (result.ID != "notification-1" || result.Status != "unknown" || result.Error != "connection lost") {
				t.Errorf("notify=%v", result)
			}
			return err
		}},
		{name: "download", method: "GET", path: "/files/video%2F1", response: "video bytes", call: func(ctx context.Context, h *pluginsdk.Host) error {
			result, err := h.Download(ctx, "video/1")
			if err == nil && string(result) != "video bytes" {
				t.Errorf("download=%q", result)
			}
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				if r.Method != test.method || r.URL.EscapedPath() != "/internal/plugin"+test.path {
					t.Errorf("request=%s %s", r.Method, r.URL.EscapedPath())
				}
				if r.Header.Get("Authorization") != "Bearer secret-token" || r.Header.Get("X-Secretary-Operation-ID") != "run-1/tool-1" {
					t.Error("missing authentication or operation ID")
				}
				if test.body != nil {
					var body any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(body, test.body) {
						t.Errorf("body=%#v want %#v", body, test.body)
					}
				}
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			host, err := pluginsdk.NewHost(pluginsdk.HostOptions{BaseURL: server.URL, Token: "secret-token", OperationID: "run-1/tool-1"})
			if err != nil {
				t.Fatal(err)
			}
			if err := test.call(t.Context(), host); err != nil {
				t.Fatal(err)
			}
			if count.Load() != 1 {
				t.Fatalf("requests=%d", count.Load())
			}
		})
	}
}

func TestHostUpload(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/internal/plugin/files" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing auth")
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if string(data) != "hello" || header.Filename != "report.txt" || header.Header.Get("Content-Type") != "text/plain" {
			t.Errorf("upload=%q %+v", data, header)
		}
		_, _ = io.WriteString(w, `{"id":"upload-1"}`)
	}))
	defer server.Close()
	host, err := pluginsdk.NewHost(pluginsdk.HostOptions{BaseURL: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := host.Upload(t.Context(), pluginsdk.UploadRequest{Name: "report.txt", Data: []byte("hello"), MIMEType: "text/plain"})
	if err != nil || result["id"] != "upload-1" {
		t.Fatalf("upload result=%v err=%v", result, err)
	}
}

func TestHostDeniesRedirectAndRedactsErrors(t *testing.T) {
	t.Parallel()
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetRequests.Add(1) }))
	defer target.Close()
	var requests atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if strings.HasSuffix(r.URL.Path, "/redirect") {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "upstream said credential secret-token was rejected"})
	}))
	defer source.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { t.Error("unsafe caller redirect hook invoked"); return nil }}
	host, err := pluginsdk.NewHost(pluginsdk.HostOptions{BaseURL: source.URL, Token: "secret-token", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Get(t.Context(), "redirect")
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("redirect err=%v", err)
	}
	if targetRequests.Load() != 0 {
		t.Fatal("token request followed redirect")
	}
	_, err = host.Task(t.Context(), map[string]any{"name": "task"}, "stable-1")
	if err == nil || strings.Contains(err.Error(), "secret-token") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("host error=%v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("unexpected retry, requests=%d", requests.Load())
	}
	if client.Timeout != 0 {
		t.Fatal("constructor mutated supplied HTTP client")
	}
}

func TestHostSizeLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		chunked bool
	}{{name: "content length"}, {name: "chunked", chunked: true}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.chunked {
					w.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(w, strings.Repeat("x", 1025))
			}))
			defer server.Close()
			host, err := pluginsdk.NewHost(pluginsdk.HostOptions{BaseURL: server.URL, MaxResponseBytes: 1024, MaxFileBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := host.Get(t.Context(), "large"); err == nil || !strings.Contains(err.Error(), "size limit") {
				t.Fatalf("JSON limit=%v", err)
			}
			if _, err := host.Download(t.Context(), "large"); err == nil || !strings.Contains(err.Error(), "size limit") {
				t.Fatalf("file limit=%v", err)
			}
			if _, err := host.Upload(t.Context(), pluginsdk.UploadRequest{Name: "large", Data: make([]byte, 1025)}); err == nil {
				t.Fatal("oversized upload accepted")
			}
		})
	}
}

func TestHostCancellationAndDeadline(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			canceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-r.Context().Done()
				close(canceled)
			}))
			defer server.Close()
			client := &http.Client{}
			if mode == "timeout" {
				client.Timeout = 50 * time.Millisecond
			}
			host, err := pluginsdk.NewHost(pluginsdk.HostOptions{BaseURL: server.URL, HTTPClient: client})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := host.Get(ctx, "wait"); done <- err }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("request not started")
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				want := context.Canceled
				if mode == "timeout" {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatalf("error=%v want=%v", err, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("request did not return")
			}
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP context was not canceled")
			}
		})
	}
}

func TestHostRequiresStableSideEffectIDs(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	host, _ := pluginsdk.NewHost(pluginsdk.HostOptions{BaseURL: server.URL})
	if _, err := host.Task(t.Context(), map[string]any{}, ""); err == nil {
		t.Fatal("task without operation ID accepted")
	}
	if _, err := host.Notify(t.Context(), "session", "text", ""); err == nil {
		t.Fatal("notify without operation ID accepted")
	}
	if requests.Load() != 0 {
		t.Fatal("invalid side effect reached host")
	}
}

func TestToolHostReceivesInvocationOperationID(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Secretary-Operation-ID") != "stable-operation" {
			t.Errorf("operation ID=%q", r.Header.Get("X-Secretary-Operation-ID"))
		}
		_, _ = io.WriteString(w, `{"value":"ok"}`)
	}))
	defer server.Close()
	opts := options()
	opts.HostURL = server.URL
	session := connect(t, opts, func(ctx context.Context, _ map[string]any, tool pluginsdk.ToolContext) (any, error) {
		return tool.Host.Get(ctx, "key")
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "test_echo", Arguments: map[string]any{"text": "go"}, Meta: mcp.Meta{"secretary/operationId": "stable-operation"}})
	if err != nil || result.IsError {
		t.Fatalf("result=%v err=%v", result, err)
	}
}
