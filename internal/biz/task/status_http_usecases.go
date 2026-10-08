package task

import (
	"context"
	"errors"
)

func (a *Commands) Ping(ctx context.Context) error {
	if a.Scheduler == nil {
		return errors.New("Temporal is not connected")
	}
	return a.Scheduler.Ping(ctx)
}
