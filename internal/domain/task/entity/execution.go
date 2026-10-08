package entity

import (
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/persona"
)

type Input struct {
	TaskID   string
	Revision int
	Manual   bool
}

type Snapshot struct {
	Task    Task
	Config  agent.Config
	Persona persona.Persona
}

type StepInput struct {
	Snapshot    Snapshot
	Step        Step
	ExecutionID string
	Results     map[string]any
}

// ExecutionError stops automatic retries when task state or an ambiguous side
// effect makes retrying unsafe. Code remains stable across execution backends.
type ExecutionError struct {
	Code    string
	Message string
	Cause   error
}

func NewExecutionError(message, code string, cause error) *ExecutionError {
	return &ExecutionError{Code: code, Message: message, Cause: cause}
}

func (e *ExecutionError) Error() string { return e.Message }
func (e *ExecutionError) Unwrap() error { return e.Cause }
