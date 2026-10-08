package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/xingexin/catbot/internal/agent"
	"github.com/xingexin/catbot/internal/domain"
	"github.com/xingexin/catbot/internal/store"
)

// modelCall records one host invocation, not a billing estimate. Retries within
// a provider request may consume additional usage that the provider never returns.
type modelCall struct {
	ID            string          `json:"id"`
	PluginID      string          `json:"pluginId"`
	PluginVersion string          `json:"pluginVersion"`
	ConfigID      string          `json:"configId"`
	Model         string          `json:"model"`
	Protocol      string          `json:"protocol"`
	Kind          string          `json:"kind"`
	OperationID   string          `json:"operationId,omitempty"`
	Status        string          `json:"status"`
	StartedAt     time.Time       `json:"startedAt"`
	FinishedAt    *time.Time      `json:"finishedAt,omitempty"`
	DurationMS    *int64          `json:"durationMs,omitempty"`
	Usage         json.RawMessage `json:"usage"`
	Error         string          `json:"error,omitempty"`
}

// withPluginModelCall saves the attempt before sending a request. OperationID
// only correlates calls; it neither grants permission nor deduplicates requests.
func (a *App) withPluginModelCall(w http.ResponseWriter, r *http.Request, c domain.Config, kind, model string, perform func() (map[string]any, json.RawMessage, error)) {
	p := r.Context().Value(pluginContextKey{}).(domain.Plugin)
	operationID := r.Header.Get("X-Secretary-Operation-ID")
	if len(operationID) > 512 {
		fail(w, errors.New("model operation ID exceeds 512 bytes"))
		return
	}
	call := modelCall{
		ID: domain.ID(), PluginID: p.ID, PluginVersion: p.Manifest.Version,
		ConfigID: c.ID, Model: model, Protocol: c.Protocol, Kind: kind,
		OperationID: operationID, Status: "running", StartedAt: time.Now().UTC(),
	}
	if err := a.Store.Put(r.Context(), "model-call", call.ID, call); err != nil {
		fail(w, errors.New("cannot record model call; request was not sent"))
		return
	}
	result, usage, callErr := perform()
	finished := time.Now().UTC()
	duration := finished.Sub(call.StartedAt).Milliseconds()
	call.FinishedAt, call.DurationMS, call.Usage = &finished, &duration, usage
	call.Status = "completed"
	if callErr != nil {
		call.Status, call.Error = "failed", safeModelCallError(callErr)
	}
	// The request may be canceled while its provider is still responding. Keep
	// its known terminal state without persisting credentials or request content.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := a.Store.Put(ctx, "model-call", call.ID, call); err != nil {
		fail(w, errors.New("model call ended but recording its result failed; outcome was not saved"))
		return
	}
	if callErr != nil {
		fail(w, callErr)
		return
	}
	JSON(w, http.StatusOK, result)
}

func knownModelUsage(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil || string(raw) == "null" || string(raw) == "{}" {
		return nil
	}
	return raw
}

func safeModelCallError(err error) string {
	var httpErr *agent.HTTPError
	switch {
	case errors.Is(err, context.Canceled):
		return "model call canceled; unreturned usage is unknown"
	case errors.Is(err, context.DeadlineExceeded):
		return "model call timed out; unreturned usage is unknown"
	case errors.As(err, &httpErr):
		return fmt.Sprintf("model endpoint returned HTTP %d", httpErr.Status)
	default:
		// Provider/transport errors can embed URLs, credentials, or content.
		return "model call failed; unreturned usage is unknown"
	}
}

func (a *App) recoverModelCalls(ctx context.Context) error {
	calls, err := store.All[modelCall](ctx, a.Store, "model-call")
	if err != nil {
		return fmt.Errorf("load interrupted model calls: %w", err)
	}
	for _, call := range calls {
		if call.Status != "running" {
			continue
		}
		call.Status = "interrupted"
		call.Error = "host restarted before result was saved; completion and unreturned usage are unknown"
		// Neither an actual finish time nor the elapsed request duration is known.
		if err := a.Store.Put(ctx, "model-call", call.ID, call); err != nil {
			return fmt.Errorf("mark interrupted model call: %w", err)
		}
	}
	return nil
}
