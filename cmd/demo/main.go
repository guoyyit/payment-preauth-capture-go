package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/service"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

func main() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "preauth-demo-")
	must(err)
	defer os.RemoveAll(dir)

	db, err := store.NewFileStore(filepath.Join(dir, "store.json"))
	must(err)
	svc := service.New(db)

	_, err = svc.CreateAccount(ctx, service.CreateAccountRequest{
		AccountID: "acct-1",
		Balance:   100_000,
	})
	must(err)

	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	_, err = svc.PreAuthorize(ctx, service.PreAuthRequest{
		IdempotencyKey: "pa-1-key",
		AccountID:      "acct-1",
		PreAuthID:      "pa-1",
		Amount:         60_000,
		TTL:            time.Hour,
		Now:            now,
	})
	must(err)

	// 60 concurrent captures of 10 each: only 6 can fit into the 600 hold.
	var wg sync.WaitGroup
	var ok, rejected int64
	var mu sync.Mutex
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := svc.Capture(ctx, service.CaptureRequest{
				IdempotencyKey: fmt.Sprintf("cap-key-%d", i),
				PreAuthID:      "pa-1",
				CaptureID:      fmt.Sprintf("cap-%d", i),
				Amount:         10_000,
				Now:            now.Add(time.Minute),
			})
			mu.Lock()
			if err == nil {
				ok++
			} else {
				rejected++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	batch, err := svc.Settle(ctx, service.SettleRequest{
		IdempotencyKey: "settle-1",
		PeriodID:       "2026-10-05",
		BatchID:        "batch-1",
		Now:            now.Add(2 * time.Hour),
	})
	must(err)

	pa, err := svc.GetPreAuth(ctx, "pa-1")
	must(err)
	acct, err := svc.GetAccount(ctx, "acct-1")
	must(err)

	fmt.Printf("captures accepted=%d rejected=%d\n", ok, rejected)
	fmt.Printf("preauth status=%s held=%d captured=%d voided=%d\n",
		pa.Status, pa.HeldAmount, pa.CapturedAmount, pa.VoidedAmount)
	fmt.Printf("account balance=%d reserved=%d\n", acct.Balance, acct.Reserved)
	fmt.Printf("settlement captures=%d total=%d\n", len(batch.CaptureIDs), batch.TotalAmount)

	// Expire the remaining hold.
	n, err := svc.SweepExpired(ctx, now.Add(3*time.Hour))
	must(err)
	pa, _ = svc.GetPreAuth(ctx, "pa-1")
	acct, _ = svc.GetAccount(ctx, "acct-1")
	fmt.Printf("swept=%d preauth=%s account balance=%d reserved=%d\n",
		n, pa.Status, acct.Balance, acct.Reserved)

	events, _ := svc.ListEvents(ctx)
	var autoReleases int
	for _, e := range events {
		if e.Type == domain.EventAutoReleased {
			autoReleases++
		}
	}
	fmt.Printf("audit events=%d auto-release events=%d\n", len(events), autoReleases)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}
