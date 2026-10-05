package expiry_test

import (
	"context"
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/expiry"
)

type fakeSweeper struct {
	calls chan time.Time
}

func (f *fakeSweeper) SweepExpired(ctx context.Context, now time.Time) (int, error) {
	select {
	case f.calls <- now:
	default:
	}
	return 0, nil
}

func TestTimerTicksAndStops(t *testing.T) {
	sw := &fakeSweeper{calls: make(chan time.Time, 4)}
	timer := expiry.New(sw, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- timer.Run(ctx) }()

	select {
	case <-sw.calls:
	case <-time.After(time.Second):
		t.Fatal("timer never swept")
	}

	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("run returned %v want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timer did not stop")
	}
}
