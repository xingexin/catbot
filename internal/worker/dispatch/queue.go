package dispatch

import (
	"context"
	"github.com/xingexin/catbot/internal/domain/conversation"
	convrepo "github.com/xingexin/catbot/internal/domain/conversation/repository"
	"github.com/xingexin/catbot/internal/infra/store"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Runner interface {
	Execute(context.Context, string) (conversation.Run, error)
}
type Dispatcher struct {
	Store    store.Store
	Runner   Runner
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	active   map[string]context.CancelFunc
	sessions map[string]bool
	wg       sync.WaitGroup
}

func New(s store.Store, r Runner) *Dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{Store: s, Runner: r, ctx: ctx, cancel: cancel, active: map[string]context.CancelFunc{}, sessions: map[string]bool{}}
}
func (a *Dispatcher) Start() {
	a.wg.Go(func() {
		timer := time.NewTicker(150 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-timer.C:
				a.dispatch()
			}
		}
	})
}
func (a *Dispatcher) Cancel(id string) bool {
	a.mu.Lock()
	cancel := a.active[id]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		return true
	}
	return false
}
func (a *Dispatcher) Close() {
	a.cancel()
	a.mu.Lock()
	for _, cancel := range a.active {
		cancel()
	}
	a.mu.Unlock()
	a.wg.Wait()
}
func (a *Dispatcher) dispatch() {
	a.mu.Lock()
	if len(a.active) >= 8 {
		a.mu.Unlock()
		return
	}
	busy := make([]string, 0, len(a.sessions))
	for id := range a.sessions {
		busy = append(busy, id)
	}
	a.mu.Unlock()
	queryCtx, queryCancel := context.WithTimeout(a.ctx, 3*time.Second)
	runs, err := convrepo.New(a.Store).QueuedRuns(queryCtx, busy, 64)
	queryCancel()
	if err != nil {
		slog.Error("load queue", "error", err)
		return
	}
	for _, r := range runs {
		if r.Status != "queued" || strings.HasPrefix(r.ID, "background-") {
			continue
		}
		a.mu.Lock()
		if a.sessions[r.SessionID] || len(a.active) >= 8 {
			a.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(a.ctx)
		a.sessions[r.SessionID] = true
		a.active[r.ID] = cancel
		a.mu.Unlock()
		a.wg.Go(func() {
			defer cancel()
			if _, err := a.Runner.Execute(ctx, r.ID); err != nil {
				slog.Info("run ended", "runId", r.ID, "error", err)
			}
			a.mu.Lock()
			delete(a.active, r.ID)
			delete(a.sessions, r.SessionID)
			a.mu.Unlock()
		})
	}
}
