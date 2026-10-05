package service

import (
	"context"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

// Service implements the preauth/capture/void/adjust/refund/settlement workflows.
type Service struct {
	db *store.FileStore
}

func New(db *store.FileStore) *Service {
	return &Service{db: db}
}

type CreateAccountRequest struct {
	AccountID string
	Balance   domain.Amount
}

// VoidResult reports a (partial) void operation.
type VoidResult struct {
	VoidID    string
	PreAuthID string
	Amount    domain.Amount
}

type PreAuthRequest struct {
	IdempotencyKey string
	AccountID      string
	PreAuthID      string
	Amount         domain.Amount
	TTL            time.Duration
	Now            time.Time
}

type CaptureRequest struct {
	IdempotencyKey string
	PreAuthID      string
	CaptureID      string
	Amount         domain.Amount
	Now            time.Time
}

type VoidRequest struct {
	IdempotencyKey string
	PreAuthID      string
	VoidID         string
	Amount         domain.Amount // 0 means release all remaining hold
	Now            time.Time
}

type AdjustRequest struct {
	IdempotencyKey string
	PreAuthID      string
	NewAmount      domain.Amount
	Now            time.Time
}

type RefundRequest struct {
	IdempotencyKey string
	PreAuthID      string
	CaptureID      string
	RefundID       string
	Amount         domain.Amount // 0 means refund all capture remaining
	Now            time.Time
}

type SettleRequest struct {
	IdempotencyKey string
	PeriodID       string
	BatchID        string
	Now            time.Time
}

func (s *Service) Settle(ctx context.Context, req SettleRequest) (*domain.SettlementBatch, error) {
	return settle(ctx, s, req)
}
