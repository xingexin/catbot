package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/xingexin/catbot/internal/domain"
)

const DefaultMaxInputBytes = 96 << 10

// ContextBudgetError reports the serialized API request size, not a token count.
// Tokenization and the provider's model context window are separate limits.
type ContextBudgetError struct {
	RequiredBytes int
	LimitBytes    int
	HasTools      bool
}

func (e *ContextBudgetError) Error() string {
	message := fmt.Sprintf("model input exceeds maxInputBytes (%d > %d); shorten the prompt, persona or tool schemas, or increase the input budget; the current request was not truncated", e.RequiredBytes, e.LimitBytes)
	if e.HasTools {
		message += "; completed tool calls were preserved, inspect their results before retrying"
	}
	return message
}

func inputBudget(c domain.Config) int {
	if c.MaxInputBytes > 0 {
		return c.MaxInputBytes
	}
	return DefaultMaxInputBytes
}

// fitHistory drops only complete old conversation turns. The current user
// request, persona examples, and every tool call/result in this execution stay
// together; forgetting executed actions could cause the model to repeat them.
func fitHistory(c domain.Config, system string, examples []Entry, history []domain.Message, current []Entry, tools []domain.Tool) ([]Entry, []domain.Message, int, error) {
	for {
		entries := make([]Entry, 0, len(examples)+len(history)+len(current))
		entries = append(entries, examples...)
		for _, msg := range history {
			entries = append(entries, Entry{Role: msg.Role, Text: msg.Content})
		}
		entries = append(entries, current...)
		body, err := json.Marshal(makeBody(c, system, entries, tools))
		if err != nil {
			return nil, history, 0, fmt.Errorf("encode model input: %w", err)
		}
		if len(body) <= inputBudget(c) {
			return entries, history, len(body), nil
		}
		if len(history) == 0 {
			return nil, nil, len(body), &ContextBudgetError{RequiredBytes: len(body), LimitBytes: inputBudget(c), HasTools: len(current) > 1}
		}
		history = history[nextTurn(history):]
	}
}

func nextTurn(messages []domain.Message) int {
	for i := 1; i < len(messages); i++ {
		if messages[i].Role == "user" {
			return i
		}
	}
	return len(messages)
}

// ToolObservation limits both model context and streamed event size. A truncated
// result remains valid JSON, identifies itself as a preview, and never slices a
// UTF-8 character in half. Callers retain their full result in their own store.
func ToolObservation(value any, limit int) (string, any, error) {
	if limit < 256 {
		return "", nil, errors.New("tool observation limit must be at least 256 bytes")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", nil, fmt.Errorf("encode tool result: %w", err)
	}
	if len(encoded) <= limit {
		return string(encoded), value, nil
	}
	preview := map[string]any{
		"truncated":     true,
		"originalBytes": len(encoded),
		"preview":       "",
		"note":          "Partial result only. Query a smaller range or read the saved artifact before claiming complete analysis.",
	}
	full := string(encoded)
	// JSON escaping changes the size, so bound the encoded envelope itself.
	lo, hi := 0, min(len(encoded), limit)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		preview["preview"] = utf8Prefix(full, mid)
		candidate, err := json.Marshal(preview)
		if err != nil {
			return "", nil, fmt.Errorf("encode tool preview: %w", err)
		}
		if len(candidate) <= limit {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	preview["preview"] = utf8Prefix(full, lo)
	encoded, err = json.Marshal(preview)
	if err != nil {
		return "", nil, fmt.Errorf("encode tool preview: %w", err)
	}
	return string(encoded), preview, nil
}

func utf8Prefix(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

func messageSize(m domain.Message) int { return len(m.Role) + len(m.Content) + 32 }

// Compact returns whole recent conversation turns plus bounded excerpts of
// older turns. The excerpts are deliberately labeled: they are neither durable
// semantic memory nor an LLM-generated summary. The combined byte budget is
// respected, leaving final protocol-level accounting to the executor.
func Compact(messages []domain.Message, limit int) ([]domain.Message, string) {
	if limit <= 0 || len(messages) == 0 {
		return nil, ""
	}
	starts := []int{}
	total := 0
	for i, msg := range messages {
		total += messageSize(msg)
		if msg.Role == "user" {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return nil, conversationExcerpts(messages, limit)
	}
	if starts[0] == 0 && total <= limit {
		return append([]domain.Message(nil), messages...), ""
	}
	keep := len(messages)
	size := 0
	for i := len(starts) - 1; i >= 0; i-- {
		end := len(messages)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		turnSize := 0
		for _, msg := range messages[starts[i]:end] {
			turnSize += messageSize(msg)
		}
		// If history must be omitted, reserve a small part for visible excerpts.
		budget := limit
		if starts[i] > 0 {
			budget -= min(3000, limit/8)
		}
		if size == 0 && turnSize <= limit {
			budget = limit
		}
		if size+turnSize > budget {
			break
		}
		size += turnSize
		keep = starts[i]
	}
	history := append([]domain.Message(nil), messages[keep:]...)
	if keep == 0 {
		return history, ""
	}
	return history, conversationExcerpts(messages[:keep], min(3000, limit-size))
}

func conversationExcerpts(messages []domain.Message, limit int) string {
	const label = "Earlier conversation excerpts (not complete history):\n"
	if limit <= len(label) {
		return ""
	}
	remaining := limit - len(label)
	lines := []string{}
	for i := len(messages) - 1; i >= 0 && remaining > 0; i-- {
		msg := messages[i]
		content := msg.Content
		if len(content) > 480 {
			content = utf8Prefix(content, 480) + "…"
		}
		line := msg.Role + ": " + content + "\n"
		if len(line) > remaining {
			break
		}
		lines = append(lines, line)
		remaining -= len(line)
	}
	if len(lines) == 0 {
		last := messages[len(messages)-1]
		line := last.Role + ": " + last.Content
		return label + utf8Prefix(line, remaining)
	}
	var out strings.Builder
	out.WriteString(label)
	for i := len(lines) - 1; i >= 0; i-- {
		out.WriteString(lines[i])
	}
	return out.String()
}
