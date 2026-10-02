package domain

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

type Config struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Kind         string       `json:"kind"`
	Provider     string       `json:"provider"`
	Protocol     string       `json:"protocol"`
	BaseURL      string       `json:"baseUrl"`
	Model        string       `json:"model"`
	CredentialID string       `json:"credentialId,omitempty"`
	MaxSteps     int          `json:"maxSteps"`
	MaxTokens    int          `json:"maxTokens"`
	TimeoutSec   int          `json:"timeoutSec"`
	Capabilities Capabilities `json:"capabilities"`
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

type Persona struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	SystemPrompt string    `json:"systemPrompt"`
	Examples     []Message `json:"examples"`
	Preferences  string    `json:"preferences"`
	Tools        []string  `json:"tools"`
	Version      int       `json:"version"`
	Default      bool      `json:"default"`
}

type Session struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	PersonaID       string            `json:"personaId"`
	ConfigID        string            `json:"configId"`
	Channel         string            `json:"channel"`
	ChannelProvider string            `json:"channelProvider,omitempty"`
	ChannelAccount  string            `json:"channelAccount,omitempty"`
	Recipient       string            `json:"recipient,omitempty"`
	Messages        []Message         `json:"messages"`
	Summary         string            `json:"summary"`
	Native          map[string]string `json:"native"`
	ActiveConfig    string            `json:"activeConfig,omitempty"`
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

type Step struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Tool      string         `json:"tool,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Prompt    string         `json:"prompt,omitempty"`
	DelaySec  int            `json:"delaySec,omitempty"`
}

type Manifest struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Entry        string         `json:"entry"`
	Description  string         `json:"description"`
	ConfigSchema map[string]any `json:"configSchema"`
	Tools        []Tool         `json:"tools"`
	Templates    []TaskTemplate `json:"templates,omitempty"`
}

type TaskTemplate struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Steps []Step `json:"steps"`
}

type Plugin struct {
	ID        string            `json:"id"`
	Manifest  Manifest          `json:"manifest"`
	Directory string            `json:"directory"`
	Enabled   bool              `json:"enabled"`
	Config    map[string]any    `json:"config"`
	Secrets   map[string]string `json:"secrets"`
	Grants    []string          `json:"grants"`
	Error     string            `json:"error,omitempty"`
}

type Run struct {
	ID         string            `json:"id"`
	SessionID  string            `json:"sessionId"`
	Prompt     string            `json:"prompt"`
	Status     string            `json:"status"`
	Config     Config            `json:"config"`
	Persona    Persona           `json:"persona"`
	Versions   map[string]string `json:"versions"`
	Result     string            `json:"result"`
	Error      string            `json:"error,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
	FinishedAt *time.Time        `json:"finishedAt,omitempty"`
	Usage      map[string]int    `json:"usage,omitempty"`
	NativeID   string            `json:"nativeId,omitempty"`
}

type Event struct {
	Sequence int64          `json:"sequence"`
	RunID    string         `json:"runId"`
	Type     string         `json:"type"`
	Data     map[string]any `json:"data"`
	Time     time.Time      `json:"time"`
}

type Task struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Cron       string            `json:"cron,omitempty"`
	TimeZone   string            `json:"timeZone"`
	RunAt      *time.Time        `json:"runAt,omitempty"`
	CatchupSec int               `json:"catchupSec"`
	Paused     bool              `json:"paused"`
	Status     string            `json:"status"`
	SessionID  string            `json:"sessionId"`
	ConfigID   string            `json:"configId"`
	PersonaID  string            `json:"personaId"`
	Steps      []Step            `json:"steps"`
	Versions   map[string]string `json:"versions"`
	Revision   int               `json:"revision"`
	Notify     bool              `json:"notify"`
	Error      string            `json:"error,omitempty"`
}

type TaskExecution struct {
	Config     *Config           `json:"config,omitempty"`
	Persona    *Persona          `json:"persona,omitempty"`
	Versions   map[string]string `json:"versions,omitempty"`
	ID         string            `json:"id"`
	TaskID     string            `json:"taskId"`
	Status     string            `json:"status"`
	Results    map[string]any    `json:"results"`
	Error      string            `json:"error,omitempty"`
	StartedAt  time.Time         `json:"startedAt"`
	FinishedAt *time.Time        `json:"finishedAt,omitempty"`
}

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
