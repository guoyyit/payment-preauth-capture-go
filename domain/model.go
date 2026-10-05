package domain

import "time"

// Amount unit: cents (smallest integer unit, no floating point).
type Amount = int64

// Account is the local ledger account.
type Account struct {
	ID       string `json:"id"`
	Balance  Amount `json:"balance"`  // total owned funds
	Reserved Amount `json:"reserved"` // frozen by active preauths
	Version  int64  `json:"version"`
}

// Available balance that can still be frozen.
func (a *Account) Available() Amount {
	return a.Balance - a.Reserved
}

type PreAuthStatus string

const (
	PreAuthActive    PreAuthStatus = "ACTIVE"
	PreAuthCaptured  PreAuthStatus = "CAPTURED"           // fully captured, possibly with remainder released
	PreAuthVoided    PreAuthStatus = "VOIDED"             // fully voided/released without capture
	PreAuthPartially PreAuthStatus = "PARTIALLY_CAPTURED" // captured > 0, remainder still held
	PreAuthReleased  PreAuthStatus = "RELEASED"           // expired, remainder auto-released
)

// PreAuth freezes HeldAmount on an account until ExpiresAt.
type PreAuth struct {
	ID             string        `json:"id"`
	AccountID      string        `json:"account_id"`
	OriginalAmount Amount        `json:"original_amount"`
	HeldAmount     Amount        `json:"held_amount"` // current held amount after adjustments
	CapturedAmount Amount        `json:"captured_amount"`
	VoidedAmount   Amount        `json:"voided_amount"`
	RefundedAmount Amount        `json:"refunded_amount"`
	Status         PreAuthStatus `json:"status"`
	CreatedAt      time.Time     `json:"created_at"`
	ExpiresAt      time.Time     `json:"expires_at"`
	AdjustedAt     time.Time     `json:"adjusted_at"`
	Version        int64         `json:"version"`
}

// RemainingHeld returns frozen amount not yet captured or voided.
func (p *PreAuth) RemainingHeld() Amount {
	return p.HeldAmount - p.CapturedAmount - p.VoidedAmount
}

type OperationType string

const (
	OpPreAuth OperationType = "PREAUTH"
	OpCapture OperationType = "CAPTURE"
	OpVoid    OperationType = "VOID"
	OpAdjust  OperationType = "ADJUST"
	OpRefund  OperationType = "REFUND"
	OpSettle  OperationType = "SETTLE"
)

type OpStatus string

const (
	OpCompleted OpStatus = "COMPLETED"
	OpFailed    OpStatus = "FAILED"
)

// IdempotentRecord stores the first outcome of an operation keyed by IdempotencyKey.
type IdempotentRecord struct {
	Key         string        `json:"key"`
	OpType      OperationType `json:"op_type"`
	PreAuthID   string        `json:"preauth_id"`
	Status      OpStatus      `json:"status"`
	ResultRefID string        `json:"result_ref_id"` // capture/void/refund/batch id
	ErrCode     string        `json:"err_code,omitempty"`
	ErrMessage  string        `json:"err_message,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
}

type EventType string

const (
	EventPreAuthCreated  EventType = "PREAUTH_CREATED"
	EventPreAuthAdjusted EventType = "PREAUTH_ADJUSTED"
	EventCaptured        EventType = "CAPTURED"
	EventVoided          EventType = "VOIDED"
	EventRefunded        EventType = "REFUNDED"
	EventAutoReleased    EventType = "AUTO_RELEASED"
	EventSettled         EventType = "SETTLED"
)

// Event is an append-only audit/ledger event.
type Event struct {
	Seq       int64          `json:"seq"`
	Type      EventType      `json:"type"`
	PreAuthID string         `json:"preauth_id"`
	AccountID string         `json:"account_id"`
	RefID     string         `json:"ref_id"`
	Amount    Amount         `json:"amount"`
	Detail    map[string]any `json:"detail,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type CaptureStatus string

const (
	CapturePendingRefund CaptureStatus = "CAPTURED"
	CaptureRefunded      CaptureStatus = "REFUNDED"
)

// Capture is one capture record under a preauth.
type Capture struct {
	ID             string        `json:"id"`
	PreAuthID      string        `json:"preauth_id"`
	AccountID      string        `json:"account_id"`
	Amount         Amount        `json:"amount"`
	RefundedAmount Amount        `json:"refunded_amount"`
	RefundIDs      []string      `json:"refund_ids,omitempty"`
	Status         CaptureStatus `json:"status"`
	SettledBatchID string        `json:"settled_batch_id,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	Version        int64         `json:"version"`
}

// Refund is one refund record against a capture.
type Refund struct {
	ID        string    `json:"id"`
	CaptureID string    `json:"capture_id"`
	PreAuthID string    `json:"preauth_id"`
	Amount    Amount    `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}

// VoidRecord is one void/release record against a preauth.
type VoidRecord struct {
	ID        string    `json:"id"`
	PreAuthID string    `json:"preauth_id"`
	AccountID string    `json:"account_id"`
	Amount    Amount    `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}

type BatchStatus string

const (
	BatchBuilding  BatchStatus = "BUILDING"
	BatchCompleted BatchStatus = "COMPLETED"
)

// SettlementBatch aggregates captures settled in one cycle.
type SettlementBatch struct {
	ID           string      `json:"id"`
	PeriodID     string      `json:"period_id"`
	GrossAmount  Amount      `json:"gross_amount"`  // captured at batch time
	RefundAmount Amount      `json:"refund_amount"` // post-batch refunds applied here
	TotalAmount  Amount      `json:"total_amount"`  // net = gross - refund
	CaptureIDs   []string    `json:"capture_ids"`
	RefundIDs    []string    `json:"refund_ids,omitempty"`
	Status       BatchStatus `json:"status"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}
