package plugin

import (
	"github.com/xingexin/catbot/internal/domain/agent"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
)

type Manifest struct {
	ID           string                    `json:"id"`
	Name         string                    `json:"name"`
	Version      string                    `json:"version"`
	Entry        string                    `json:"entry"`
	Runtime      string                    `json:"runtime,omitempty"`
	Description  string                    `json:"description"`
	ConfigSchema map[string]any            `json:"configSchema"`
	Tools        []agent.Tool              `json:"tools"`
	Templates    []taskentity.TaskTemplate `json:"templates,omitempty"`
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
