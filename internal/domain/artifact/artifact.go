package artifact

import (
	"encoding/json"
	"time"
)

type Artifact struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Mime      string          `json:"mime"`
	Size      int64           `json:"size"`
	Path      string          `json:"-"`
	PluginID  string          `json:"pluginId,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}
