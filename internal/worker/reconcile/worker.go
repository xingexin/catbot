package reconcile

import (
	"context"
	"sync"
	"time"
)

type Worker struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func Start(work ...func(context.Context)) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Worker{cancel: cancel}
	for _, run := range work {
		w.wg.Go(func() {
			run(ctx)
			timer := time.NewTicker(15 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					run(ctx)
				}
			}
		})
	}
	return w
}
func (w *Worker) Close() {
	if w != nil {
		w.cancel()
		w.wg.Wait()
	}
}
