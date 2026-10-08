package main

import (
	"github.com/xingexin/catbot/internal/bootstrap"
	"log/slog"
	"os"
)

func main() {
	if err := bootstrap.Run(); err != nil {
		slog.Error("catbot stopped", "error", err)
		os.Exit(1)
	}
}
