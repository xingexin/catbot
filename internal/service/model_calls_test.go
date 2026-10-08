package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain"
	"github.com/xingexin/catbot/internal/store"
)

const modelLedgerSecret = "secret-MODEL-LEDGER-sentinel"
const modelLedgerInput = "private-INPUT-sentinel"
const modelLedgerOutput = "private-OUTPUT-sentinel"

func seedModelLedgerPlugin(t *testing.T, a *App, endpoint, protocol string) string {
	t.Helper()
	p := domain.Plugin{ID: "media-fixture", Manifest: domain.Manifest{Version: "2.3.4"}, Grants: []string{"models"}}
	if err := a.Store.Put(t.Context(), "plugin-version", "media-snapshot", p); err != nil {
		t.Fatal(err)
	}
	if err := a.Vault.Set(t.Context(), "model-key", "fixture", modelLedgerSecret); err != nil {
		t.Fatal(err)
	}
	c := domain.Config{ID: "config", Name: "fixture", Kind: "api", Model: "fixture-model", Protocol: protocol, BaseURL: endpoint, CredentialID: "model-key", TimeoutSec: 3, MaxTokens: 128}
	if err := a.Store.Put(t.Context(), "config", c.ID, c); err != nil {
		t.Fatal(err)
	}
	return a.Token("plugin:media-snapshot")
}

func modelLedgerRequest(ctx context.Context, a *App, token, kind string, body map[string]any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/internal/plugin/"+kind, bytes.NewReader(b)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Secretary-Operation-ID", "run-123:step-4:tool-2")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func modelLedgerRecords(t *testing.T, a *App) []modelCall {
	t.Helper()
	calls, err := store.All[modelCall](t.Context(), a.Store, "model-call")
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func modelUsageUnknown(usage json.RawMessage) bool {
	return len(usage) == 0 || string(usage) == "null"
}

func assertModelLedgerMetadata(t *testing.T, call modelCall, kind, protocol, model, status string) {
	t.Helper()
	if call.ID == "" || call.PluginID != "media-fixture" || call.PluginVersion != "2.3.4" || call.ConfigID != "config" || call.Model != model || call.Protocol != protocol || call.Kind != kind || call.OperationID != "run-123:step-4:tool-2" || call.Status != status {
		t.Fatalf("missing model call metadata: %+v", call)
	}
	if call.StartedAt.IsZero() || call.FinishedAt == nil || call.FinishedAt.Before(call.StartedAt) || call.DurationMS == nil || *call.DurationMS < 0 {
		t.Fatalf("missing or invalid execution timing: %+v", call)
	}
	b, _ := json.Marshal(call)
	for _, forbidden := range []string{modelLedgerSecret, modelLedgerInput, modelLedgerOutput, `"baseUrl":`, `"credentialId":`, `"prompt":`, `"images":`} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("ledger contains sensitive field or content %q", forbidden)
		}
	}
}

func TestPluginGenerateRecordsActualUsageAndMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, response, wantUsage string
		wantStatus                          string
	}{
		{"chat", "openai-chat", `{"choices":[{"message":{"content":"private-OUTPUT-sentinel"}}],"usage":{"prompt_tokens":21,"completion_tokens":5}}`, `{"completion_tokens":5,"prompt_tokens":21}`, "completed"},
		{"responses", "openai-responses", `{"output":[{"type":"message","content":[{"type":"output_text","text":"private-OUTPUT-sentinel"}]}],"usage":{"input_tokens":7,"output_tokens":3}}`, `{"input_tokens":7,"output_tokens":3}`, "completed"},
		{"anthropic", "anthropic", `{"content":[{"type":"text","text":"private-OUTPUT-sentinel"}],"usage":{"input_tokens":8,"output_tokens":4}}`, `{"input_tokens":8,"output_tokens":4}`, "completed"},
		{"usage absent", "openai-chat", `{"choices":[{"message":{"content":"private-OUTPUT-sentinel"}}]}`, `null`, "completed"},
		{"known zero preserved", "openai-chat", `{"choices":[{"message":{"content":"private-OUTPUT-sentinel"}}],"usage":{"completion_tokens":0}}`, `{"completion_tokens":0}`, "completed"},
		{"provider rejected output with known usage", "openai-chat", `{"choices":[{"message":{"content":"private-OUTPUT-sentinel"},"finish_reason":"length"}],"usage":{"completion_tokens":128}}`, `{"completion_tokens":128}`, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			var sent atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sent.Add(1)
				calls, err := store.All[modelCall](r.Context(), a.Store, "model-call")
				if err != nil || len(calls) != 1 || calls[0].Status != "running" || calls[0].FinishedAt != nil || !modelUsageUnknown(calls[0].Usage) {
					t.Error("external request sent before durable running record", calls, err)
				}
				if r.Header.Get("Authorization") != "Bearer "+modelLedgerSecret && r.Header.Get("X-Api-Key") != modelLedgerSecret {
					t.Error("credential was not forwarded to model provider")
				}
				data, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(data), modelLedgerInput) {
					t.Error("fixture did not receive prompt")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer srv.Close()
			token := seedModelLedgerPlugin(t, a, srv.URL, tc.protocol)
			w := modelLedgerRequest(t.Context(), a, token, "generate", map[string]any{"configId": "config", "prompt": modelLedgerInput})
			if (w.Code == 200) != (tc.wantStatus == "completed") || sent.Load() != 1 {
				t.Fatal("response contract changed", w.Code, w.Body.String(), sent.Load())
			}
			if tc.wantStatus == "completed" && !strings.Contains(w.Body.String(), modelLedgerOutput) {
				t.Fatal("generated text was lost", w.Body.String())
			}
			calls := modelLedgerRecords(t, a)
			if len(calls) != 1 {
				t.Fatal("missing or duplicate ledger entry", calls)
			}
			assertModelLedgerMetadata(t, calls[0], "generate", tc.protocol, "fixture-model", tc.wantStatus)
			usage, _ := json.Marshal(calls[0].Usage)
			if string(usage) != tc.wantUsage {
				t.Fatal("unknown usage synthesized or known usage lost", string(usage), tc.wantUsage)
			}
		})
	}
}

