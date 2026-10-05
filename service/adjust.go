package service

import (
	"context"
	"fmt"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

func (s *Service) Adjust(ctx context.Context, req AdjustRequest) (*domain.PreAuth, error) {
	if err := validatePositive(req.NewAmount); err != nil {
		return nil, err
	}
	return runIdempotent(
		ctx, s.db, req.IdempotencyKey, domain.OpAdjust, req.PreAuthID, req.Now,
		func(repo store.Repository, rec *domain.IdempotentRecord) (*domain.PreAuth, string, error) {
			preauth, account, err := loadActivePreAuth(ctx, repo, req.PreAuthID, req.Now)
			if err != nil {
				return nil, "", err
			}
			committed := preauth.CapturedAmount + preauth.VoidedAmount
			if req.NewAmount < committed {
				return nil, "", fmt.Errorf(
					"%w: new hold %d cannot be less than already committed %d",
					domain.ErrAmountExceedsHeld, req.NewAmount, committed)
			}
			delta := req.NewAmount - preauth.HeldAmount
			if delta > 0 {
				// Upward adjustment re-checks currently available balance.
				if account.Available() < delta {
					return nil, "", fmt.Errorf("%w: need %d, available %d",
						domain.ErrInsufficientFunds, delta, account.Available())
				}
				account.Reserved += delta
			} else if delta < 0 {
				// Downward adjustment immediately releases the difference.
				account.Reserved += delta // delta is negative
			}
			account.Version++

			preauth.HeldAmount = req.NewAmount
			preauth.AdjustedAt = req.Now
			preauth.Version++
			recomputeStatus(preauth)

			if err := repo.PutPreAuth(ctx, preauth); err != nil {
				return nil, "", err
			}
			if _, err := repo.AppendEvent(ctx, &domain.Event{
				Type:      domain.EventPreAuthAdjusted,
				PreAuthID: preauth.ID,
				AccountID: account.ID,
				RefID:     rec.Key,
				Amount:    delta,
				Detail: map[string]any{
					"new_held": req.NewAmount,
				},
				CreatedAt: req.Now,
			}); err != nil {
				return nil, "", err
			}
			return preauth, preauth.ID, nil
		},
		func(repo store.Repository, refID string) (*domain.PreAuth, error) {
			return repo.GetPreAuth(ctx, refID)
		},
	)
}
