package sdkbridge

import (
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/domain/agent"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discard(string, map[string]any) error { return nil }

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read after terminal event") }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestSDKBridgeReturnsOnCompletedEvent(t *testing.T) {
	t.Parallel()
	bridge := Bridge{URL: "https://runtime.example", RunToken: func(string) string { return "fixture" }, HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		stream := "data: {\"type\":\"native.session\",\"data\":{\"id\":\"native-session\"}}\n\ndata: {\"type\":\"completed\",\"data\":{\"text\":\"done\",\"usage\":{\"input_tokens\":5}}}\n\n"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(io.MultiReader(strings.NewReader(stream), errorReader{}))}, nil
	})}}
	result, err := bridge.Run(t.Context(), agent.Request{}, discard)
	if err != nil || result.Text != "done" || result.NativeID != "native-session" || result.Usage["input_tokens"] != 5 {
		t.Fatalf("bridge waited beyond completion: result=%#v err=%v", result, err)
	}
}

func TestSDKBridgeDoesNotForwardSecretsOnRedirect(t *testing.T) {
	t.Parallel()
	forwarded := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = true
		fmt.Fprint(w, "unexpected")
	}))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	bridge := Bridge{URL: server.URL, Token: "fixture-secret", RunToken: func(string) string { return "fixture" }}
	_, err := bridge.Run(t.Context(), agent.Request{}, discard)
	if err == nil || forwarded {
		t.Fatalf("runtime redirect followed: forwarded=%v err=%v", forwarded, err)
	}
}
