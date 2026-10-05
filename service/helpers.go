package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

func validatePositive(amount domain.Amount) error {
	if amount <= 0 {
		return fmt.Errorf("%w: amount must be positive", domain.ErrInvalidAmount)
	}
	return nil
}

// beginIdempotent returns an existing record (replay path) or inserts a fresh one.
func beginIdempotent(
	ctx context.Context,
	repo store.Repository,
	key string,
	op domain.OperationType,
	preauthID string,
	now time.Time,
) (*domain.IdempotentRecord, bool, error) {
	if key == "" {
		return nil, false, errors.New("idempotency key is required")
	}
	existing, inserted, err := repo.PutIdempotent(ctx, &domain.IdempotentRecord{
		Key:       key,
		OpType:    op,
		PreAuthID: preauthID,
		Status:    domain.OpFailed,
		CreatedAt: now,
	})
	if err != nil {
		return nil, false, err
	}
	if !inserted {
		if existing.OpType != op || existing.PreAuthID != preauthID {
			return existing, false, fmt.Errorf(
				"%w: key reused for a different operation", domain.ErrDuplicateRequest)
		}
		return existing, false, nil
	}
	return existing, true, nil
}

// completeIdempotent records the successful result reference on a fresh record.
func completeIdempotent(
	ctx context.Context,
	repo store.Repository,
	rec *domain.IdempotentRecord,
	refID string,
) error {
	rec.Status = domain.OpCompleted
	rec.ResultRefID = refID
	rec.ErrCode = ""
	rec.ErrMessage = ""
	_, _, err := repo.PutIdempotent(ctx, rec)
	return err
}

// failIdempotent stamps a business error onto the record so replays return the
// same first-time failure instead of re-executing side effects.
func failIdempotent(
	ctx context.Context,
	repo store.Repository,
	rec *domain.IdempotentRecord,
	err error,
) {
	rec.ErrMessage = err.Error()
	for _, sentinel := range []error{
		domain.ErrAccountNotFound,
		domain.ErrPreAuthNotFound,
		domain.ErrInsufficientFunds,
		domain.ErrAmountExceedsHeld,
		domain.ErrAmountExceedsCaptured,
		domain.ErrInvalidState,
		domain.ErrPreAuthExpired,
		domain.ErrDuplicateRequest,
		domain.ErrAlreadySettled,
		domain.ErrInvalidAmount,
		domain.ErrNotFound,
	} {
		if errors.Is(err, sentinel) {
			rec.ErrCode = sentinel.Error()
			break
		}
	}
	_, _, _ = repo.PutIdempotent(ctx, rec)
}

// replayError rebuilds the original error from a failed first-time execution.
func replayError(rec *domain.IdempotentRecord) error {
	if rec.ErrCode == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", errorByCode(rec.ErrCode), rec.ErrMessage)
}

func errorByCode(code string) error {
	switch code {
	case domain.ErrAccountNotFound.Error():
		return domain.ErrAccountNotFound
	case domain.ErrPreAuthNotFound.Error():
		return domain.ErrPreAuthNotFound
	case domain.ErrInsufficientFunds.Error():
		return domain.ErrInsufficientFunds
	case domain.ErrAmountExceedsHeld.Error():
		return domain.ErrAmountExceedsHeld
	case domain.ErrAmountExceedsCaptured.Error():
		return domain.ErrAmountExceedsCaptured
	case domain.ErrInvalidState.Error():
		return domain.ErrInvalidState
	case domain.ErrPreAuthExpired.Error():
		return domain.ErrPreAuthExpired
	case domain.ErrDuplicateRequest.Error():
		return domain.ErrDuplicateRequest
	case domain.ErrAlreadySettled.Error():
		return domain.ErrAlreadySettled
	case domain.ErrInvalidAmount.Error():
		return domain.ErrInvalidAmount
	default:
		return domain.ErrInvalidState
	}
}

// runIdempotent executes action exactly-once for a given idempotency key.
// Business failures are durably recorded as FAILED and replayed verbatim;
// system errors abort the transaction so the operation can be retried.
func runIdempotent[T any](
	ctx context.Context,
	db *store.FileStore,
	key string,
	op domain.OperationType,
	preauthID string,
	now time.Time,
	action func(repo store.Repository, rec *domain.IdempotentRecord) (T, string, error),
	loadResult func(repo store.Repository, refID string) (T, error),
) (T, error) {
	var out T
	var bizErr error
	err := db.Update(ctx, func(repo store.Repository) error {
		rec, isNew, err := beginIdempotent(ctx, repo, key, op, preauthID, now)
		if err != nil {
			return err
		}
		if !isNew {
			if rec.Status == domain.OpCompleted {
				out, bizErr = loadResult(repo, rec.ResultRefID)
			} else {
				bizErr = replayError(rec)
			}
			return nil
		}
		result, refID, actionErr := action(repo, rec)
		if actionErr != nil {
			failIdempotent(ctx, repo, rec, actionErr)
			bizErr = actionErr
			return nil
		}
		if err := completeIdempotent(ctx, repo, rec, refID); err != nil {
			return err
		}
		out = result
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, bizErr
}
