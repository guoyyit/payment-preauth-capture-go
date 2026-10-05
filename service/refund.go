package service

import (
	"context"
	"fmt"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

func (s *Service) Refund(ctx context.Context, req RefundRequest) (*domain.Refund, error) {
	return runIdempotent(
		ctx, s.db, req.IdempotencyKey, domain.OpRefund, req.PreAuthID, req.Now,
		func(repo store.Repository, rec *domain.IdempotentRecord) (*domain.Refund, string, error) {
			preauth, err := repo.GetPreAuth(ctx, req.PreAuthID)
			if err != nil {
				return nil, "", err
			}
			account, err := repo.GetAccount(ctx, preauth.AccountID)
			if err != nil {
				return nil, "", err
			}
			capture, err := repo.GetCapture(ctx, req.CaptureID)
			if err != nil {
				return nil, "", err
			}
			if capture.PreAuthID != preauth.ID {
				return nil, "", fmt.Errorf("%w: capture does not belong to preauth",
					domain.ErrInvalidState)
			}
			refundable := capture.Amount - capture.RefundedAmount
			amount := req.Amount
			if amount == 0 {
				amount = refundable
			}
			if amount <= 0 {
				return nil, "", domain.ErrInvalidAmount
			}
			if amount > refundable {
				return nil, "", fmt.Errorf("%w: refund %d > refundable %d",
					domain.ErrAmountExceedsCaptured, amount, refundable)
			}

			// Refunded money returns to the customer: balance is restored.
			account.Balance += amount
			account.Version++

			capture.RefundedAmount += amount
			capture.RefundIDs = append(capture.RefundIDs, req.RefundID)
			capture.Version++
			if capture.RefundedAmount == capture.Amount {
				capture.Status = domain.CaptureRefunded
			}

			preauth.RefundedAmount += amount
			preauth.Version++

			refund := &domain.Refund{
				ID:        req.RefundID,
				CaptureID: capture.ID,
				PreAuthID: preauth.ID,
				Amount:    amount,
				CreatedAt: req.Now,
			}
			// If the capture was already in a completed batch, the refund
			// reduces that batch's net total so settlement and refunds stay
			// consistent regardless of their order.
			if capture.SettledBatchID != "" {
				batch, err := repo.GetBatch(ctx, capture.SettledBatchID)
				if err != nil {
					return nil, "", err
				}
				batch.RefundAmount += amount
				batch.TotalAmount = batch.GrossAmount - batch.RefundAmount
				batch.RefundIDs = append(batch.RefundIDs, refund.ID)
				batch.UpdatedAt = req.Now
				if err := repo.PutBatch(ctx, batch); err != nil {
					return nil, "", err
				}
			}
			if err := repo.PutRefund(ctx, refund); err != nil {
				return nil, "", err
			}
			if err := repo.PutCapture(ctx, capture); err != nil {
				return nil, "", err
			}
			if err := repo.PutPreAuth(ctx, preauth); err != nil {
				return nil, "", err
			}
			if _, err := repo.AppendEvent(ctx, &domain.Event{
				Type:      domain.EventRefunded,
				PreAuthID: preauth.ID,
				AccountID: account.ID,
				RefID:     refund.ID,
				Amount:    amount,
				Detail: map[string]any{
					"capture_id": capture.ID,
				},
				CreatedAt: req.Now,
			}); err != nil {
				return nil, "", err
			}
			return refund, refund.ID, nil
		},
		func(repo store.Repository, refID string) (*domain.Refund, error) {
			return repo.GetRefund(ctx, refID)
		},
	)
}
