package service

import (
	"context"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

func (s *Service) Void(ctx context.Context, req VoidRequest) (*VoidResult, error) {
	return runIdempotent(
		ctx, s.db, req.IdempotencyKey, domain.OpVoid, req.PreAuthID, req.Now,
		func(repo store.Repository, rec *domain.IdempotentRecord) (*VoidResult, string, error) {
			preauth, account, err := loadActivePreAuth(ctx, repo, req.PreAuthID, req.Now)
			if err != nil {
				return nil, "", err
			}
			amount := req.Amount
			if amount == 0 {
				amount = preauth.RemainingHeld()
			}
			if amount <= 0 {
				return nil, "", domain.ErrInvalidAmount
			}
			if amount > preauth.RemainingHeld() {
				return nil, "", domain.ErrAmountExceedsHeld
			}
			// Releasing the hold makes those funds available again.
			account.Reserved -= amount
			account.Version++

			preauth.VoidedAmount += amount
			preauth.Version++
			recomputeStatus(preauth)

			voidRecord := &domain.VoidRecord{
				ID:        req.VoidID,
				PreAuthID: preauth.ID,
				AccountID: account.ID,
				Amount:    amount,
				CreatedAt: req.Now,
			}
			if err := repo.PutVoid(ctx, voidRecord); err != nil {
				return nil, "", err
			}
			if err := repo.PutPreAuth(ctx, preauth); err != nil {
				return nil, "", err
			}
			if _, err := repo.AppendEvent(ctx, &domain.Event{
				Type:      domain.EventVoided,
				PreAuthID: preauth.ID,
				AccountID: account.ID,
				RefID:     req.VoidID,
				Amount:    amount,
				CreatedAt: req.Now,
			}); err != nil {
				return nil, "", err
			}
			result := &VoidResult{
				VoidID:    req.VoidID,
				PreAuthID: preauth.ID,
				Amount:    amount,
			}
			return result, req.VoidID, nil
		},
		// Replay the first void result verbatim.
		func(repo store.Repository, refID string) (*VoidResult, error) {
			v, err := repo.GetVoid(ctx, refID)
			if err != nil {
				return nil, err
			}
			return &VoidResult{VoidID: v.ID, PreAuthID: v.PreAuthID, Amount: v.Amount}, nil
		},
	)
}
