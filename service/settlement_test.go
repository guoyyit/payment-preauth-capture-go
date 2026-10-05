package service_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/service"
)

func TestDuplicateSettlementIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 60_000, 24*time.Hour)

	for i, amt := range []int64{20_000, 20_000, 20_000} {
		if _, err := f.svc.Capture(ctx, service.CaptureRequest{
			IdempotencyKey: "cap-" + strconv.Itoa(i), PreAuthID: "pa",
			CaptureID: "cap-" + strconv.Itoa(i), Amount: amt, Now: f.now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	req := service.SettleRequest{
		IdempotencyKey: "settle", PeriodID: "2026-10-05",
		BatchID: "b1", Now: f.now,
	}
	first, err := f.svc.Settle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.Settle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.TotalAmount != second.TotalAmount ||
		len(first.CaptureIDs) != len(second.CaptureIDs) {
		t.Fatalf("settle replay mismatch: %+v vs %+v", first, second)
	}
	if first.TotalAmount != 60_000 || len(first.CaptureIDs) != 3 {
		t.Fatalf("batch=%+v", first)
	}

	// A different period settles nothing; old captures are not recounted.
	next, err := f.svc.Settle(ctx, service.SettleRequest{
		IdempotencyKey: "settle-2", PeriodID: "2026-10-06",
		BatchID: "b2", Now: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.CaptureIDs) != 0 || next.TotalAmount != 0 {
		t.Fatalf("recounted settled captures: %+v", next)
	}
}

func TestConcurrentSettlementRunsOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 100_000, 24*time.Hour)
	for i := 0; i < 5; i++ {
		if _, err := f.svc.Capture(ctx, service.CaptureRequest{
			IdempotencyKey: "cap-" + strconv.Itoa(i), PreAuthID: "pa",
			CaptureID: "cap-" + strconv.Itoa(i), Amount: 10_000, Now: f.now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	const runners = 8
	var wg sync.WaitGroup
	results := make([]*domain.SettlementBatch, runners)
	errs := make([]error, runners)
	for i := 0; i < runners; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b, err := f.svc.Settle(ctx, service.SettleRequest{
				IdempotencyKey: "same-settle", PeriodID: "p",
				BatchID: "b", Now: f.now,
			})
			results[i] = b
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("settle error: %v", err)
		}
	}
	for i, b := range results {
		if b == nil || b.ID != "b" || b.TotalAmount != 50_000 ||
			len(b.CaptureIDs) != 5 {
			t.Fatalf("runner %d got inconsistent batch: %+v", i, b)
		}
	}

	// Every capture must be settled exactly once.
	settled := map[string]int{}
	for _, b := range results {
		for _, id := range b.CaptureIDs {
			settled[id]++
		}
	}
	if len(settled) != 5 {
		t.Fatalf("unique settled captures=%d want 5", len(settled))
	}
}

func TestRefundConcurrentWithSettlementAmountConsistent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 100_000, 24*time.Hour)

	if _, err := f.svc.Capture(ctx, service.CaptureRequest{
		IdempotencyKey: "cap", PreAuthID: "pa", CaptureID: "cap",
		Amount: 60_000, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}

	// Refund 25000 races with batch generation.
	var wg sync.WaitGroup
	var batch *domain.SettlementBatch
	var settleErr error
	var refundErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		batch, settleErr = f.svc.Settle(ctx, service.SettleRequest{
			IdempotencyKey: "settle", PeriodID: "p", BatchID: "b", Now: f.now,
		})
	}()
	go func() {
		defer wg.Done()
		_, refundErr = f.svc.Refund(ctx, service.RefundRequest{
			IdempotencyKey: "ref", PreAuthID: "pa", CaptureID: "cap",
			RefundID: "ref", Amount: 25_000, Now: f.now,
		})
	}()
	wg.Wait()
	if settleErr != nil || refundErr != nil {
		t.Fatalf("settle=%v refund=%v", settleErr, refundErr)
	}

	// Final invariant: batch total + post-batch refunded remaining == capture,
	// and the captured record's settled flag/refund totals are consistent.
	pa, err := f.svc.GetPreAuth(ctx, "pa")
	if err != nil {
		t.Fatal(err)
	}
	acct, _ := f.svc.GetAccount(ctx, "acct")
	// capture 60000 -> balance 40000 reserved 40000; refund 25000 -> 65000.
	if acct.Balance != 65_000 {
		t.Fatalf("balance=%d want 65000", acct.Balance)
	}
	if pa.RefundedAmount != 25_000 {
		t.Fatalf("refunded=%d want 25000", pa.RefundedAmount)
	}
	finalBatch, err := f.svc.GetBatch(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Refunds either predate the batch (excluded from gross) or postdate it
	// (applied as batch refunds): the final net total is always 35000.
	if finalBatch.TotalAmount != 35_000 {
		t.Fatalf("final batch net=%d want 35000 (gross=%d refund=%d)",
			finalBatch.TotalAmount, finalBatch.GrossAmount, finalBatch.RefundAmount)
	}
	if finalBatch.GrossAmount-finalBatch.RefundAmount != 35_000 {
		t.Fatalf("gross-refund != net: %+v", finalBatch)
	}
	if len(finalBatch.CaptureIDs) != 1 {
		t.Fatalf("capture counted %d times, want exactly 1", len(finalBatch.CaptureIDs))
	}
}

func TestRefundAfterSettlementAdjustsBatch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.preauth(t, "pa", 100_000, 24*time.Hour)
	if _, err := f.svc.Capture(ctx, service.CaptureRequest{
		IdempotencyKey: "cap", PreAuthID: "pa", CaptureID: "cap",
		Amount: 60_000, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	batch, err := f.svc.Settle(ctx, service.SettleRequest{
		IdempotencyKey: "s", PeriodID: "p", BatchID: "b", Now: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if batch.TotalAmount != 60_000 || batch.GrossAmount != 60_000 {
		t.Fatalf("initial batch=%+v", batch)
	}
	if _, err := f.svc.Refund(ctx, service.RefundRequest{
		IdempotencyKey: "r", PreAuthID: "pa", CaptureID: "cap",
		RefundID: "r", Amount: 40_000, Now: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	adjusted, err := f.svc.GetBatch(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if adjusted.GrossAmount != 60_000 || adjusted.RefundAmount != 40_000 ||
		adjusted.TotalAmount != 20_000 || len(adjusted.RefundIDs) != 1 {
		t.Fatalf("batch not adjusted after refund: %+v", adjusted)
	}
	retry, err := f.svc.Settle(ctx, service.SettleRequest{
		IdempotencyKey: "s", PeriodID: "p", BatchID: "b", Now: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retry.TotalAmount != 20_000 || len(retry.CaptureIDs) != 1 {
		t.Fatalf("settle retry changed batch: %+v", retry)
	}
}
