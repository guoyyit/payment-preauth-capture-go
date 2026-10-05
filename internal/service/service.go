package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/internal/domain"
	"github.com/guoyyit/payment-preauth-capture-go/internal/store"
)

// Config 服务配置项。
type Config struct {
	// SnapshotPath 快照文件路径；空串表示仅内存（不持久化）。
	SnapshotPath string
	// Now 时钟来源，默认 time.Now；测试可注入以模拟到期。
	Now func() time.Time
}

// Service 是预授权/捕获/退款/结算服务。
// 单进程内所有变更经同一把互斥锁串行化，配合整份快照原子写盘，
// 保证并发下不出现超额占用、重复释放与重复结算。
type Service struct {
	mu    sync.Mutex
	store *store.Store
	snap  *domain.Snapshot
	now   func() time.Time
	newID func(prefix string) string
}

// New 创建服务实例。若配置了快照路径，则从磁盘加载并执行崩溃恢复。
func New(cfg Config) (*Service, error) {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	svc := &Service{now: now, newID: randomID}
	if cfg.SnapshotPath != "" {
		st, snap, err := store.New(cfg.SnapshotPath)
		if err != nil {
			return nil, err
		}
		svc.store = st
		svc.snap = snap
	} else {
		svc.snap = emptySnapshot()
	}
	return svc, nil
}

func emptySnapshot() *domain.Snapshot {
	return &domain.Snapshot{
		Accounts:    map[string]*domain.Account{},
		PreAuths:    map[string]*domain.PreAuth{},
		Captures:    map[string]*domain.Capture{},
		Refunds:     []*domain.Refund{},
		Releases:    []*domain.ReleaseEvent{},
		Adjustments: []*domain.Adjustment{},
		Batches:     map[string]*domain.SettlementBatch{},
		Idempotency: map[string]*domain.IdempotencyRecord{},
	}
}

// Close 释放资源。当前持久化为每次操作即时落盘，无需额外缓冲。
func (s *Service) Close() {}

// CreateAccount 在本地构造一个账户。
func (s *Service) CreateAccount(id string, balance domain.Money) (*domain.Account, error) {
	if balance < 0 {
		return nil, newBizError(ErrCodeInvalidAmount, "balance must be non-negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.snap.Accounts[id]; ok {
		return nil, newBizError(ErrCodeDuplicateMismatch, "account already exists: "+id)
	}
	acc := &domain.Account{ID: id, Balance: balance, Version: 1}
	s.snap.Accounts[id] = acc
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	return copyPtr(acc), nil
}

// GetAccount 查询账户当前状态。
func (s *Service) GetAccount(id string) (*domain.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc, ok := s.snap.Accounts[id]
	if !ok {
		return nil, newBizError(ErrCodeNotFound, "account not found: "+id)
	}
	return copyPtr(acc), nil
}

// persistLocked 必须在持锁状态下调用。
func (s *Service) persistLocked() error {
	if s.store == nil {
		return nil
	}
	if err := s.store.Save(s.snap); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	return nil
}

// runIdempotent 以幂等键执行业务函数。
//   - 首次提交：执行 fn，成功响应或业务错误连同键落盘；
//   - 重复提交：不执行任何副作用，直接返回首次的响应/错误；
//   - 同键但参数指纹不同或操作种类不同：返回 duplicate_request_mismatch。
func runIdempotent[T any](s *Service, key string, kind domain.OpKind, fingerprint string, fn func() (T, error)) (T, error) {
	var zero T
	s.mu.Lock()
	defer s.mu.Unlock()

	if rec, ok := s.snap.Idempotency[key]; ok {
		if rec.Kind != kind {
			return zero, newBizError(ErrCodeDuplicateMismatch, "idempotency key reused for different operation kind")
		}
		if rec.Fingerprint != fingerprint {
			return zero, newBizError(ErrCodeDuplicateMismatch, "idempotency key reused with different request parameters")
		}
		return replayResult[T](rec)
	}

	result, err := fn()
	rec := &domain.IdempotencyRecord{Key: key, Kind: kind, Fingerprint: fingerprint, CreatedAt: s.now()}
	if err != nil {
		if be := asBizError(err); be != nil {
			rec.ErrorCode = string(be.Code)
			rec.ErrorMsg = be.Message
		}
		s.snap.Idempotency[key] = rec
		_ = s.persistLocked()
		return zero, err
	}
	data, _ := json.Marshal(result)
	rec.ResultJSON = data
	s.snap.Idempotency[key] = rec
	if perr := s.persistLocked(); perr != nil {
		return zero, perr
	}
	return result, nil
}

func asBizError(err error) *BizError {
	for err != nil {
		if be, ok := err.(*BizError); ok {
			return be
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil
		}
		err = u.Unwrap()
	}
	return nil
}

func replayResult[T any](rec *domain.IdempotencyRecord) (T, error) {
	var zero T
	if rec.ErrorCode != "" {
		return zero, newBizError(ErrorCode(rec.ErrorCode), rec.ErrorMsg)
	}
	var out T
	if len(rec.ResultJSON) > 0 {
		if err := json.Unmarshal(rec.ResultJSON, &out); err != nil {
			return zero, err
		}
	}
	return out, nil
}

func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

func randomID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return prefix + "-" + time.Now().Format("20060102150405.000000000")
	}
	return prefix + "-" + hex.EncodeToString(b)
}
