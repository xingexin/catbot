package agent

import "context"

type toolResultLimitKey struct{}

// WithToolResultLimit carries this invocation's JSON observation byte budget.
// It is a presentation limit, never an authorization or scope grant.
func WithToolResultLimit(ctx context.Context, limit int) context.Context {
	return context.WithValue(ctx, toolResultLimitKey{}, limit)
}

// ToolResultLimit returns zero when the executor did not specify a byte budget.
func ToolResultLimit(ctx context.Context) int {
	limit, _ := ctx.Value(toolResultLimitKey{}).(int)
	return max(0, limit)
}
