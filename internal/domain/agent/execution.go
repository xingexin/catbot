package agent

import (
	"context"
	"github.com/xingexin/catbot/internal/domain/persona"
)

// Request is an execution contract; it does not own a persisted conversation run.
type Request struct {
	RunID     string
	SessionID string
	Prompt    string
	Config    Config
	Persona   persona.Persona
	History   []Message
	Tools     []Tool
	Key       string
	NativeID  string
}
type Result struct {
	Text     string
	Usage    map[string]int
	NativeID string
}
type Executor interface {
	Run(context.Context, Request, Emit) (Result, error)
}
type ToolCaller interface {
	Call(context.Context, string, string, map[string]any, string) (any, error)
}

// ModelClient keeps protocol encoding and network I/O outside the tool loop.
// InputSize measures the same serialized request that Step sends.
type ModelClient interface {
	Step(context.Context, Config, string, string, []Entry, []Tool, Emit) (Turn, error)
	InputSize(Config, string, []Entry, []Tool) (int, error)
}
type Call struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type Entry struct {
	Role, Text, CallID string
	IsError            bool
	Images             []string
	Calls              []Call
	Raw                []any
}
type Turn struct {
	Text  string
	Calls []Call
	Raw   []any
	Usage map[string]int
}
type Emit func(string, map[string]any) error
