package service

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/internal/domain"
)

func TestAdjust_UpChecksAvailableBalance(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 500)
	auth := mustCreateAuth(t, svc, "acc", 300, time.Hour)

	if _, err := svc.AdjustAmount(AdjustRequest{
		IdempotencyKey: "up-over",
		AuthID:         auth.ID,
		NewAmount:      600, // 可用只有 200，增量 300
	}); GetErrorCode(err) != ErrCodeInsufficientFunds {
		t.Fatalf("want insufficient_funds, got %v", err)
	}

	res, err := svc.AdjustAmount(AdjustRequest{
		IdempotencyKey: "up-ok",
		AuthID:         auth.ID,
		NewAmount:      500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Auth.Amount != 500 {
		t.Fatalf("amount not updated: %+v", res.Auth)
	}
	acc := mustAccount(t, svc, "acc")
	if acc.Frozen != 500 || acc.Available() != 0 {
		t.Fatalf("freeze not extended: %+v", acc)
	}
}

func TestAdjust_DownReleasesDifference(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 500, time.Hour)

	if _, err := svc.Capture(CaptureRequest{IdempotencyKey: "c1", AuthID: auth.ID, Amount: 100}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.AdjustAmount(AdjustRequest{
		IdempotencyKey: "down",
		AuthID:         auth.ID,
		NewAmount:      200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Release == nil || res.Release.Amount != 300 || res.Release.Reason != domain.ReleaseAdjustDown {
		t.Fatalf("down-adjust release wrong: %+v", res.Release)
	}
	got, _ := svc.GetPreAuth(auth.ID)
	if got.Amount != 200 || got.Captured != 100 || got.Remaining() != 100 {
		t.Fatalf("auth after down-adjust wrong: %+v", got)
	}
	acc := mustAccount(t, svc, "acc")
	if acc.Frozen != 100 || acc.Balance != 900 {
		t.Fatalf("account after down-adjust wrong: %+v", acc)
	}

	// 不能下调到已占用以下。
	_, err = svc.AdjustAmount(AdjustRequest{IdempotencyKey: "down-below", AuthID: auth.ID, NewAmount: 99})
	if GetErrorCode(err) != ErrCodeAdjustBelowUsed {
		t.Fatalf("want adjust_below_used, got %v", err)
	}
}

func TestAdjust_ConcurrentWithCapture_IsDeterministic(t *testing.T) {
	// 冻结 500；并发发起 5 笔 100 的捕获与一次“下调到 250”。
	// 无论实际交错顺序如何，最终必须满足：
	//   - 捕获成功数*100 <= 250（下调后的剩余）或下调被拒（捕获先占满）；
	//   - 账户冻结额始终等于活跃预授权剩余额；
	//   - 无超额、无负剩余。
	for iter := 0; iter < 50; iter++ {
		svc, _ := newTestService(t)
		seedAccount(t, svc, "acc", 5000)
		auth := mustCreateAuth(t, svc, "acc", 500, time.Hour)

		var wg sync.WaitGroup
		start := make(chan struct{})
		captureOK := make(chan struct{}, 5)
		wg.Add(6)
		go func() {
			defer wg.Done()
			<-start
			_, _ = svc.AdjustAmount(AdjustRequest{
				IdempotencyKey: "adj-" + strconv.Itoa(iter),
				AuthID:         auth.ID,
				NewAmount:      250,
			})
		}()
		for i := 0; i < 5; i++ {
			i := i
			go func() {
				defer wg.Done()
				<-start
				_, err := svc.Capture(CaptureRequest{
					IdempotencyKey: "cap-" + strconv.Itoa(iter) + "-" + strconv.Itoa(i),
					AuthID:         auth.ID,
					Amount:         100,
				})
				if err == nil {
					captureOK <- struct{}{}
				}
			}()
		}
		close(start)
		wg.Wait()
		close(captureOK)

		okCount := len(captureOK)
		got, err := svc.GetPreAuth(auth.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Captured != domain.Money(okCount)*100 {
			t.Fatalf("iter %d captured count mismatch: ok=%d auth=%+v", iter, okCount, got)
		}
		if got.Captured+got.Released > got.Amount {
			t.Fatalf("iter %d invariant broken: %+v", iter, got)
		}
		if got.Remaining() < 0 {
			t.Fatalf("iter %d negative remaining: %+v", iter, got)
		}
		acc := mustAccount(t, svc, "acc")
		if acc.Frozen < 0 || acc.Available() < 0 {
			t.Fatalf("iter %d account wrong: %+v", iter, acc)
		}
		// 不变量：账户总额 = 初始 - 已捕获（未发生退款）；冻结额等于活跃剩余额。
		if acc.Balance != 5000-got.Captured {
			t.Fatalf("iter %d balance wrong: %+v auth=%+v", iter, acc, got)
		}
		if got.Status == domain.AuthActive && acc.Frozen != got.Remaining() {
			t.Fatalf("iter %d frozen mismatch: acc=%+v auth=%+v", iter, acc, got)
		}
	}
}
