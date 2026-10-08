package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Direct struct {
	Model ModelClient
	Tools ToolCaller
}

func (d *Direct) Run(ctx context.Context, r Request, emit Emit) (Result, error) {
	examples := []Entry{}
	for _, msg := range r.Persona.Examples {
		examples = append(examples, Entry{Role: msg.Role, Text: msg.Content})
	}
	history := append([]Message(nil), r.History...)
	current := []Entry{{Role: "user", Text: r.Prompt}}
	system := BuildSystemPrompt(r.Persona, time.Now())
	out := Result{Usage: map[string]int{}}
	tools := r.Tools
	if !r.Config.Capabilities.Tools {
		tools = nil
	}
	allowed := map[string]bool{}
	for _, t := range tools {
		allowed[t.Name] = true
	}
	for step := 0; step < r.Config.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		h, remaining, inputBytes, err := FitHistory(d.Model, r.Config, system, examples, history, current, tools)
		if err != nil {
			return out, err
		}
		if dropped := len(history) - len(remaining); dropped > 0 {
			if err := emit("context.compacted", map[string]any{"droppedMessages": dropped, "inputBytes": inputBytes, "maxInputBytes": inputBudget(r.Config), "reason": "oldest complete conversation turns omitted"}); err != nil {
				return out, err
			}
		}
		history = remaining
		t, err := d.Model.Step(ctx, r.Config, r.Key, system, h, tools, emit)
		for k, v := range t.Usage {
			out.Usage[k] += v
		}
		if err != nil {
			return out, err
		}
		current = append(current, Entry{Role: "assistant", Text: t.Text, Calls: t.Calls, Raw: t.Raw})
		if len(t.Calls) == 0 {
			out.Text = t.Text
			return out, nil
		}
		// Validate the entire batch before any tool can produce side effects.
		for _, call := range t.Calls {
			if !allowed[call.Name] {
				return out, fmt.Errorf("tool is not available: %s", call.Name)
			}
		}
		if d.Tools == nil {
			return out, errors.New("tool execution service is unavailable")
		}
		for i, call := range t.Calls {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			if err := emit("tool.started", map[string]any{"name": call.Name, "callId": call.ID}); err != nil {
				return out, err
			}
			var args map[string]any
			var value any
			err := json.Unmarshal([]byte(call.Arguments), &args)
			if err == nil && args == nil {
				err = errors.New("tool arguments must be an object")
			}
			if err == nil {
				value, err = d.Tools.Call(ctx, r.RunID, call.Name, args, fmt.Sprintf("%s:%d:%d", r.RunID, step, i))
			}
			if err != nil {
				value = map[string]any{"error": err.Error()}
			}
			// The observation is itself JSON inside a JSON model request. Reserve
			// room for escaping, declarations, and the rest of this tool batch.
			limit := min(64<<10, max(512, inputBudget(r.Config)/(4*len(t.Calls))))
			encoded, observation, merr := ToolObservation(value, limit)
			if merr != nil {
				return out, merr
			}
			if e := emit("tool.completed", map[string]any{"name": call.Name, "callId": call.ID, "result": observation, "isError": err != nil}); e != nil {
				return out, e
			}
			current = append(current, Entry{Role: "tool", Text: encoded, CallID: call.ID, IsError: err != nil})
		}
	}
	return out, errors.New("agent step limit reached")
}
