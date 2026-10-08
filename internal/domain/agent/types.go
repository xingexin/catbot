package agent

type Config struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Kind          string       `json:"kind"`
	Provider      string       `json:"provider"`
	Protocol      string       `json:"protocol"`
	BaseURL       string       `json:"baseUrl"`
	Model         string       `json:"model"`
	CredentialID  string       `json:"credentialId,omitempty"`
	MaxSteps      int          `json:"maxSteps"`
	MaxTokens     int          `json:"maxTokens"`
	MaxInputBytes int          `json:"maxInputBytes,omitempty"`
	TimeoutSec    int          `json:"timeoutSec"`
	Capabilities  Capabilities `json:"capabilities"`
}

type Capabilities struct {
	Tools  bool `json:"tools"`
	Images bool `json:"images"`
	Stream bool `json:"stream"`
	Resume bool `json:"resume"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Tool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Permissions  []string       `json:"permissions"`
	TimeoutSec   int            `json:"timeoutSec"`
	RetrySafe    bool           `json:"retrySafe"`
	PluginID     string         `json:"pluginId"`
	Version      string         `json:"version"`
}
