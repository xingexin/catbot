package pluginsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout         = 180 * time.Second
	defaultJSONLimit int64 = 2 << 20
	defaultFileLimit int64 = 100 << 20
)

// HostOptions configures the authenticated catbot host connection. A supplied
// HTTPClient is copied; redirects are always disabled and a zero timeout is
// replaced by 180 seconds. No application-level retries are made.
type HostOptions struct {
	BaseURL          string
	Token            string
	OperationID      string
	HTTPClient       *http.Client
	MaxResponseBytes int64
	MaxFileBytes     int64
}

// Host exposes capability-checked storage, files, models, tasks and notifications.
// Construct one with NewHost or use ToolContext.Host. It is safe for concurrent
// calls. Authorization remains the host's responsibility.
type Host struct {
	base             string
	token            string
	operationID      string
	client           *http.Client
	maxResponseBytes int64
	maxFileBytes     int64
	redact           func(string) string
}

// NewHost constructs an HTTP client without making any network calls. An empty
// BaseURL is permitted for plugins that do not call host capabilities.
func NewHost(options HostOptions) (*Host, error) {
	base := strings.TrimRight(options.BaseURL, "/")
	if base != "" {
		parsed, err := url.Parse(base)
		if err != nil {
			return nil, errors.New("invalid host URL")
		}
		validScheme := parsed.Scheme == "http" || parsed.Scheme == "https"
		if !validScheme || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, errors.New("host URL requires http(s), a host, and no credentials, query or fragment")
		}
	}
	client := &http.Client{Timeout: defaultTimeout}
	if options.HTTPClient != nil {
		*client = *options.HTTPClient
	}
	if client.Timeout <= 0 {
		client.Timeout = defaultTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	jsonLimit := options.MaxResponseBytes
	if jsonLimit <= 0 {
		jsonLimit = defaultJSONLimit
	}
	fileLimit := options.MaxFileBytes
	if fileLimit <= 0 {
		fileLimit = defaultFileLimit
	}
	return &Host{
		base: base, token: options.Token, operationID: options.OperationID, client: client,
		maxResponseBytes: jsonLimit, maxFileBytes: fileLimit,
		redact: newRedactor(nil, nil, options.Token),
	}, nil
}

// Get retrieves a plugin-scoped stored value.
func (h *Host) Get(ctx context.Context, key string) (any, error) {
	var result struct {
		Value any `json:"value"`
	}
	err := h.json(ctx, http.MethodGet, "/kv/"+url.PathEscape(key), nil, &result)
	return result.Value, err
}

// Set stores a plugin-scoped JSON value.
func (h *Host) Set(ctx context.Context, key string, value any) error {
	return h.json(ctx, http.MethodPut, "/kv/"+url.PathEscape(key), value, nil)
}

// Save persists a named structured artifact and returns its host metadata.
func (h *Host) Save(ctx context.Context, name string, data any) (map[string]any, error) {
	return h.object(ctx, "/artifacts", map[string]any{"name": name, "data": data})
}

// GenerateRequest selects a configured model and optional image inputs.
type GenerateRequest struct {
	ConfigID string   `json:"configId"`
	Prompt   string   `json:"prompt"`
	Images   []string `json:"images,omitempty"`
}

// GenerateResult contains model output and optional provider usage data.
type GenerateResult struct {
	Text  string `json:"text"`
	Usage any    `json:"usage,omitempty"`
}

// Generate runs a permitted host model using this call's stable operation ID.
func (h *Host) Generate(ctx context.Context, request GenerateRequest) (GenerateResult, error) {
	var result GenerateResult
	err := h.json(ctx, http.MethodPost, "/generate", request, &result)
	return result, err
}

// TranscribeRequest identifies an uploaded audio artifact and model configuration.
type TranscribeRequest struct {
	ConfigID   string `json:"configId"`
	ArtifactID string `json:"artifactId"`
	Model      string `json:"model"`
}

// TranscribeResult contains the host transcription text.
type TranscribeResult struct {
	Text string `json:"text"`
}

// Transcribe transcribes an uploaded artifact using a permitted model.
func (h *Host) Transcribe(ctx context.Context, request TranscribeRequest) (TranscribeResult, error) {
	var result TranscribeResult
	err := h.json(ctx, http.MethodPost, "/transcribe", request, &result)
	return result, err
}

// UploadRequest contains a file to persist. Data is bounded by MaxFileBytes.
type UploadRequest struct {
	Name     string
	Data     []byte
	MIMEType string
}

