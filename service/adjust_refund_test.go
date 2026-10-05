package service_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/service"
)

func TestAdjustUpChecksAvailable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// Hold 80000 of 100000 -> available 20000.
	f.preauth(t, "pa", 80_000, 24*time.Hour)

	// +30000 exceeds the 20000 available.
	_, err := f.svc.Adjust(ctx, service.AdjustRequest{
		IdempotencyKey: "adj-up-bad", PreAuthID: "pa",
		NewAmount: 110_000, Now: f.now,
	})
	if !errors.Is(err, domain.ErrInsufficientFunds) {
		t.Fatalf("want ErrInsufficientFunds, got %v", err)
	}

	// +10000 fits.
	pa, err := f.svc.Adjust(ctx, service.AdjustRequest{
		IdempotencyKey: "adj-up-ok", PreAuthID: "pa",
		NewAmount: 90_000, Now: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pa.HeldAmount != 90_000 {
		t.Fatalf("held=%d want 90000", pa.HeldAmount)
	}
	acct, _ := f.svc.GetAccount(ctx, "acct")
	if acct.Reserved != 90_000 || acct.Balance != 100_000 {
		t.Fatalf("acct=%+v", acct)
	}
}

func TestAdjustDownReleasesImmediately(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 80_000, 24*time.Hour)

	if _, err := f.svc.Capture(ctx, service.CaptureRequest{
		IdempotencyKey: "cap", PreAuthID: "pa", CaptureID: "c1",
		Amount: 20_000, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}

	pa, err := f.svc.Adjust(ctx, service.AdjustRequest{
		IdempotencyKey: "adj-down", PreAuthID: "pa",
		NewAmount: 50_000, Now: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pa.HeldAmount != 50_000 || pa.RemainingHeld() != 30_000 {
		t.Fatalf("held=%d remaining=%d", pa.HeldAmount, pa.RemainingHeld())
	}
	acct, _ := f.svc.GetAccount(ctx, "acct")
	if acct.Reserved != 30_000 {
		t.Fatalf("reserved=%d want 30000", acct.Reserved)
	}

	// Cannot shrink below already committed 20000.
	_, err = f.svc.Adjust(ctx, service.AdjustRequest{
		IdempotencyKey: "adj-too-low", PreAuthID: "pa",
		NewAmount: 10_000, Now: f.now,
	})
	if !errors.Is(err, domain.ErrAmountExceedsHeld) {
		t.Fatalf("want ErrAmountExceedsHeld, got %v", err)
	}
}

func TestAdjustConcurrentWithCaptureIsDeterministic(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 100_000, 24*time.Hour)

	// Race: 20 captures of 6000 and an upward adjust of +50000
	// (account has exactly 100000 total, available starts at 0).
	const workers = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	capturedOK := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.svc.Capture(ctx, service.CaptureRequest{
				IdempotencyKey: "cap-" + strconv.Itoa(i), PreAuthID: "pa",
				CaptureID: "c-" + strconv.Itoa(i),
				Amount:    6_000,
				Now:       f.now,
			})
			mu.Lock()
			if err == nil {
				capturedOK++
			}
			mu.Unlock()
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = f.svc.Adjust(ctx, service.AdjustRequest{
			IdempotencyKey: "adj", PreAuthID: "pa",
			NewAmount: 150_000, Now: f.now,
		})
	}()
	wg.Wait()

	pa, err := f.svc.GetPreAuth(ctx, "pa")
	if err != nil {
		t.Fatal(err)
	}
	acct, _ := f.svc.GetAccount(ctx, "acct")

	// Whichever order the global lock chose, invariants must hold:
	// reserved == remaining hold and no balance negative.
	if acct.Reserved != pa.RemainingHeld() {
		t.Fatalf("reserved=%d != remaining=%d (non-deterministic state)",
			acct.Reserved, pa.RemainingHeld())
	}
	if acct.Balance < 0 || acct.Reserved < 0 {
		t.Fatalf("negative funds: %+v", acct)
	}
	if pa.CapturedAmount > pa.HeldAmount {
		t.Fatalf("captured %d > held %d", pa.CapturedAmount, pa.HeldAmount)
	}
	if pa.HeldAmount == 100_000 {
		// Adjust lost the race: at most 16 captures (96000) fit.
		if capturedOK > 16 {
			t.Fatalf("captured=%d without adjust, want <=16", capturedOK)
		}
	}
	t.Logf("adjust result held=%d captured=%d captures=%d reserved=%d balance=%d",
		pa.HeldAmount, pa.CapturedAmount, capturedOK, acct.Reserved, acct.Balance)
}

func TestRefundBoundaries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 50_000, 24*time.Hour)

	if _, err := f.svc.Capture(ctx, service.CaptureRequest{
		IdempotencyKey: "cap", PreAuthID: "pa", CaptureID: "c1",
		Amount: 30_000, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}

	// Partial refund.
	if _, err := f.svc.Refund(ctx, service.RefundRequest{
		IdempotencyKey: "r1", PreAuthID: "pa", CaptureID: "c1",
		RefundID: "r1", Amount: 20_000, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	// 10000 remains refundable; 15000 must be rejected.
	_, err := f.svc.Refund(ctx, service.RefundRequest{
		IdempotencyKey: "r2-bad", PreAuthID: "pa", CaptureID: "c1",
		RefundID: "r2", Amount: 15_000, Now: f.now,
	})
	if !errors.Is(err, domain.ErrAmountExceedsCaptured) {
		t.Fatalf("want ErrAmountExceedsCaptured, got %v", err)
	}
	// Refund the rest via amount=0.
	r3, err := f.svc.Refund(ctx, service.RefundRequest{
		IdempotencyKey: "r3", PreAuthID: "pa", CaptureID: "c1",
		RefundID: "r3", Amount: 0, Now: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r3.Amount != 10_000 {
		t.Fatalf("auto remaining refund=%d want 10000", r3.Amount)
	}

	pa, _ := f.svc.GetPreAuth(ctx, "pa")
	if pa.RefundedAmount != 30_000 {
		t.Fatalf("refunded=%d want 30000", pa.RefundedAmount)
	}
	acct, _ := f.svc.GetAccount(ctx, "acct")
	// Captured 30000 left balance 70000; refunding 30000 restores it.
	if acct.Balance != 100_000 {
		t.Fatalf("balance=%d want 100000", acct.Balance)
	}

	// Duplicate refund key replays first refund, no second effect.
	again, err := f.svc.Refund(ctx, service.RefundRequest{
		IdempotencyKey: "r1", PreAuthID: "pa", CaptureID: "c1",
		RefundID: "r1", Amount: 20_000, Now: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Amount != 20_000 {
		t.Fatalf("replay amount=%d want 20000", again.Amount)
	}
	pa2, _ := f.svc.GetPreAuth(ctx, "pa")
	if pa2.RefundedAmount != 30_000 {
		t.Fatalf("duplicate refund changed state: %d", pa2.RefundedAmount)
	}
}
