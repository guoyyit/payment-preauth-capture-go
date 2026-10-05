package service

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/internal/domain"
)

func seedAccount(t *testing.T, svc *Service, id string, balance domain.Money) {
	t.Helper()
	if _, err := svc.CreateAccount(id, balance); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
}

func TestCreatePreAuth_FreezesFunds(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 1000)

	auth := mustCreateAuth(t, svc, "acc", 300, time.Hour)
	if auth.Status != domain.AuthActive || auth.Amount != 300 {
		t.Fatalf("unexpected auth: %+v", auth)
	}
	acc := mustAccount(t, svc, "acc")
	if acc.Frozen != 300 || acc.Available() != 700 {
		t.Fatalf("account not frozen correctly: %+v", acc)
	}
}

func TestCreatePreAuth_InsufficientFunds(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 100)

	_, err := svc.CreatePreAuth(CreateAuthRequest{
		IdempotencyKey: "k1",
		AccountID:      "acc",
		Amount:         200,
		TTLMillis:      60000,
	})
	if GetErrorCode(err) != ErrCodeInsufficientFunds {
		t.Fatalf("want insufficient_funds, got %v", err)
	}
	acc := mustAccount(t, svc, "acc")
	if acc.Frozen != 0 {
		t.Fatalf("failed auth must not freeze funds: %+v", acc)
	}
}

func TestCapture_PartialAndOverrunRejected(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 500, time.Hour)

	res, err := svc.Capture(CaptureRequest{IdempotencyKey: "c1", AuthID: auth.ID, Amount: 200})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if res.Capture.Refundable() != 200 {
		t.Fatalf("refundable mismatch: %d", res.Capture.Refundable())
	}
	if _, err := svc.Capture(CaptureRequest{IdempotencyKey: "c2", AuthID: auth.ID, Amount: 400}); err == nil {
		t.Fatal("expected overrun capture to be rejected")
	} else if GetErrorCode(err) != ErrCodeExceedRemaining {
		t.Fatalf("want exceed_remaining, got %v", err)
	}

	got, err := svc.GetPreAuth(auth.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Captured != 200 || got.Remaining() != 300 {
		t.Fatalf("auth totals wrong: %+v", got)
	}
	acc := mustAccount(t, svc, "acc")
	if acc.Balance != 800 || acc.Frozen != 300 {
		t.Fatalf("account state wrong: %+v", acc)
	}
}

func TestVoid_PartialRelease(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 500, time.Hour)

	if _, err := svc.Capture(CaptureRequest{IdempotencyKey: "c1", AuthID: auth.ID, Amount: 200}); err != nil {
		t.Fatal(err)
	}
	voidRes, err := svc.Void(VoidRequest{IdempotencyKey: "v1", AuthID: auth.ID, Amount: 100})
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if voidRes.Release.Reason != domain.ReleaseVoid {
		t.Fatalf("want void reason, got %s", voidRes.Release.Reason)
	}
	releases, err := svc.ListReleases(auth.ID)
	if err != nil || len(releases) != 1 || releases[0].Amount != 100 {
		t.Fatalf("release trail wrong: %v %v", releases, err)
	}
	if _, err := svc.Void(VoidRequest{IdempotencyKey: "v2", AuthID: auth.ID, Amount: 201}); err == nil {
		t.Fatal("over-void must be rejected")
	}
	got, _ := svc.GetPreAuth(auth.ID)
	if got.Remaining() != 200 {
		t.Fatalf("remaining wrong: %d", got.Remaining())
	}
}

func TestConcurrentCapture_NeverOverruns(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 10000)
	auth := mustCreateAuth(t, svc, "acc", 1000, time.Hour)

	const workers = 20
	var wg sync.WaitGroup
	results := make(chan error, workers)
	wg.Add(workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Capture(CaptureRequest{
				IdempotencyKey: uniqueKey("cc", i),
				AuthID:         auth.ID,
				Amount:         100,
			})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	success, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case GetErrorCode(err) == ErrCodeExceedRemaining:
			rejected++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 10 || rejected != 10 {
		t.Fatalf("want 10 success / 10 rejected, got %d / %d", success, rejected)
	}
	got, _ := svc.GetPreAuth(auth.ID)
	if got.Captured != 1000 || got.Status != domain.AuthClosed {
		t.Fatalf("auth state wrong: captured=%d status=%s", got.Captured, got.Status)
	}
	caps, err := svc.ListCaptures(auth.ID)
	if err != nil || len(caps) != 10 {
		t.Fatalf("want 10 capture records, got %d err=%v", len(caps), err)
	}
}

func TestConcurrentCaptureAndVoid_NoDoubleRelease(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 10000)
	auth := mustCreateAuth(t, svc, "acc", 600, time.Hour)

	const perKind = 10
	var wg sync.WaitGroup
	wg.Add(perKind * 2)
	start := make(chan struct{})
	errs := make(chan error, perKind*2)
	for i := 0; i < perKind; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Capture(CaptureRequest{IdempotencyKey: uniqueKey("cap", i), AuthID: auth.ID, Amount: 100})
			errs <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Void(VoidRequest{IdempotencyKey: uniqueKey("void", i), AuthID: auth.ID, Amount: 100})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if GetErrorCode(err) != ErrCodeExceedRemaining {
			t.Fatalf("unexpected err: %v", err)
		}
	}
	got, _ := svc.GetPreAuth(auth.ID)
	if got.Captured+got.Released > got.Amount || got.Remaining() < 0 {
		t.Fatalf("invariant broken: %+v", got)
	}
	if success*100 != int(got.Amount) {
		t.Fatalf("exactly the frozen amount must be consumed: success=%d auth=%+v", success, got)
	}
}

func uniqueKey(prefix string, i int) string {
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(i)
}
