package entity

import (
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
	"time"
)

type Step struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Tool      string         `json:"tool,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Prompt    string         `json:"prompt,omitempty"`
	DelaySec  int            `json:"delaySec,omitempty"`
}

type TaskTemplate struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Steps      []Step `json:"steps"`
	NotifyWhen string `json:"notifyWhen,omitempty"`
	NotifyText string `json:"notifyText,omitempty"`
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
	NotifyWhen string            `json:"notifyWhen,omitempty"`
	NotifyText string            `json:"notifyText,omitempty"`
	Error      string            `json:"error,omitempty"`
}

type TaskExecution struct {
	Config     *agent.Config     `json:"config,omitempty"`
	Persona    *persona.Persona  `json:"persona,omitempty"`
	Versions   map[string]string `json:"versions,omitempty"`
	ID         string            `json:"id"`
	TaskID     string            `json:"taskId"`
	Status     string            `json:"status"`
	Results    map[string]any    `json:"results"`
	Error      string            `json:"error,omitempty"`
	StartedAt  time.Time         `json:"startedAt"`
	FinishedAt *time.Time        `json:"finishedAt,omitempty"`
}
