package jobs

import (
	"context"
	"time"
)

type Runner struct {
	ticker *time.Ticker
	stop   chan struct{}
}

func New(interval time.Duration) *Runner {
	if interval <= 0 {
		interval = 1 * time.Minute
	}
	return &Runner{
		ticker: time.NewTicker(interval),
		stop:   make(chan struct{}),
	}
}

func (r *Runner) Start(ctx context.Context, fn func(context.Context)) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.stop:
				return
			case <-r.ticker.C:
				fn(ctx)
			}
		}
	}()
}

func (r *Runner) Stop() {
	close(r.stop)
	r.ticker.Stop()
}
