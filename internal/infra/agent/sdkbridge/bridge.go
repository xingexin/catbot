package sdkbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/domain/agent"
	"io"
	"net/http"
	"strings"
)

type Bridge struct {
	URL, Token, GatewayURL string
	RunToken               func(string) string
	HTTP                   *http.Client
}

func (b *Bridge) Run(ctx context.Context, r agent.Request, emit agent.Emit) (agent.Result, error) {
	payload := map[string]any{
		"runId": r.RunID, "sessionId": r.SessionID, "config": r.Config,
		"prompt": r.Prompt, "persona": r.Persona, "history": r.History, "nativeId": r.NativeID,
		"apiKey": r.Key, "gatewayUrl": b.GatewayURL, "gatewayToken": b.RunToken("run:" + r.RunID),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return agent.Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(b.URL, "/")+"/runs", bytes.NewReader(data))
	if err != nil {
		return agent.Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	req.Header.Set("Content-Type", "application/json")
	client := b.HTTP
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return agent.Result{}, fmt.Errorf("SDK execution service unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return agent.Result{}, fmt.Errorf("SDK execution service returned HTTP %d", resp.StatusCode)
	}
	result := agent.Result{}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 16<<20))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var e struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &e); err != nil {
			return result, err
		}
		if e.Type == "error" {
			return result, fmt.Errorf("SDK execution failed: %s", str(e.Data["message"]))
		}
		if e.Type == "native.session" {
			result.NativeID = str(e.Data["id"])
		}
		if e.Type == "completed" {
			result.Text = str(e.Data["text"])
			result.Usage = usage(e.Data["usage"])
		}
		if err := emit(e.Type, e.Data); err != nil {
			return result, err
		}
		if e.Type == "completed" {
			return result, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	return result, errors.New("SDK stream interrupted; execution outcome requires inspection")
}

func str(v any) string { s, _ := v.(string); return s }
func usage(v any) map[string]int {
	out := map[string]int{}
	obj, _ := v.(map[string]any)
	for k, v := range obj {
		if n, ok := v.(float64); ok {
			out[k] = int(n)
		}
	}
	return out
}
