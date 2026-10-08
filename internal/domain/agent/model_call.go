package agent

import (
	"encoding/json"
	"time"
)

// ModelCall records one plugin host invocation, not a billing estimate.
// Usage omitted by the provider remains unknown, including usage from retries.
type ModelCall struct {
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
