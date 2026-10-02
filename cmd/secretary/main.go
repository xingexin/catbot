package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"agentTest/internal/job"
	"agentTest/internal/service"
	"agentTest/internal/store"
	"go.temporal.io/sdk/client"
)

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func main() {
	if err := run(); err != nil {
		slog.Error("secretary stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if os.Getenv("ADMIN_PASSWORD") == "" || os.Getenv("RUNTIME_TOKEN") == "" {
		return errors.New("ADMIN_PASSWORD and RUNTIME_TOKEN are required")
	}
	s, err := store.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer s.Close()
	data, err := filepath.Abs(env("DATA_DIR", "data"))
	if err != nil {
		return err
	}
	plugins, err := filepath.Abs(env("PLUGIN_DIR", "plugins"))
	if err != nil {
		return err
	}
	uploadMB, err := strconv.Atoi(env("MAX_UPLOAD_MB", "100"))
	if err != nil || uploadMB < 1 || uploadMB > 1024 {
		return errors.New("MAX_UPLOAD_MB must be 1..1024")
	}
	a, err := service.New(s, service.Options{
		MaxUploadMB: uploadMB,
		DataDir:     data, PluginDir: plugins, InternalURL: env("INTERNAL_URL", "http://127.0.0.1:8080"), RuntimeURL: env("RUNTIME_URL", "http://127.0.0.1:8091"),
		RuntimeToken: os.Getenv("RUNTIME_TOKEN"), MasterKey: os.Getenv("MASTER_KEY"), AdminPassword: os.Getenv("ADMIN_PASSWORD"), CookieSecure: os.Getenv("COOKIE_SECURE") == "true",
		QQAppID: os.Getenv("QQ_APP_ID"), QQUser: os.Getenv("QQ_USER_OPENID"), QQConfigID: os.Getenv("QQ_CONFIG_ID"), QQPersonaID: os.Getenv("QQ_PERSONA_ID"),
	})
	if err != nil {
		return err
	}
	defer a.Close()
	if err := registerChannels(a); err != nil {
		return err
	}
	c, err := client.DialContext(ctx, client.Options{HostPort: env("TEMPORAL_ADDRESS", "127.0.0.1:7233"), Namespace: env("TEMPORAL_NAMESPACE", "default")})
	if err != nil {
		return err
	}
	defer c.Close()
	engine := job.New(c, s, a)
	a.Scheduler = engine
	a.Notifier = a.SendMessage
	if err := a.Bootstrap(ctx); err != nil {
		return err
	}
	w := engine.Worker()
	if err := w.Start(); err != nil {
		return err
	}
	defer w.Stop()
	a.Start()
	server := &http.Server{Addr: env("LISTEN_ADDR", ":8080"), Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	errs := make(chan error, 1)
	go func() { slog.Info("secretary listening", "address", server.Addr); errs <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}
