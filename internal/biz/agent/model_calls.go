package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentdomain "github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/agent/modelapi"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
)

// withModelCall persists intent before invoking a provider. operationID only
// correlates calls: it neither grants permission nor deduplicates requests.
func (a *Service) withModelCall(ctx context.Context, p plugin.Plugin, operationID string, c agentdomain.Config, kind, model string, perform func() (map[string]any, json.RawMessage, error)) (map[string]any, error) {
	if len(operationID) > 512 {
		return nil, errors.New("model operation ID exceeds 512 bytes")
	}
	call := agentdomain.ModelCall{
		ID: idgen.New(), PluginID: p.ID, PluginVersion: p.Manifest.Version,
		ConfigID: c.ID, Model: model, Protocol: c.Protocol, Kind: kind,
		OperationID: operationID, Status: "running", StartedAt: time.Now().UTC(),
	}
	if err := a.Store.Put(ctx, "model-call", call.ID, call); err != nil {
		return nil, errors.New("cannot record model call; request was not sent")
	}
	result, usage, callErr := perform()
	finished := time.Now().UTC()
	duration := finished.Sub(call.StartedAt).Milliseconds()
	call.FinishedAt, call.DurationMS, call.Usage = &finished, &duration, usage
	call.Status = "completed"
	if callErr != nil {
		call.Status, call.Error = "failed", safeModelCallError(callErr)
	}
	// Preserve the known outcome even after the caller has disconnected.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.Store.Put(saveCtx, "model-call", call.ID, call); err != nil {
		return nil, errors.New("model call ended but recording its result failed; outcome was not saved")
	}
	return result, callErr
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
	var httpErr *modelapi.HTTPError
	switch {
	case errors.Is(err, context.Canceled):
		return "model call canceled; unreturned usage is unknown"
	case errors.Is(err, context.DeadlineExceeded):
		return "model call timed out; unreturned usage is unknown"
	case errors.As(err, &httpErr):
		return fmt.Sprintf("model endpoint returned HTTP %d", httpErr.Status)
	default:
		return "model call failed; unreturned usage is unknown"
	}
}

func (a *Service) RecoverModelCalls(ctx context.Context) error {
	calls, err := store.All[agentdomain.ModelCall](ctx, a.Store, "model-call")
	if err != nil {
		return fmt.Errorf("load interrupted model calls: %w", err)
	}
	for _, call := range calls {
		if call.Status != "running" {
			continue
		}
		call.Status = "interrupted"
		call.Error = "host restarted before result was saved; completion and unreturned usage are unknown"
		// Neither the actual finish time nor elapsed request duration is known.
		if err := a.Store.Put(ctx, "model-call", call.ID, call); err != nil {
			return fmt.Errorf("mark interrupted model call: %w", err)
		}
	}
	return nil
}
