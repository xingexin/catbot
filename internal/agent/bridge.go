package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Bridge struct {
	URL, Token, GatewayURL string
	RunToken               func(string) string
	HTTP                   *http.Client
}

func (b *Bridge) Run(ctx context.Context, r Request, emit Emit) (Result, error) {
	payload := map[string]any{
		"runId": r.Run.ID, "sessionId": r.Run.SessionID, "config": r.Run.Config,
		"prompt": r.Run.Prompt, "persona": r.Run.Persona, "history": r.History, "nativeId": r.NativeID,
		"apiKey": r.Key, "gatewayUrl": b.GatewayURL, "gatewayToken": b.RunToken("run:" + r.Run.ID),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(b.URL, "/")+"/runs", bytes.NewReader(data))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	req.Header.Set("Content-Type", "application/json")
	client := b.HTTP
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("SDK execution service unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Result{}, fmt.Errorf("SDK execution service returned HTTP %d", resp.StatusCode)
	}
	result := Result{}
	done := false
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
			done = true
			result.Text = str(e.Data["text"])
			result.Usage = usage(e.Data["usage"])
		}
		if err := emit(e.Type, e.Data); err != nil {
			return result, err
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if !done {
		return result, errors.New("SDK stream interrupted; execution outcome requires inspection")
	}
	return result, nil
}
