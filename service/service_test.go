package service_test

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/service"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

type fixture struct {
	svc *service.Service
	db  *store.FileStore
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.json")
	db, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	svc := service.New(db)
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	ctx := context.Background()
	if _, err := svc.CreateAccount(ctx, service.CreateAccountRequest{
		AccountID: "acct",
		Balance:   100_000,
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return &fixture{svc: svc, db: db, now: now}
}

func (f *fixture) preauth(t *testing.T, id string, amount int64, ttl time.Duration) *domain.PreAuth {
	t.Helper()
	pa, err := f.svc.PreAuthorize(context.Background(), service.PreAuthRequest{
		IdempotencyKey: "key-" + id,
		AccountID:      "acct",
		PreAuthID:      id,
		Amount:         amount,
		TTL:            ttl,
		Now:            f.now,
	})
	if err != nil {
		t.Fatalf("preauth: %v", err)
	}
	return pa
}

func TestConcurrentCaptureNoOverrun(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 60_000, time.Hour)

	const workers = 60
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	var acceptedErr error
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.svc.Capture(ctx, service.CaptureRequest{
				IdempotencyKey: "cap-key-" + strconv.Itoa(i),
				PreAuthID:      "pa",
				CaptureID:      "cap-" + strconv.Itoa(i),
				Amount:         10_000,
				Now:            f.now.Add(time.Minute),
			})
			mu.Lock()
			if err == nil {
				accepted++
			} else if !errors.Is(err, domain.ErrAmountExceedsHeld) &&
				!errors.Is(err, domain.ErrInvalidState) {
				acceptedErr = err
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if acceptedErr != nil {
		t.Fatalf("unexpected capture error: %v", acceptedErr)
	}
	if accepted != 6 {
		t.Fatalf("expected exactly 6 accepted captures, got %d", accepted)
	}
	pa, err := f.svc.GetPreAuth(ctx, "pa")
	if err != nil {
		t.Fatal(err)
	}
	if pa.CapturedAmount != 60_000 {
		t.Fatalf("captured=%d want 60000", pa.CapturedAmount)
	}
	if pa.RemainingHeld() != 0 {
		t.Fatalf("remaining=%d want 0", pa.RemainingHeld())
	}
	if pa.Status != domain.PreAuthCaptured {
		t.Fatalf("status=%s want CAPTURED", pa.Status)
	}
	acct, err := f.svc.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if acct.Reserved != 0 || acct.Balance != 40_000 {
		t.Fatalf("account balance=%d reserved=%d", acct.Balance, acct.Reserved)
	}
}

func TestDuplicateCaptureReplaysFirstResult(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 50_000, time.Hour)

	req := service.CaptureRequest{
		IdempotencyKey: "same-key",
		PreAuthID:      "pa",
		CaptureID:      "cap-1",
		Amount:         20_000,
		Now:            f.now,
	}
	first, err := f.svc.Capture(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.Capture(ctx, req)
	if err != nil {
		t.Fatalf("replay should not error: %v", err)
	}
	if first.ID != second.ID || first.Amount != second.Amount {
		t.Fatalf("replay mismatch: %+v vs %+v", first, second)
	}
	pa, _ := f.svc.GetPreAuth(ctx, "pa")
	if pa.CapturedAmount != 20_000 {
		t.Fatalf("captured=%d want 20000 (no double effect)", pa.CapturedAmount)
	}
}

func TestDuplicateFailedRequestReplaysFailure(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 10_000, time.Hour)

	req := service.CaptureRequest{
		IdempotencyKey: "oversize",
		PreAuthID:      "pa",
		CaptureID:      "cap-x",
		Amount:         50_000,
		Now:            f.now,
	}
	if _, err := f.svc.Capture(ctx, req); !errors.Is(err, domain.ErrAmountExceedsHeld) {
		t.Fatalf("want ErrAmountExceedsHeld, got %v", err)
	}
	// Even though it failed the first time, the same key replays the failure
	// instead of re-executing.
	if _, err := f.svc.Capture(ctx, req); !errors.Is(err, domain.ErrAmountExceedsHeld) {
		t.Fatalf("replay want ErrAmountExceedsHeld, got %v", err)
	}
	pa, _ := f.svc.GetPreAuth(ctx, "pa")
	if pa.CapturedAmount != 0 {
		t.Fatalf("failed capture must not capture, got %d", pa.CapturedAmount)
	}
}

func TestPartialVoidBoundary(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 60_000, time.Hour)

	if _, err := f.svc.Capture(ctx, service.CaptureRequest{
		IdempotencyKey: "cap", PreAuthID: "pa", CaptureID: "c1",
		Amount: 20_000, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Void(ctx, service.VoidRequest{
		IdempotencyKey: "void", PreAuthID: "pa", VoidID: "v1",
		Amount: 30_000, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	// Capture + void == 50000; another 20000 capture must be rejected.
	_, err := f.svc.Capture(ctx, service.CaptureRequest{
		IdempotencyKey: "cap2", PreAuthID: "pa", CaptureID: "c2",
		Amount: 20_000, Now: f.now,
	})
	if !errors.Is(err, domain.ErrAmountExceedsHeld) {
		t.Fatalf("want ErrAmountExceedsHeld, got %v", err)
	}
	// Releasing the remaining 10000 must succeed.
	if _, err := f.svc.Void(ctx, service.VoidRequest{
		IdempotencyKey: "void-rest", PreAuthID: "pa", VoidID: "v2",
		Amount: 0, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	acct, _ := f.svc.GetAccount(ctx, "acct")
	if acct.Reserved != 0 {
		t.Fatalf("reserved=%d want 0", acct.Reserved)
	}
	if acct.Balance != 80_000 {
		t.Fatalf("balance=%d want 80000", acct.Balance)
	}
}
