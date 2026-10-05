package service

import (
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/internal/domain"
)

func TestCrashRecovery_StateSurvivesRestart(t *testing.T) {
	path := snapshotPath(t)
	clock := newFakeClock()

	svc := reopenPersistentService(t, path, clock)
	if _, err := svc.CreateAccount("acc", 10000); err != nil {
		t.Fatal(err)
	}
	auth, err := svc.CreatePreAuth(CreateAuthRequest{
		IdempotencyKey: "auth1",
		AccountID:      "acc",
		Amount:         5000,
		TTLMillis:      int64(time.Hour / time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	capRes, err := svc.Capture(CaptureRequest{IdempotencyKey: "cap1", AuthID: auth.ID, Amount: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Void(VoidRequest{IdempotencyKey: "void1", AuthID: auth.ID, Amount: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refund(RefundRequest{IdempotencyKey: "ref1", CaptureID: capRes.Capture.ID, Amount: 500}); err != nil {
		t.Fatal(err)
	}
	start := clock.now().Add(-time.Hour).UnixMilli()
	end := clock.now().Add(time.Hour).UnixMilli()
	batch, err := svc.SettleCycle(SettlementRequest{
		IdempotencyKey: "batch1",
		PeriodStart:    start,
		PeriodEnd:      end,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 重启：全部状态必须与崩溃前一致。
	svc2 := reopenPersistentService(t, path, clock)
	acc, err := svc2.GetAccount("acc")
	if err != nil {
		t.Fatal(err)
	}
	// 初始 10000；捕获 2000 扣走，退款 500 退回 -> 8500；
	// 冻结剩余 = 5000-2000-1000(撤销) = 2000（下调未发生，Released=1000）。
	if acc.Balance != 8500 || acc.Frozen != 2000 {
		t.Fatalf("account mismatch after restart: %+v", acc)
	}
	a2, err := svc2.GetPreAuth(auth.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a2.Captured != 2000 || a2.Released != 1000 || a2.Status != domain.AuthActive {
		t.Fatalf("auth mismatch after restart: %+v", a2)
	}
	c2, err := svc2.GetCapture(capRes.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Refunded != 500 || !c2.Settled || c2.BatchID != batch.ID {
		t.Fatalf("capture mismatch after restart: %+v", c2)
	}
	b2, err := svc2.GetBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b2.Status != domain.BatchConfirmed || b2.TotalAmount != 1500 {
		t.Fatalf("batch mismatch after restart: %+v", b2)
	}

	// 幂等记录仍在：重复提交返回首次结果，无二次副作用。
	replay, err := svc2.Capture(CaptureRequest{IdempotencyKey: "cap1", AuthID: auth.ID, Amount: 2000})
	if err != nil || replay.Capture.ID != capRes.Capture.ID {
		t.Fatalf("idempotent replay after restart failed: %+v err=%v", replay, err)
	}
	a3, _ := svc2.GetPreAuth(auth.ID)
	if a3.Captured != 2000 {
		t.Fatalf("replay must not double capture: %+v", a3)
	}
}

func TestCrashRecovery_OpenBatchRolledBack(t *testing.T) {
	path := snapshotPath(t)
	clock := newFakeClock()

	svc := reopenPersistentService(t, path, clock)
	if _, err := svc.CreateAccount("acc", 5000); err != nil {
		t.Fatal(err)
	}
	auth := mustCreateAuth(t, svc, "acc", 2000, 24*time.Hour)
	capRes, err := svc.Capture(CaptureRequest{IdempotencyKey: "cap1", AuthID: auth.ID, Amount: 800})
	if err != nil {
		t.Fatal(err)
	}
	start := clock.now().Add(-time.Hour).UnixMilli()
	end := clock.now().Add(time.Hour).UnixMilli()
	batch, err := svc.GenerateBatch(SettlementRequest{
		IdempotencyKey: "gen1",
		PeriodStart:    start,
		PeriodEnd:      end,
	})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != domain.BatchOpen {
		t.Fatalf("precondition: want open batch")
	}

	// 崩溃发生在确认之前：重启后 open 批次按策略回滚，捕获可重新结算/退款。
	svc2 := reopenPersistentService(t, path, clock)
	if _, err := svc2.GetBatch(batch.ID); GetErrorCode(err) != ErrCodeNotFound {
		t.Fatalf("open batch must be rolled back, got err=%v", err)
	}
	c, err := svc2.GetCapture(capRes.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.BatchID != "" || c.Settled {
		t.Fatalf("capture must be released from rolled-back batch: %+v", c)
	}
	// 回滚后可退款。
	if _, err := svc2.Refund(RefundRequest{
		IdempotencyKey: "ref-after-rollback",
		CaptureID:      capRes.Capture.ID,
		Amount:         100,
	}); err != nil {
		t.Fatalf("refund after rollback must succeed: %v", err)
	}
	// 也可重新发起结算（净额 700）。
	reBatch, err := svc2.SettleCycle(SettlementRequest{
		IdempotencyKey: "regen",
		PeriodStart:    start,
		PeriodEnd:      end,
	})
	if err != nil || reBatch.TotalAmount != 700 {
		t.Fatalf("re-settle after rollback wrong: %+v err=%v", reBatch, err)
	}
}
