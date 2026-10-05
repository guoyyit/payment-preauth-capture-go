package store

import (
	"context"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
)

// Repository is the in-transaction data view used by the service layer.
type Repository interface {
	CreateAccount(ctx context.Context, account *domain.Account) error
	GetAccount(ctx context.Context, id string) (*domain.Account, error)

	PutPreAuth(ctx context.Context, preauth *domain.PreAuth) error
	GetPreAuth(ctx context.Context, id string) (*domain.PreAuth, error)
	ListPreAuths(ctx context.Context) ([]*domain.PreAuth, error)

	PutCapture(ctx context.Context, capture *domain.Capture) error
	GetCapture(ctx context.Context, id string) (*domain.Capture, error)
	ListCapturesByPreAuth(ctx context.Context, preauthID string) ([]*domain.Capture, error)
	ListUnsettledCaptures(ctx context.Context) ([]*domain.Capture, error)

	PutVoid(ctx context.Context, void *domain.VoidRecord) error
	GetVoid(ctx context.Context, id string) (*domain.VoidRecord, error)

	PutRefund(ctx context.Context, refund *domain.Refund) error
	GetRefund(ctx context.Context, id string) (*domain.Refund, error)
	ListRefundsByPreAuth(ctx context.Context, preauthID string) ([]*domain.Refund, error)

	AppendEvent(ctx context.Context, event *domain.Event) (int64, error)
	ListEvents(ctx context.Context) ([]*domain.Event, error)

	GetIdempotent(ctx context.Context, key string) (*domain.IdempotentRecord, error)
	PutIdempotent(ctx context.Context, record *domain.IdempotentRecord) (existing *domain.IdempotentRecord, inserted bool, err error)

	PutBatch(ctx context.Context, batch *domain.SettlementBatch) error
	GetBatchByPeriod(ctx context.Context, periodID string) (*domain.SettlementBatch, error)
	GetBatch(ctx context.Context, id string) (*domain.SettlementBatch, error)
}

// FileStore is an atomic, file-backed, crash-safe implementation of Repository.
// Every committed transaction is persisted as a single atomic file replacement.
// Its concrete state is implemented in filestore.go.
type FileStore = fileStore
