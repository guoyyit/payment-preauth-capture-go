package expiry

import (
	"context"
	"time"
)

// ExpiredSweeper is implemented by *service.Service.
type ExpiredSweeper interface {
	SweepExpired(ctx context.Context, now time.Time) (int, error)
}

// Timer periodically releases expired preauth holds.
type Timer struct {
	svc      ExpiredSweeper
	interval time.Duration
}

func New(svc ExpiredSweeper, interval time.Duration) *Timer {
	return &Timer{svc: svc, interval: interval}
}

// Run blocks until ctx is cancelled, sweeping on every tick.
func (sw *Timer) Run(ctx context.Context) error {
	ticker := time.NewTicker(sw.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := sw.svc.SweepExpired(ctx, time.Now()); err != nil {
				// A sweep failure is retried on the next tick; keep running.
				_ = err
			}
		}
	}
}
