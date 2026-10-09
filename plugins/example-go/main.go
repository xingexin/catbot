package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"unicode/utf8"

	pluginsdk "github.com/xingexin/catbot/packages/plugin-sdk-go"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := pluginsdk.Serve(ctx, map[string]pluginsdk.Handler{
		"echo": echo,
		"note": note,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func echo(_ context.Context, args map[string]any, _ pluginsdk.ToolContext) (any, error) {
	text, ok := args["text"].(string)
	if !ok {
		return nil, fmt.Errorf("text must be a string")
	}
	return map[string]any{"text": text, "characters": utf8.RuneCountInString(text)}, nil
}

func note(ctx context.Context, args map[string]any, tool pluginsdk.ToolContext) (any, error) {
	name, ok := args["name"].(string)
	if !ok {
		return nil, fmt.Errorf("name must be a string")
	}
	key := "note-" + name
	if text, exists := args["text"]; exists {
		if err := tool.Host.Set(ctx, key, map[string]any{"name": name, "text": text}); err != nil {
			return nil, err
		}
	}
	value, err := tool.Host.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return map[string]any{"note": value}, nil
}