func TestPluginTranscribeRecordsUsageAndSafeFailure(t *testing.T) {
	for _, tc := range []struct {
		name, response, usage, status string
		code                          int
	}{
		{"nested usage", `{"text":"private-OUTPUT-sentinel","usage":{"type":"tokens","input_tokens":32,"input_token_details":{"audio_tokens":22},"output_tokens":8}}`, `{"input_token_details":{"audio_tokens":22},"input_tokens":32,"output_tokens":8,"type":"tokens"}`, "completed", 200},
		{"absent usage", `{"text":"private-OUTPUT-sentinel"}`, `null`, "completed", 200},
		{"missing text", `{"usage":{"seconds":12}}`, `{"seconds":12}`, "failed", 200},
		{"http failure", `{"error":"secret-MODEL-LEDGER-sentinel private-INPUT-sentinel"}`, `null`, "failed", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer "+modelLedgerSecret {
					t.Error("incorrect transcription endpoint or credentials")
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
				} else {
					defer r.MultipartForm.RemoveAll()
					if r.FormValue("model") != "asr-model" {
						t.Error("transcription model override lost")
					}
				}
				calls, err := store.All[modelCall](r.Context(), a.Store, "model-call")
				if err != nil || len(calls) != 1 || calls[0].Status != "running" {
					t.Error("transcription was not recorded before submission", calls, err)
				}
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.response)
			}))
			defer srv.Close()
			token := seedModelLedgerPlugin(t, a, srv.URL, "openai-responses")
			artifact := domain.Artifact{ID: "audio", Name: "private-INPUT-sentinel.wav", Size: 16}
			if err := os.WriteFile(filepath.Join(a.Options.DataDir, "files", artifact.ID), []byte(modelLedgerInput), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.Store.Put(t.Context(), "artifact", artifact.ID, artifact); err != nil {
				t.Fatal(err)
			}
			w := modelLedgerRequest(t.Context(), a, token, "transcribe", map[string]any{"configId": "config", "artifactId": artifact.ID, "model": "asr-model"})
			if (w.Code == 200) != (tc.status == "completed") {
				t.Fatal("transcription failure lost", w.Code, w.Body.String())
			}
			calls := modelLedgerRecords(t, a)
			if len(calls) != 1 {
				t.Fatal(calls)
			}
			assertModelLedgerMetadata(t, calls[0], "transcribe", "openai-responses", "asr-model", tc.status)
			usage, _ := json.Marshal(calls[0].Usage)
			if string(usage) != tc.usage || (tc.code != 200 && !strings.Contains(calls[0].Error, "HTTP 403")) {
				t.Fatal("incorrect transcription usage or error", string(usage), calls[0].Error)
			}
		})
	}
}

type modelLedgerFaultStore struct {
	store.Store
	FailState string
}

func (s *modelLedgerFaultStore) Put(ctx context.Context, kind, id string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if kind == "model-call" && value.(modelCall).Status == s.FailState {
		return errors.New("ledger storage unavailable")
	}
	return s.Store.Put(ctx, kind, id, value)
}

