package service

import (
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/internal/domain"
)

func TestExpiry_AutoReleaseOnAccess(t *testing.T) {
	svc, clock := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 400, time.Hour)

	clock.advance(30 * time.Minute)
	if _, err := svc.Capture(CaptureRequest{IdempotencyKey: "before", AuthID: auth.ID, Amount: 100}); err != nil {
		t.Fatal(err)
	}

	clock.advance(31 * time.Minute)
	_, err := svc.Capture(CaptureRequest{IdempotencyKey: "after", AuthID: auth.ID, Amount: 1})
	if GetErrorCode(err) != ErrCodeAuthExpired {
		t.Fatalf("want auth_expired, got %v", err)
	}

	got, _ := svc.GetPreAuth(auth.ID)
	if got.Status != domain.AuthClosed || got.Released != 300 {
		t.Fatalf("expected remaining auto-released, got %+v", got)
	}
	releases, _ := svc.ListReleases(auth.ID)
	var expiryEvent *domain.ReleaseEvent
	for _, e := range releases {
		if e.Reason == domain.ReleaseExpiry {
			expiryEvent = e
		}
	}
	if expiryEvent == nil || expiryEvent.Amount != 300 {
		t.Fatalf("expiry release event missing/wrong: %+v", releases)
	}
	acc := mustAccount(t, svc, "acc")
	if acc.Frozen != 0 || acc.Balance != 900 {
		t.Fatalf("account not restored: %+v", acc)
	}
}

func TestExpiry_SweepAndIdempotency(t *testing.T) {
	svc, clock := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth1 := mustCreateAuth(t, svc, "acc", 100, time.Hour)
	auth2 := mustCreateAuth(t, svc, "acc", 200, 2*time.Hour)

	clock.advance(90 * time.Minute)
	n, err := svc.SweepExpired()
	if err != nil || n != 1 {
		t.Fatalf("sweep want 1 release, got n=%d err=%v", n, err)
	}
	a1, _ := svc.GetPreAuth(auth1.ID)
	a2, _ := svc.GetPreAuth(auth2.ID)
	if a1.Status != domain.AuthClosed || a1.Released != 100 {
		t.Fatalf("auth1 should be released: %+v", a1)
	}
	if a2.Status != domain.AuthActive {
		t.Fatalf("auth2 should still be active: %+v", a2)
	}

	// 重复扫描不产生二次释放。
	n2, err := svc.SweepExpired()
	if err != nil || n2 != 0 {
		t.Fatalf("second sweep must be a no-op, got n=%d err=%v", n2, err)
	}
	rel1, _ := svc.ListReleases(auth1.ID)
	if len(rel1) != 1 {
		t.Fatalf("exactly one expiry release allowed, got %d", len(rel1))
	}
	acc := mustAccount(t, svc, "acc")
	if acc.Frozen != 200 {
		t.Fatalf("frozen should reflect only active auth: %+v", acc)
	}
}

func TestExpiry_FullyCapturedAuthNoExpiryEvent(t *testing.T) {
	svc, clock := newTestService(t)
	seedAccount(t, svc, "acc", 500)
	auth := mustCreateAuth(t, svc, "acc", 100, time.Hour)

	if _, err := svc.Capture(CaptureRequest{IdempotencyKey: "full", AuthID: auth.ID, Amount: 100}); err != nil {
		t.Fatal(err)
	}
	clock.advance(2 * time.Hour)
	n, err := svc.SweepExpired()
	if err != nil || n != 0 {
		t.Fatalf("nothing to release, got n=%d err=%v", n, err)
	}
	rels, _ := svc.ListReleases(auth.ID)
	if len(rels) != 0 {
		t.Fatalf("no release event expected, got %d", len(rels))
	}
}
