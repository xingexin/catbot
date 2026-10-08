package modelapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/xingexin/catbot/internal/domain/agent"
)

// Transcribe performs one bounded provider request. Intent and outcome recording
// belongs to the caller; this adapter never retries an uncertain upload.
func Transcribe(ctx context.Context, c agent.Config, key, name, model string, audio io.Reader) (map[string]any, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", name)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(part, io.LimitReader(audio, (25<<20)+1))
	if err != nil {
		return nil, err
	}
	if n > 25<<20 {
		return nil, errors.New("audio exceeds 25 MB")
	}
	if err = form.WriteField("model", model); err != nil {
		return nil, err
	}
	if err = form.WriteField("response_format", "json"); err != nil {
		return nil, err
	}
	if err = form.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.BaseURL, "/")+"/audio/transcriptions", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("transcription transport failed: %w", ctx.Err())
		}
		return nil, errors.New("transcription transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode}
	}
	var result map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil {
		return nil, errors.New("invalid transcription response")
	}
	if _, ok := result["text"].(string); !ok {
		return result, errors.New("transcription endpoint did not return text")
	}
	return result, nil
}
