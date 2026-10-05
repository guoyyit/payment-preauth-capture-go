package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/service"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

func TestExpiryAutoRelease(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 50_000, time.Hour)

	// Before expiry: capture must still work.
	if _, err := f.svc.Capture(ctx, service.CaptureRequest{
		IdempotencyKey: "cap", PreAuthID: "pa", CaptureID: "c1",
		Amount: 20_000, Now: f.now.Add(30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	// After expiry: capture must be rejected.
	_, err := f.svc.Capture(ctx, service.CaptureRequest{
		IdempotencyKey: "cap-late", PreAuthID: "pa", CaptureID: "c2",
		Amount: 10_000, Now: f.now.Add(2 * time.Hour),
	})
	if !errors.Is(err, domain.ErrPreAuthExpired) {
		t.Fatalf("want ErrPreAuthExpired, got %v", err)
	}

	n, err := f.svc.SweepExpired(ctx, f.now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("released=%d want 1", n)
	}

	pa, _ := f.svc.GetPreAuth(ctx, "pa")
	if pa.Status != domain.PreAuthCaptured {
		t.Fatalf("status=%s want CAPTURED (partly captured then remainder released)", pa.Status)
	}
	if pa.RemainingHeld() != 0 {
		t.Fatalf("remaining=%d want 0", pa.RemainingHeld())
	}
	acct, _ := f.svc.GetAccount(ctx, "acct")
	if acct.Reserved != 0 {
		t.Fatalf("reserved=%d want 0 after release", acct.Reserved)
	}
	if acct.Balance != 80_000 {
		t.Fatalf("balance=%d want 80000", acct.Balance)
	}

	// Release event must be auditable.
	events, err := f.svc.ListEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == domain.EventAutoReleased && e.Amount == 30_000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("auto-release event for 30000 not found in %d events", len(events))
	}

	// Second sweep must not double-release.
	n2, err := f.svc.SweepExpired(ctx, f.now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second sweep released=%d want 0", n2)
	}
	acct2, _ := f.svc.GetAccount(ctx, "acct")
	if acct2.Reserved != 0 || acct2.Balance != 80_000 {
		t.Fatalf("account changed after second sweep: %+v", acct2)
	}
}

func TestExpiryFullReleaseStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 40_000, time.Hour)

	if _, err := f.svc.SweepExpired(ctx, f.now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	pa, _ := f.svc.GetPreAuth(ctx, "pa")
	if pa.Status != domain.PreAuthReleased {
		t.Fatalf("status=%s want RELEASED", pa.Status)
	}
	acct, _ := f.svc.GetAccount(ctx, "acct")
	if acct.Balance != 100_000 || acct.Reserved != 0 {
		t.Fatalf("funds not restored: %+v", acct)
	}

	// Any post-release operation is a state-machine violation.
	_, err := f.svc.Capture(ctx, service.CaptureRequest{
		IdempotencyKey: "late", PreAuthID: "pa", CaptureID: "c",
		Amount: 1, Now: f.now.Add(2 * time.Hour),
	})
	if !errors.Is(err, domain.ErrInvalidState) && !errors.Is(err, domain.ErrPreAuthExpired) {
		t.Fatalf("want illegal state/expired, got %v", err)
	}
}

func TestCrashRecoveryAcrossRestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 70_000, 24*time.Hour)

	if _, err := f.svc.Capture(ctx, service.CaptureRequest{
		IdempotencyKey: "cap1", PreAuthID: "pa", CaptureID: "c1",
		Amount: 30_000, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Refund(ctx, service.RefundRequest{
		IdempotencyKey: "ref1", PreAuthID: "pa", CaptureID: "c1",
		RefundID: "r1", Amount: 10_000, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Void(ctx, service.VoidRequest{
		IdempotencyKey: "void1", PreAuthID: "pa", VoidID: "v1",
		Amount: 15_000, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	batch, err := f.svc.Settle(ctx, service.SettleRequest{
		IdempotencyKey: "set1", PeriodID: "p1", BatchID: "b1", Now: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a full process restart by reopening the same data file.
	path := f.db.Path()
	db2, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svc2 := service.New(db2)

	pa, err := svc2.GetPreAuth(ctx, "pa")
	if err != nil {
		t.Fatal(err)
	}
	if pa.HeldAmount != 70_000 || pa.CapturedAmount != 30_000 ||
		pa.VoidedAmount != 15_000 || pa.RefundedAmount != 10_000 {
		t.Fatalf("preauth state lost: %+v", pa)
	}
	acct, err := svc2.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if acct.Balance != 80_000 || acct.Reserved != 25_000 {
		t.Fatalf("account after restart balance=%d reserved=%d", acct.Balance, acct.Reserved)
	}

	// Retrying the settlement with the same key returns the same batch and
	// never double counts.
	same, err := svc2.Settle(ctx, service.SettleRequest{
		IdempotencyKey: "set1", PeriodID: "p1", BatchID: "b1", Now: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if same.ID != batch.ID || same.TotalAmount != batch.TotalAmount ||
		len(same.CaptureIDs) != 1 {
		t.Fatalf("batch replay mismatch: %+v vs %+v", same, batch)
	}

	// A new period settles nothing: c1 was already marked settled.
	empty, err := svc2.Settle(ctx, service.SettleRequest{
		IdempotencyKey: "set2", PeriodID: "p2", BatchID: "b2", Now: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.CaptureIDs) != 0 || empty.TotalAmount != 0 {
		t.Fatalf("already-settled capture recounted: %+v", empty)
	}

	events, err := svc2.ListEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 {
		t.Fatalf("audit events lost after restart: %d", len(events))
	}
}
