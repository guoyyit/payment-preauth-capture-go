package service

import (
	"context"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

func (s *Service) CreateAccount(ctx context.Context, req CreateAccountRequest) (*domain.Account, error) {
	if req.AccountID == "" {
		return nil, domain.ErrInvalidAmount
	}
	if req.Balance <= 0 {
		return nil, domain.ErrInvalidAmount
	}
	account := &domain.Account{ID: req.AccountID, Balance: req.Balance, Reserved: 0}
	err := s.db.Update(ctx, func(repo store.Repository) error {
		return repo.CreateAccount(ctx, account)
	})
	if err != nil {
		return nil, err
	}
	return account, nil
}

func (s *Service) GetAccount(ctx context.Context, id string) (*domain.Account, error) {
	var out *domain.Account
	err := s.db.Read(ctx, func(repo store.Repository) error {
		a, err := repo.GetAccount(ctx, id)
		if err != nil {
			return err
		}
		out = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
