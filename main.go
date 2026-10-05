// Command payment-preauth-capture-go 是一个纯本地的预授权与捕获结算演示程序：
// 账户与请求数据全部在本地构造，不依赖任何外部服务。
//
// 重复运行本程序会从 data/snapshot.json 恢复：首次运行业务流程，
// 再次运行只展示重启后读到的状态与幂等重放，不产生二次副作用。
package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/internal/service"
)

const (
	demoAccountID = "acc-1"
	demoAuthID    = "auth-demo-1"
	demoCaptureID = "cap-demo-1"
)

func main() {
	path := filepath.Join("data", "snapshot.json")
	svc, err := service.New(service.Config{SnapshotPath: path})
	if err != nil {
		panic(err)
	}
	defer svc.Close()

	if service.GetErrorCode(func() error {
		_, e := svc.GetAccount(demoAccountID)
		return e
	}()) == service.ErrCodeNotFound {
		runFresh(svc)
		return
	}
	runReplay(svc)
}

func runFresh(svc *service.Service) {
	if _, err := svc.CreateAccount(demoAccountID, 100000); err != nil {
		panic(err)
	}
	if _, err := svc.CreatePreAuth(service.CreateAuthRequest{
		IdempotencyKey: "create-auth-1",
		AccountID:      demoAccountID,
		AuthID:         demoAuthID,
		Amount:         30000,
		TTLMillis:      int64(time.Hour / time.Millisecond),
	}); err != nil {
		panic(err)
	}
	if _, err := svc.Capture(service.CaptureRequest{
		IdempotencyKey: "capture-1",
		AuthID:         demoAuthID,
		CaptureID:      demoCaptureID,
		Amount:         10000,
	}); err != nil {
		panic(err)
	}
	if _, err := svc.AdjustAmount(service.AdjustRequest{
		IdempotencyKey: "adjust-1",
		AuthID:         demoAuthID,
		NewAmount:      20000,
	}); err != nil {
		panic(err)
	}
	if _, err := svc.Refund(service.RefundRequest{
		IdempotencyKey: "refund-1",
		CaptureID:      demoCaptureID,
		Amount:         2000,
	}); err != nil {
		panic(err)
	}
	now := time.Now()
	batch, err := svc.SettleCycle(service.SettlementRequest{
		IdempotencyKey: "settle-1",
		PeriodStart:    now.Add(-time.Minute).UnixMilli(),
		PeriodEnd:      now.Add(time.Minute).UnixMilli(),
	})
	if err != nil {
		panic(err)
	}
	acc, _ := svc.GetAccount(demoAccountID)
	fmt.Println("== 首次运行 ==")
	fmt.Printf("account balance=%d frozen=%d available=%d\n", acc.Balance, acc.Frozen, acc.Available())
	fmt.Printf("settled batch=%s total=%d captures=%d\n", batch.ID, batch.TotalAmount, len(batch.CaptureIDs))
	fmt.Println("状态已写入 data/snapshot.json，再次运行可看到恢复与幂等重放。")
}

func runReplay(svc *service.Service) {
	acc, _ := svc.GetAccount(demoAccountID)
	auth, _ := svc.GetPreAuth(demoAuthID)
	batches := svc.ListBatches()
	replay, err := svc.Capture(service.CaptureRequest{
		IdempotencyKey: "capture-1",
		AuthID:         demoAuthID,
		CaptureID:      demoCaptureID,
		Amount:         10000,
	})
	fmt.Println("== 重启恢复后 ==")
	fmt.Printf("account balance=%d frozen=%d available=%d batches=%d\n",
		acc.Balance, acc.Frozen, acc.Available(), len(batches))
	fmt.Printf("auth captured=%d released=%d amount=%d status=%s\n",
		auth.Captured, auth.Released, auth.Amount, auth.Status)
	if err != nil {
		fmt.Printf("replay error (unexpected): %v\n", err)
		return
	}
	fmt.Printf("idempotent replay -> capture=%s amount=%d (首次结果，无二次副作用)\n",
		replay.Capture.ID, replay.Capture.Amount)
}