func TestModelLedgerStorageFailureAndBootstrapRecovery(t *testing.T) {
	for _, failState := range []string{"running", "completed"} {
		t.Run(failState, func(t *testing.T) {
			a := testApp(t)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"done"}}]}`)
			}))
			defer srv.Close()
			token := seedModelLedgerPlugin(t, a, srv.URL, "openai-chat")
			s := &modelLedgerFaultStore{Store: a.Store, FailState: failState}
			a.Store = s
			w := modelLedgerRequest(t.Context(), a, token, "generate", map[string]any{"configId": "config", "prompt": modelLedgerInput})
			if w.Code == 200 {
				t.Fatal("failed ledger write reported successful call")
			}
			if failState == "running" {
				if calls.Load() != 0 || len(modelLedgerRecords(t, a)) != 0 {
					t.Fatal("sent provider request without recording it first")
				}
				return
			}
			records := modelLedgerRecords(t, a)
			if calls.Load() != 1 || len(records) != 1 || records[0].Status != "running" {
				t.Fatal("unconfirmed call was retried or lost", records, calls.Load())
			}
			s.FailState = ""
			if err := a.Bootstrap(t.Context()); err != nil {
				t.Fatal(err)
			}
			records = modelLedgerRecords(t, a)
			if records[0].Status != "interrupted" || !modelUsageUnknown(records[0].Usage) || records[0].DurationMS != nil || records[0].FinishedAt != nil || !strings.Contains(records[0].Error, "unknown") || calls.Load() != 1 {
				t.Fatal("restart guessed an outcome or repeated the request", records, calls.Load())
			}
			if err := a.Bootstrap(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := modelLedgerRecords(t, a); len(got) != 1 || got[0].Status != "interrupted" || calls.Load() != 1 {
				t.Fatal("second bootstrap changed terminal outcome", got)
			}
		})
	}
}

func TestModelLedgerRecordsCanceledCallAndProtectsAdminAPI(t *testing.T) {
	a := testApp(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
			t.Error("canceled model request remained connected")
		}
	}))
	defer srv.Close()
	token := seedModelLedgerPlugin(t, a, srv.URL, "openai-chat")
	a.Store = &modelLedgerFaultStore{Store: a.Store}
	body := map[string]any{"configId": "config", "prompt": modelLedgerInput}
	if w := modelLedgerRequest(t.Context(), a, "not-authenticated", "generate", body); w.Code != 401 || len(modelLedgerRecords(t, a)) != 0 {
		t.Fatal("operation header bypassed plugin authentication", w.Code)
	}
	w := modelLedgerRequest(ctx, a, token, "generate", body)
	calls := modelLedgerRecords(t, a)
	if w.Code == 200 || len(calls) != 1 || calls[0].Status != "failed" || !modelUsageUnknown(calls[0].Usage) || !strings.Contains(calls[0].Error, "canceled") {
		t.Fatal("canceled request did not persist a safe failure", w.Code, calls)
	}
	if w := request(t, a, "GET", "/api/model-calls", nil, nil); w.Code != 401 {
		t.Fatal("ledger exposed to anonymous user", w.Code)
	}
	login := request(t, a, "POST", "/api/login", map[string]any{"password": "test-password"}, nil)
	w = request(t, a, "GET", "/api/model-calls", nil, login.Result().Cookies()[0])
	if w.Code != 200 || !strings.Contains(w.Body.String(), calls[0].ID) || strings.Contains(w.Body.String(), modelLedgerSecret) || strings.Contains(w.Body.String(), modelLedgerInput) {
		t.Fatal("admin ledger did not return safe persisted data", w.Code, w.Body.String())
	}
}

func TestRecoverModelCallsPreservesTerminalUsage(t *testing.T) {
	a := testApp(t)
	finished := time.Now().UTC()
	duration := int64(120)
	for _, status := range []string{"completed", "failed", "interrupted"} {
		call := modelCall{ID: status, Status: status, StartedAt: finished.Add(-time.Second), FinishedAt: &finished, DurationMS: &duration, Usage: json.RawMessage(`{"input_tokens":3}`)}
		if err := a.Store.Put(t.Context(), "model-call", status, call); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Bootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, call := range modelLedgerRecords(t, a) {
		if call.Status != call.ID || call.FinishedAt == nil || !call.FinishedAt.Equal(finished) || call.DurationMS == nil || *call.DurationMS != duration || string(call.Usage) != `{"input_tokens":3}` {
			t.Fatal("bootstrap overwrote a terminal ledger entry", call)
		}
	}
	if got := safeModelCallError(fmt.Errorf("request failed: %s %s", modelLedgerSecret, modelLedgerInput)); strings.Contains(got, modelLedgerSecret) || strings.Contains(got, modelLedgerInput) {
		t.Fatal("provider details persisted", got)
	}
}
