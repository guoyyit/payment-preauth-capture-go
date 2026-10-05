package service

import (
	"context"
	"fmt"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

// SweepExpired releases the remaining hold of every preauth whose expiry has
// passed. Each release is committed in its own transaction so one failure does
// not block others; the global store lock makes it safe against concurrent
// capture/void/adjust. It returns the number of preauths released.
func (s *Service) SweepExpired(ctx context.Context, now time.Time) (int, error) {
	var candidates []*domain.PreAuth
	if err := s.db.Read(ctx, func(repo store.Repository) error {
		all, err := repo.ListPreAuths(ctx)
		if err != nil {
			return err
		}
		for _, p := range all {
			if p.RemainingHeld() > 0 && !now.Before(p.ExpiresAt) {
				candidates = append(candidates, p)
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}

	released := 0
	var firstErr error
	for _, candidate := range candidates {
		didRelease := false
		err := s.db.Update(ctx, func(repo store.Repository) error {
			preauth, err := repo.GetPreAuth(ctx, candidate.ID)
			if err != nil {
				return err
			}
			account, err := repo.GetAccount(ctx, preauth.AccountID)
			if err != nil {
				return err
			}
			// Re-check under the write lock: a concurrent capture/void may
			// have already consumed or released the hold.
			remaining := preauth.RemainingHeld()
			if remaining <= 0 || now.Before(preauth.ExpiresAt) {
				return nil
			}
			didRelease = true
			account.Reserved -= remaining
			account.Version++

			preauth.VoidedAmount += remaining
			preauth.Version++
			if preauth.CapturedAmount > 0 {
				preauth.Status = domain.PreAuthCaptured
			} else {
				preauth.Status = domain.PreAuthReleased
			}

			if err := repo.PutPreAuth(ctx, preauth); err != nil {
				return err
			}
			refID := fmt.Sprintf("auto-release-%s", preauth.ID)
			if err := repo.PutVoid(ctx, &domain.VoidRecord{
				ID:        refID,
				PreAuthID: preauth.ID,
				AccountID: account.ID,
				Amount:    remaining,
				CreatedAt: now,
			}); err != nil {
				return err
			}
			if _, err := repo.AppendEvent(ctx, &domain.Event{
				Type:      domain.EventAutoReleased,
				PreAuthID: preauth.ID,
				AccountID: account.ID,
				RefID:     refID,
				Amount:    remaining,
				Detail: map[string]any{
					"reason": "expired",
				},
				CreatedAt: now,
			}); err != nil {
				return err
			}
			return nil
		})
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if err == nil && didRelease {
			released++
		}
	}
	return released, firstErr
}
