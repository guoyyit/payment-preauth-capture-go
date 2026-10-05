package service

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestRefund_PartialAndBoundary(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 500, time.Hour)

	capRes, err := svc.Capture(CaptureRequest{IdempotencyKey: "c1", AuthID: auth.ID, Amount: 300})
	if err != nil {
		t.Fatal(err)
	}
	capID := capRes.Capture.ID

	if _, err := svc.Refund(RefundRequest{IdempotencyKey: "r0", CaptureID: capID, Amount: 0}); GetErrorCode(err) != ErrCodeInvalidAmount {
		t.Fatalf("zero refund must be invalid: %v", err)
	}
	if _, err := svc.Refund(RefundRequest{IdempotencyKey: "r1", CaptureID: capID, Amount: 100}); err != nil {
		t.Fatalf("first partial refund: %v", err)
	}
	if _, err := svc.Refund(RefundRequest{IdempotencyKey: "r2", CaptureID: capID, Amount: 150}); err != nil {
		t.Fatalf("second partial refund: %v", err)
	}
	// 已退 250，再退 51 超过可退 50。
	if _, err := svc.Refund(RefundRequest{IdempotencyKey: "r3", CaptureID: capID, Amount: 51}); GetErrorCode(err) != ErrCodeExceedRefundable {
		t.Fatalf("want exceed_refundable, got %v", err)
	}
	// 恰好退完最后 50。
	if _, err := svc.Refund(RefundRequest{IdempotencyKey: "r4", CaptureID: capID, Amount: 50}); err != nil {
		t.Fatalf("final refund to boundary: %v", err)
	}
	got, _ := svc.GetCapture(capID)
	if got.Refunded != 300 || got.Refundable() != 0 {
		t.Fatalf("refund boundary wrong: %+v", got)
	}
	acc := mustAccount(t, svc, "acc")
	if acc.Balance != 1000 {
		t.Fatalf("full refund must restore balance: %+v", acc)
	}
}

func TestRefund_UnknownCapture(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Refund(RefundRequest{IdempotencyKey: "rx", CaptureID: "nope", Amount: 1})
	if GetErrorCode(err) != ErrCodeNotFound {
		t.Fatalf("want not_found, got %v", err)
	}
}

func TestRefund_ConcurrentWithCapture_IsConsistent(t *testing.T) {
	// 同一捕获上并发“再捕获后退款”这里退化为：先并发 10 笔捕获 100，
	// 再并发对每笔捕获退 100 与对同笔再退 100，保证每笔最多退一次。
	for iter := 0; iter < 30; iter++ {
		svc, _ := newTestService(t)
		seedAccount(t, svc, "acc", 10000)
		auth := mustCreateAuth(t, svc, "acc", 1000, time.Hour)

		capIDs := make([]string, 10)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(10)
		var capMu sync.Mutex
		for i := 0; i < 10; i++ {
			i := i
			go func() {
				defer wg.Done()
				<-start
				res, err := svc.Capture(CaptureRequest{
					IdempotencyKey: "cap-" + strconv.Itoa(iter) + "-" + strconv.Itoa(i),
					AuthID:         auth.ID,
					Amount:         100,
				})
				if err != nil {
					t.Errorf("iter %d capture %d: %v", iter, i, err)
					return
				}
				capMu.Lock()
				capIDs[i] = res.Capture.ID
				capMu.Unlock()
			}()
		}
		close(start)
		wg.Wait()

		// 对每个捕获并发发两笔全额退款，恰好一笔成功。
		wg.Add(20)
		start2 := make(chan struct{})
		var okMu sync.Mutex
		refundOK := 0
		for i := 0; i < 10; i++ {
			for j := 0; j < 2; j++ {
				i, j := i, j
				go func() {
					defer wg.Done()
					<-start2
					_, err := svc.Refund(RefundRequest{
						IdempotencyKey: "ref-" + strconv.Itoa(iter) + "-" + strconv.Itoa(i) + "-" + strconv.Itoa(j),
						CaptureID:      capIDs[i],
						Amount:         100,
					})
					if err == nil {
						okMu.Lock()
						refundOK++
						okMu.Unlock()
					} else if GetErrorCode(err) != ErrCodeExceedRefundable {
						t.Errorf("iter %d unexpected refund err: %v", iter, err)
					}
				}()
			}
		}
		close(start2)
		wg.Wait()

		if refundOK != 10 {
			t.Fatalf("iter %d want exactly 10 successful refunds, got %d", iter, refundOK)
		}
		for _, id := range capIDs {
			c, _ := svc.GetCapture(id)
			if c.Refunded != 100 {
				t.Fatalf("iter %d capture %s refunded %d", iter, id, c.Refunded)
			}
		}
		acc := mustAccount(t, svc, "acc")
		if acc.Balance != 10000 {
			t.Fatalf("iter %d balance after full refunds: %+v", iter, acc)
		}
	}
}
