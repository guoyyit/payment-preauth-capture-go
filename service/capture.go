package service

import (
	"context"
	"fmt"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

func (s *Service) Capture(ctx context.Context, req CaptureRequest) (*domain.Capture, error) {
	if err := validatePositive(req.Amount); err != nil {
		return nil, err
	}
	return runIdempotent(
		ctx, s.db, req.IdempotencyKey, domain.OpCapture, req.PreAuthID, req.Now,
		func(repo store.Repository, rec *domain.IdempotentRecord) (*domain.Capture, string, error) {
			preauth, account, err := loadActivePreAuth(ctx, repo, req.PreAuthID, req.Now)
			if err != nil {
				return nil, "", err
			}
			if req.Amount > preauth.RemainingHeld() {
				return nil, "", fmt.Errorf("%w: capture %d > remaining %d",
					domain.ErrAmountExceedsHeld, req.Amount, preauth.RemainingHeld())
			}
			capture := &domain.Capture{
				ID:        req.CaptureID,
				PreAuthID: preauth.ID,
				AccountID: account.ID,
				Amount:    req.Amount,
				Status:    domain.CapturePendingRefund,
				CreatedAt: req.Now,
			}
			// Funds move from "reserved" to "captured": they leave the account
			// (merchant payable) and the hold shrinks by the same amount.
			account.Balance -= req.Amount
			account.Reserved -= req.Amount
			account.Version++

			preauth.CapturedAmount += req.Amount
			preauth.Version++
			recomputeStatus(preauth)

			if err := repo.PutCapture(ctx, capture); err != nil {
				return nil, "", err
			}
			if err := repo.PutPreAuth(ctx, preauth); err != nil {
				return nil, "", err
			}
			if _, err := repo.AppendEvent(ctx, &domain.Event{
				Type:      domain.EventCaptured,
				PreAuthID: preauth.ID,
				AccountID: account.ID,
				RefID:     capture.ID,
				Amount:    req.Amount,
				CreatedAt: req.Now,
			}); err != nil {
				return nil, "", err
			}
			return capture, capture.ID, nil
		},
		func(repo store.Repository, refID string) (*domain.Capture, error) {
			return repo.GetCapture(ctx, refID)
		},
	)
}
