package main

import (
	"context"
	"fmt"
	pluginsdk "github.com/xingexin/catbot/packages/plugin-sdk-go"
	"os"
	"time"
)

func main() {
	handlers := map[string]pluginsdk.Handler{
		"echo": func(_ context.Context, args map[string]any, _ pluginsdk.ToolContext) (any, error) { return args, nil },
		"exercise": func(ctx context.Context, args map[string]any, tool pluginsdk.ToolContext) (any, error) {
			if err := tool.Host.Set(ctx, "integration-note", args["text"]); err != nil {
				return nil, err
			}
			note, err := tool.Host.Get(ctx, "integration-note")
			if err != nil {
				return nil, err
			}
			result, err := tool.Host.Save(ctx, "Go plugin result", map[string]any{"text": note})
			if err != nil {
				return nil, err
			}
			task, err := tool.Host.Task(ctx, map[string]any{
				"name": "Go plugin task", "kind": "once", "runAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
				"configId": "config", "personaId": "secretary", "sessionId": "session", "notify": true,
				"steps": []any{map[string]any{"id": "echo", "kind": "tool", "tool": "go-fixture__echo", "arguments": map[string]any{"text": "later"}}},
			}, tool.OperationID+":task")
			if err != nil {
				return nil, err
			}
			notification, err := tool.Host.Notify(ctx, "session", "Go fixture completed", tool.OperationID+":notify")
			if err != nil {
				return nil, err
			}
			return map[string]any{"text": note, "operationId": tool.OperationID, "artifact": result, "task": task, "notification": notification}, nil
		},
	}
	if err := pluginsdk.Serve(context.Background(), handlers); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
