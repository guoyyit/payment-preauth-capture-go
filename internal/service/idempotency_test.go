package service

import (
	"testing"
	"time"
)

func TestIdempotency_CaptureReplayNoDoubleSideEffect(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 500, time.Hour)

	req := CaptureRequest{IdempotencyKey: "same-key", AuthID: auth.ID, Amount: 200}
	first, err := svc.Capture(req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Capture(req)
	if err != nil {
		t.Fatalf("replay must replay success: %v", err)
	}
	if first.Capture.ID != second.Capture.ID {
		t.Fatalf("replay must return the same capture: %s vs %s", first.Capture.ID, second.Capture.ID)
	}
	caps, _ := svc.ListCaptures(auth.ID)
	if len(caps) != 1 {
		t.Fatalf("replay must not create a second capture, got %d", len(caps))
	}
	got, _ := svc.GetPreAuth(auth.ID)
	if got.Captured != 200 {
		t.Fatalf("captured must stay 200, got %d", got.Captured)
	}
}

func TestIdempotency_ReplaysFirstError(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 100, time.Hour)

	req := CaptureRequest{IdempotencyKey: "over", AuthID: auth.ID, Amount: 200}
	_, err1 := svc.Capture(req)
	_, err2 := svc.Capture(req)
	if GetErrorCode(err1) != ErrCodeExceedRemaining || GetErrorCode(err2) != ErrCodeExceedRemaining {
		t.Fatalf("both calls must report the same error: %v %v", err1, err2)
	}
	got, _ := svc.GetPreAuth(auth.ID)
	if got.Captured != 0 {
		t.Fatalf("failed op must have no side effect: %+v", got)
	}
}

func TestIdempotency_KeyReuseDifferentParamsRejected(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 500, time.Hour)

	if _, err := svc.Capture(CaptureRequest{IdempotencyKey: "k", AuthID: auth.ID, Amount: 100}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Capture(CaptureRequest{IdempotencyKey: "k", AuthID: auth.ID, Amount: 200})
	if GetErrorCode(err) != ErrCodeDuplicateMismatch {
		t.Fatalf("same key different params must be rejected: %v", err)
	}
}

func TestIdempotency_KeyReuseAcrossKindsRejected(t *testing.T) {
	svc, _ := newTestService(t)
	seedAccount(t, svc, "acc", 1000)
	auth := mustCreateAuth(t, svc, "acc", 500, time.Hour)

	if _, err := svc.Void(VoidRequest{IdempotencyKey: "shared", AuthID: auth.ID, Amount: 10}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Capture(CaptureRequest{IdempotencyKey: "shared", AuthID: auth.ID, Amount: 10})
	if GetErrorCode(err) != ErrCodeDuplicateMismatch {
		t.Fatalf("key reuse across kinds must be rejected: %v", err)
	}
}
