package service

import (
	"context"
	"fmt"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

func (s *Service) PreAuthorize(ctx context.Context, req PreAuthRequest) (*domain.PreAuth, error) {
	if err := validatePositive(req.Amount); err != nil {
		return nil, err
	}
	if req.TTL <= 0 {
		return nil, fmt.Errorf("%w: ttl must be positive", domain.ErrInvalidAmount)
	}
	return runIdempotent(
		ctx, s.db, req.IdempotencyKey, domain.OpPreAuth, req.PreAuthID, req.Now,
		func(repo store.Repository, rec *domain.IdempotentRecord) (*domain.PreAuth, string, error) {
			account, err := repo.GetAccount(ctx, req.AccountID)
			if err != nil {
				return nil, "", err
			}
			if account.Available() < req.Amount {
				return nil, "", fmt.Errorf("%w: available %d < hold %d",
					domain.ErrInsufficientFunds, account.Available(), req.Amount)
			}
			if _, err := repo.GetPreAuth(ctx, req.PreAuthID); err == nil {
				return nil, "", fmt.Errorf("%w: preauth id %s", domain.ErrDuplicateRequest, req.PreAuthID)
			}
			now := req.Now
			preauth := &domain.PreAuth{
				ID:             req.PreAuthID,
				AccountID:      req.AccountID,
				OriginalAmount: req.Amount,
				HeldAmount:     req.Amount,
				Status:         domain.PreAuthActive,
				CreatedAt:      now,
				ExpiresAt:      now.Add(req.TTL),
				AdjustedAt:     now,
			}
			account.Reserved += req.Amount
			account.Version++
			if err := repo.PutPreAuth(ctx, preauth); err != nil {
				return nil, "", err
			}
			if _, err := repo.AppendEvent(ctx, &domain.Event{
				Type:      domain.EventPreAuthCreated,
				PreAuthID: preauth.ID,
				AccountID: account.ID,
				RefID:     preauth.ID,
				Amount:    req.Amount,
				Detail: map[string]any{
					"expires_at": preauth.ExpiresAt.Format(time.RFC3339Nano),
				},
				CreatedAt: now,
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

func (s *Service) GetPreAuth(ctx context.Context, id string) (*domain.PreAuth, error) {
	var out *domain.PreAuth
	err := s.db.Read(ctx, func(repo store.Repository) error {
		p, err := repo.GetPreAuth(ctx, id)
		if err != nil {
			return err
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) ListEvents(ctx context.Context) ([]*domain.Event, error) {
	var out []*domain.Event
	err := s.db.Read(ctx, func(repo store.Repository) error {
		events, err := repo.ListEvents(ctx)
		if err != nil {
			return err
		}
		out = events
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
