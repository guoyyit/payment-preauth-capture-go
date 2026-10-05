package service

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/internal/domain"
)

func period() (int64, int64) {
	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	return base.Add(-time.Hour).UnixMilli(), base.Add(time.Hour).UnixMilli()
}

func TestSettleCycle_NetAmountNoDuplicate(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 10000)
	auth := mustCreateAuth(t, svc, "acc", 1000, 24*time.Hour)

	c1, _ := svc.Capture(CaptureRequest{IdempotencyKey: "c1", AuthID: auth.ID, Amount: 400})
	c2, _ := svc.Capture(CaptureRequest{IdempotencyKey: "c2", AuthID: auth.ID, Amount: 300})
	if _, err := svc.Refund(RefundRequest{IdempotencyKey: "r1", CaptureID: c2.Capture.ID, Amount: 100}); err != nil {
		t.Fatal(err)
	}

	start, end := period()
	batch, err := svc.SettleCycle(SettlementRequest{IdempotencyKey: "b1", PeriodStart: start, PeriodEnd: end})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if batch.TotalAmount != 600 || len(batch.CaptureIDs) != 2 {
		t.Fatalf("batch wrong: %+v", batch)
	}
	if batch.Status != domain.BatchConfirmed {
		t.Fatalf("SettleCycle must confirm batch: %+v", batch)
	}

	// 不同幂等键的再次结算：同一批捕获不得重复计入 -> 空批次错误。
	_, err = svc.SettleCycle(SettlementRequest{IdempotencyKey: "b2", PeriodStart: start, PeriodEnd: end})
	if GetErrorCode(err) != ErrCodeSettleEmpty {
		t.Fatalf("want settle_empty on second settlement, got %v", err)
	}

	// 已结算捕获不可再退款。
	_, err = svc.Refund(RefundRequest{IdempotencyKey: "r2", CaptureID: c1.Capture.ID, Amount: 1})
	if GetErrorCode(err) != ErrCodeCaptureSettled {
		t.Fatalf("want capture_already_settled, got %v", err)
	}

	// 捕获记录落为已结算。
	got1, _ := svc.GetCapture(c1.Capture.ID)
	if !got1.Settled || got1.BatchID != batch.ID {
		t.Fatalf("capture not marked settled: %+v", got1)
	}
}

func TestSettleCycle_IdempotentReplay(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 500, time.Hour)
	if _, err := svc.Capture(CaptureRequest{IdempotencyKey: "c1", AuthID: auth.ID, Amount: 200}); err != nil {
		t.Fatal(err)
	}
	start, end := period()
	b1, err := svc.SettleCycle(SettlementRequest{IdempotencyKey: "dup", PeriodStart: start, PeriodEnd: end})
	if err != nil {
		t.Fatal(err)
	}
	b2, err := svc.SettleCycle(SettlementRequest{IdempotencyKey: "dup", PeriodStart: start, PeriodEnd: end})
	if err != nil {
		t.Fatalf("replay must succeed: %v", err)
	}
	if b1.ID != b2.ID || b1.TotalAmount != b2.TotalAmount {
		t.Fatalf("replay must return the first batch: %+v vs %+v", b1, b2)
	}
	if len(svc.ListBatches()) != 1 {
		t.Fatalf("replay must not create a second batch")
	}
}

func TestGenerateAndConfirm_RetryIdempotent(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 500, time.Hour)
	if _, err := svc.Capture(CaptureRequest{IdempotencyKey: "c1", AuthID: auth.ID, Amount: 200}); err != nil {
		t.Fatal(err)
	}
	start, end := period()
	batch, err := svc.GenerateBatch(SettlementRequest{IdempotencyKey: "g1", PeriodStart: start, PeriodEnd: end})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != domain.BatchOpen {
		t.Fatalf("generated batch must be open: %+v", batch)
	}

	// open 批次锁定的捕获拒绝退款（待定窗口）。
	caps, _ := svc.ListCaptures(auth.ID)
	_, err = svc.Refund(RefundRequest{IdempotencyKey: "rl", CaptureID: caps[0].ID, Amount: 1})
	if GetErrorCode(err) != ErrCodeIllegalState {
		t.Fatalf("refund against open batch must be rejected: %v", err)
	}

	c1, err := svc.ConfirmBatch(ConfirmBatchRequest{IdempotencyKey: "cf1", BatchID: batch.ID})
	if err != nil {
		t.Fatal(err)
	}
	// 重复确认幂等。
	c2, err := svc.ConfirmBatch(ConfirmBatchRequest{IdempotencyKey: "cf1", BatchID: batch.ID})
	if err != nil || c1.Attempts != 1 || c2.Attempts != 1 {
		t.Fatalf("confirm retry must be idempotent: %+v %+v err=%v", c1, c2, err)
	}
}

func TestSettle_ConcurrentWithRefund_FinalAmountConsistent(t *testing.T) {
	for iter := 0; iter < 50; iter++ {
		svc, _ := newTestService(t)
		seedAccount(t, svc, "acc", 10000)
		auth := mustCreateAuth(t, svc, "acc", 1000, 24*time.Hour)
		res, err := svc.Capture(CaptureRequest{IdempotencyKey: "c-" + strconv.Itoa(iter), AuthID: auth.ID, Amount: 1000})
		if err != nil {
			t.Fatal(err)
		}
		capID := res.Capture.ID
		start, end := period()

		var wg sync.WaitGroup
		wg.Add(2)
		begin := make(chan struct{})
		var settleErr, refundErr error
		go func() {
			defer wg.Done()
			<-begin
			_, settleErr = svc.SettleCycle(SettlementRequest{
				IdempotencyKey: "s-" + strconv.Itoa(iter),
				PeriodStart:    start,
				PeriodEnd:      end,
			})
		}()
		go func() {
			defer wg.Done()
			<-begin
			_, refundErr = svc.Refund(RefundRequest{
				IdempotencyKey: "rf-" + strconv.Itoa(iter),
				CaptureID:      capID,
				Amount:         400,
			})
		}()
		close(begin)
		wg.Wait()

		c, _ := svc.GetCapture(capID)
		batches := svc.ListBatches()
		// 两种确定顺序：
		//   1) 结算先提交：退款被拒（capture_already_settled），批次 1000，退款 0；
		//   2) 退款先提交：退款 400 生效，结算以净额 600 确认。
		// 两种都可能出现，结果由互斥锁顺序唯一确定。
		if settleErr == nil && GetErrorCode(refundErr) == ErrCodeCaptureSettled {
			if len(batches) != 1 || batches[0].TotalAmount != 1000 || c.Refunded != 0 {
				t.Fatalf("iter %d settle-first state wrong: batches=%+v capture=%+v", iter, batches, c)
			}
		} else if refundErr == nil && settleErr == nil {
			if len(batches) != 1 || batches[0].TotalAmount != 600 || c.Refunded != 400 {
				t.Fatalf("iter %d refund-first state wrong: batches=%+v capture=%+v", iter, batches, c)
			}
		} else {
			t.Fatalf("iter %d unexpected outcome: settle=%v refund=%v", iter, settleErr, refundErr)
		}

		// 无论哪种顺序，捕获最多计入一个批次且金额非负。
		if c.Refunded > c.Amount {
			t.Fatalf("iter %d over refund: %+v", iter, c)
		}
	}
}