// Upload stores a file as multipart form data and returns its artifact metadata.
func (h *Host) Upload(ctx context.Context, request UploadRequest) (map[string]any, error) {
	if int64(len(request.Data)) > h.maxFileBytes {
		return nil, errors.New("upload exceeds file size limit")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	headers := textproto.MIMEHeader{}
	headers.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": request.Name}))
	mimeType := request.MIMEType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if strings.ContainsAny(mimeType, "\r\n") {
		return nil, errors.New("invalid upload MIME type")
	}
	headers.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(headers)
	if err != nil {
		return nil, errors.New("create upload form")
	}
	if _, err := part.Write(request.Data); err != nil {
		return nil, errors.New("write upload form")
	}
	if err := writer.Close(); err != nil {
		return nil, errors.New("close upload form")
	}
	data, err := h.request(ctx, http.MethodPost, "/files", writer.FormDataContentType(), &body, h.maxResponseBytes)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.New("invalid host JSON response")
	}
	return result, nil
}

// Download retrieves a file with the configured file-size bound. Cancellation
// and the client's timeout cover reading the complete response body.
func (h *Host) Download(ctx context.Context, id string) ([]byte, error) {
	return h.request(ctx, http.MethodGet, "/files/"+url.PathEscape(id), "", nil, h.maxFileBytes)
}

// Task submits a task with an explicit stable operation ID. The input map is
// copied; retries by the plugin must reuse this ID. The SDK never retries it.
func (h *Host) Task(ctx context.Context, task map[string]any, operationID string) (map[string]any, error) {
	if operationID == "" {
		return nil, errors.New("task operation ID is required")
	}
	body := make(map[string]any, len(task)+1)
	for key, value := range task {
		body[key] = value
	}
	body["operationId"] = operationID
	return h.object(ctx, "/tasks", body)
}

// NotificationResult is the stored delivery status. A status such as unknown
// is not permission to retry with a new ID; consult the host's run record.
type NotificationResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Notify requests a notification using an explicit stable operation ID.
func (h *Host) Notify(
	ctx context.Context,
	sessionID string,
	text string,
	operationID string,
) (NotificationResult, error) {
	var result NotificationResult
	if operationID == "" {
		return result, errors.New("notification operation ID is required")
	}
	body := map[string]any{"sessionId": sessionID, "text": text, "operationId": operationID}
	err := h.json(ctx, http.MethodPost, "/notifications", body, &result)
	return result, err
}

func (h *Host) object(ctx context.Context, path string, body any) (map[string]any, error) {
	result := map[string]any{}
	err := h.json(ctx, http.MethodPost, path, body, &result)
	return result, err
}

func (h *Host) json(ctx context.Context, method string, path string, value any, result any) error {
	var body io.Reader
	if method != http.MethodGet {
		data, err := json.Marshal(value)
		if err != nil {
			return errors.New("host request must contain JSON values")
		}
		body = bytes.NewReader(data)
	}
	data, err := h.request(ctx, method, path, "application/json", body, h.maxResponseBytes)
	if err != nil {
		return err
	}
	if result != nil {
		if err := json.Unmarshal(data, result); err != nil {
			return errors.New("invalid host JSON response")
		}
	}
	return nil
}

func (h *Host) request(
	ctx context.Context,
	method string,
	path string,
	contentType string,
	body io.Reader,
	limit int64,
) ([]byte, error) {
	if h.base == "" {
		return nil, errors.New("SECRETARY_HOST_URL is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base+"/internal/plugin"+path, body)
	if err != nil {
		return nil, errors.New("invalid host request")
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	if h.operationID != "" {
		req.Header.Set("X-Secretary-Operation-ID", h.operationID)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := h.client.Do(req)
	if err != nil {
		// Preserve cancellation checks without exposing URLs or transport diagnostics.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("host request timed out: %w", context.DeadlineExceeded)
		}
		return nil, errors.New("host request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, min(h.maxResponseBytes, 64<<10)))
		message := fmt.Sprintf("Host HTTP %d", response.StatusCode)
		var result struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &result) == nil && result.Error != "" {
			message += ": " + h.redact(result.Error)
		}
		return nil, errors.New(message)
	}
	if response.ContentLength > limit {
		return nil, errors.New("host response exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("host response timed out: %w", context.DeadlineExceeded)
		}
		return nil, errors.New("read host response failed")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("host response exceeds size limit")
	}
	return data, nil
}
