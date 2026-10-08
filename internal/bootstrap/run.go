package bootstrap

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/config"
	"github.com/xingexin/catbot/internal/infra/store"
	infratemporal "github.com/xingexin/catbot/internal/infra/temporal"
	workertemporal "github.com/xingexin/catbot/internal/worker/temporal"
	"go.temporal.io/sdk/client"
	"log/slog"
	"net/http"
	"os"
	"os/signal"

	"syscall"
	"time"
)

func Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer s.Close()
	a, err := New(s, cfg.App)
	if err != nil {
		return err
	}
	defer a.Close()
	if err := registerChannels(a, cfg); err != nil {
		return err
	}
	c, err := client.DialContext(ctx, client.Options{HostPort: cfg.TemporalAddress, Namespace: cfg.TemporalNamespace})
	if err != nil {
		return err
	}
	defer c.Close()
	engine := infratemporal.New(c, s)
	a.Tasks.Scheduler = engine
	a.Execution.ExecutionClosed = engine.ExecutionClosed
	if err := a.Bootstrap(ctx); err != nil {
		return err
	}
	w := workertemporal.New(c, engine.TaskQueue, a.Execution)
	if err := w.Start(); err != nil {
		return err
	}
	defer w.Stop()
	a.Start()
	server := &http.Server{Addr: cfg.ListenAddr, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	errs := make(chan error, 1)
	go func() { slog.Info("catbot listening", "address", server.Addr); errs <- server.ListenAndServe() }()
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
