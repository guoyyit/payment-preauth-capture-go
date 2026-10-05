package service

import (
	"testing"
	"time"
)

func TestStateMachine_ClosedAuthRejectsAllOperations(t *testing.T) {
	svc, clock := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 100, time.Hour)

	// 全部捕获 -> 额度用尽，预授权关闭。
	capRes, err := svc.Capture(CaptureRequest{IdempotencyKey: "full", AuthID: auth.ID, Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetPreAuth(auth.ID)
	if got.Status != "closed" {
		t.Fatalf("precondition: auth should be closed")
	}

	if _, err := svc.Capture(CaptureRequest{IdempotencyKey: "x1", AuthID: auth.ID, Amount: 1}); GetErrorCode(err) != ErrCodeExceedRemaining {
		t.Fatalf("capture on consumed auth: %v", err)
	}
	if _, err := svc.Void(VoidRequest{IdempotencyKey: "x2", AuthID: auth.ID, Amount: 1}); GetErrorCode(err) != ErrCodeExceedRemaining {
		t.Fatalf("void on consumed auth: %v", err)
	}
	if _, err := svc.AdjustAmount(AdjustRequest{IdempotencyKey: "x3", AuthID: auth.ID, NewAmount: 200}); GetErrorCode(err) != ErrCodeExceedRemaining {
		t.Fatalf("adjust on consumed auth: %v", err)
	}

	// 到期的预授权返回专门错误码 auth_expired。
	clock.advance(2 * time.Hour)
	auth2 := mustCreateAuth(t, svc, "acc", 50, time.Hour)
	clock.advance(2 * time.Hour)
	if _, err := svc.Void(VoidRequest{IdempotencyKey: "x4", AuthID: auth2.ID, Amount: 1}); GetErrorCode(err) != ErrCodeAuthExpired {
		t.Fatalf("void on expired auth: %v", err)
	}

	// 已关闭预授权上的捕获仍可退款（退款针对捕获而非预授权状态）。
	if _, err := svc.Refund(RefundRequest{IdempotencyKey: "x5", CaptureID: capRes.Capture.ID, Amount: 50}); err != nil {
		t.Fatalf("refund on captured amount must work after auth close: %v", err)
	}
}

func TestStateMachine_NotFoundCodes(t *testing.T) {
	svc, _ := newTestService(t)
	cases := []struct {
		name string
		do   func() error
	}{
		{"unknown auth capture", func() error {
			_, e := svc.Capture(CaptureRequest{IdempotencyKey: "a", AuthID: "nope", Amount: 1})
			return e
		}},
		{"unknown auth void", func() error {
			_, e := svc.Void(VoidRequest{IdempotencyKey: "b", AuthID: "nope", Amount: 1})
			return e
		}},
		{"unknown auth adjust", func() error {
			_, e := svc.AdjustAmount(AdjustRequest{IdempotencyKey: "c", AuthID: "nope", NewAmount: 1})
			return e
		}},
		{"unknown batch confirm", func() error {
			_, e := svc.ConfirmBatch(ConfirmBatchRequest{IdempotencyKey: "d", BatchID: "nope"})
			return e
		}},
	}
	for _, tc := range cases {
		if GetErrorCode(tc.do()) != ErrCodeNotFound {
			t.Fatalf("%s: want not_found", tc.name)
		}
	}
}

func TestStateMachine_InvalidAmounts(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 100)

	if _, err := svc.CreatePreAuth(CreateAuthRequest{IdempotencyKey: "a", AccountID: "acc", Amount: 0, TTLMillis: 1}); GetErrorCode(err) != ErrCodeInvalidAmount {
		t.Fatalf("zero amount: %v", err)
	}
	if _, err := svc.CreatePreAuth(CreateAuthRequest{IdempotencyKey: "b", AccountID: "acc", Amount: 10, TTLMillis: 0}); GetErrorCode(err) != ErrCodeInvalidAmount {
		t.Fatalf("zero ttl: %v", err)
	}
	if _, err := svc.CreateAccount("neg", -1); GetErrorCode(err) != ErrCodeInvalidAmount {
		t.Fatalf("negative balance: %v", err)
	}
}
