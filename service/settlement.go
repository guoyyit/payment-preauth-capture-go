package service

import (
	"context"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

// settle performs one idempotent settlement run for a period.
// It is a package-level helper invoked by Service.Settle so the first-run vs
// replay logic stays identical to the other operations.
func settle(
	ctx context.Context,
	s *Service,
	req SettleRequest,
) (*domain.SettlementBatch, error) {
	return runIdempotent(
		ctx, s.db, req.IdempotencyKey, domain.OpSettle, "", req.Now,
		func(repo store.Repository, rec *domain.IdempotentRecord) (*domain.SettlementBatch, string, error) {
			// Same period must always map to the same batch: a prior batch
			// means a retry/duplicate and is returned without side effects.
			if existing, err := repo.GetBatchByPeriod(ctx, req.PeriodID); err == nil {
				return existing, existing.ID, nil
			}

			captures, err := repo.ListUnsettledCaptures(ctx)
			if err != nil {
				return nil, "", err
			}
			batch := &domain.SettlementBatch{
				ID:         req.BatchID,
				PeriodID:   req.PeriodID,
				Status:     domain.BatchBuilding,
				CaptureIDs: []string{},
				RefundIDs:  []string{},
				CreatedAt:  req.Now,
				UpdatedAt:  req.Now,
			}
			for _, capture := range captures {
				// Refunds already present at batch time are simply excluded
				// from the gross sum; later refunds adjust the batch (see
				// service.Refund), keeping final amounts consistent.
				net := capture.Amount - capture.RefundedAmount
				batch.GrossAmount += net
				batch.CaptureIDs = append(batch.CaptureIDs, capture.ID)
				capture.SettledBatchID = batch.ID
				capture.Version++
				if err := repo.PutCapture(ctx, capture); err != nil {
					return nil, "", err
				}
			}
			batch.TotalAmount = batch.GrossAmount
			batch.Status = domain.BatchCompleted
			if err := repo.PutBatch(ctx, batch); err != nil {
				return nil, "", err
			}
			if _, err := repo.AppendEvent(ctx, &domain.Event{
				Type:   domain.EventSettled,
				RefID:  batch.ID,
				Amount: batch.TotalAmount,
				Detail: map[string]any{
					"period_id":     batch.PeriodID,
					"capture_count": len(batch.CaptureIDs),
				},
				CreatedAt: req.Now,
			}); err != nil {
				return nil, "", err
			}
			return batch, batch.ID, nil
		},
		func(repo store.Repository, refID string) (*domain.SettlementBatch, error) {
			return repo.GetBatch(ctx, refID)
		},
	)
}

// GetBatch returns a settlement batch by its ID.
func (s *Service) GetBatch(ctx context.Context, id string) (*domain.SettlementBatch, error) {
	var out *domain.SettlementBatch
	err := s.db.Read(ctx, func(repo store.Repository) error {
		b, err := repo.GetBatch(ctx, id)
		if err != nil {
			return err
		}
		out = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
