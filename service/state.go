package service

import (
	"context"
	"fmt"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

// loadActivePreAuth fetches a preauth and rejects operations once it has
// fully expired (remaining hold == 0) or passed its expiry time.
func loadActivePreAuth(
	ctx context.Context,
	repo store.Repository,
	id string,
	now time.Time,
) (*domain.PreAuth, *domain.Account, error) {
	preauth, err := repo.GetPreAuth(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	account, err := repo.GetAccount(ctx, preauth.AccountID)
	if err != nil {
		return nil, nil, err
	}
	if preauth.Status == domain.PreAuthVoided ||
		preauth.Status == domain.PreAuthReleased {
		return nil, nil, fmt.Errorf("%w: preauth %s is %s",
			domain.ErrInvalidState, id, preauth.Status)
	}
	if !now.Before(preauth.ExpiresAt) && preauth.RemainingHeld() > 0 {
		return nil, nil, fmt.Errorf("%w: preauth %s expired at %s",
			domain.ErrPreAuthExpired, id, preauth.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	if preauth.RemainingHeld() <= 0 {
		return nil, nil, fmt.Errorf("%w: no remaining hold on preauth %s",
			domain.ErrInvalidState, id)
	}
	return preauth, account, nil
}

// recomputeStatus finalizes the preauth state from its running totals.
func recomputeStatus(preauth *domain.PreAuth) {
	switch {
	case preauth.CapturedAmount+preauth.VoidedAmount >= preauth.HeldAmount:
		if preauth.CapturedAmount > 0 {
			preauth.Status = domain.PreAuthCaptured
		} else {
			preauth.Status = domain.PreAuthVoided
		}
	case preauth.CapturedAmount > 0:
		preauth.Status = domain.PreAuthPartially
	default:
		preauth.Status = domain.PreAuthActive
	}
}
