package service

import (
	"encoding/json"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/internal/domain"
)

// CreateAuthRequest 创建预授权请求。
type CreateAuthRequest struct {
	IdempotencyKey string
	AccountID      string
	AuthID         string // 可选，留空则自动生成
	Amount         domain.Money
	TTLMillis      int64 // 有效期（毫秒），必须为正
}

// CaptureRequest 捕获请求，支持部分捕获。
type CaptureRequest struct {
	IdempotencyKey string
	AuthID         string
	CaptureID      string // 可选，留空自动生成
	Amount         domain.Money
}

// VoidRequest 撤销请求，支持部分撤销。
type VoidRequest struct {
	IdempotencyKey string
	AuthID         string
	ReleaseID      string // 可选，留空自动生成
	Amount         domain.Money
}

// CaptureResult 捕获成功结果（重复提交时原样返回）。
type CaptureResult struct {
	Capture *domain.Capture
	Auth    *domain.PreAuth
}

// VoidResult 撤销成功结果。
type VoidResult struct {
	Release *domain.ReleaseEvent
	Auth    *domain.PreAuth
}

// CreatePreAuth 冻结账户资金并创建带有效期的预授权。
func (s *Service) CreatePreAuth(req CreateAuthRequest) (*domain.PreAuth, error) {
	fp := fingerprint(req)
	return runIdempotent(s, req.IdempotencyKey, domain.OpCreateAuth, fp, func() (*domain.PreAuth, error) {
		if req.Amount <= 0 {
			return nil, newBizError(ErrCodeInvalidAmount, "auth amount must be positive")
		}
		if req.TTLMillis <= 0 {
			return nil, newBizError(ErrCodeInvalidAmount, "auth ttl must be positive")
		}
		acc, ok := s.snap.Accounts[req.AccountID]
		if !ok {
			return nil, newBizError(ErrCodeNotFound, "account not found: "+req.AccountID)
		}
		if acc.Available() < req.Amount {
			return nil, newBizError(ErrCodeInsufficientFunds, "available balance below auth amount")
		}
		authID := req.AuthID
		if authID == "" {
			authID = s.newID("auth")
		}
		if _, dup := s.snap.PreAuths[authID]; dup {
			return nil, newBizError(ErrCodeDuplicateMismatch, "auth already exists: "+authID)
		}
		now := s.now()
		auth := &domain.PreAuth{
			ID:        authID,
			AccountID: req.AccountID,
			Amount:    req.Amount,
			ExpiresAt: now.Add(durationMillis(req.TTLMillis)),
			Status:    domain.AuthActive,
			CreatedAt: now,
			Version:   1,
		}
		acc.Frozen += req.Amount
		acc.Version++
		s.snap.PreAuths[authID] = auth
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		return copyPtr(auth), nil
	})
}

// Capture 对预授权执行（部分）捕获。捕获金额从冻结额转为已扣款。
func (s *Service) Capture(req CaptureRequest) (*CaptureResult, error) {
	fp := fingerprint(req)
	return runIdempotent(s, req.IdempotencyKey, domain.OpCapture, fp, func() (*CaptureResult, error) {
		if req.Amount <= 0 {
			return nil, newBizError(ErrCodeInvalidAmount, "capture amount must be positive")
		}
		auth, err := s.requireActiveAuth(req.AuthID)
		if err != nil {
			return nil, err
		}
		if auth.Remaining() < req.Amount {
			return nil, newBizError(ErrCodeExceedRemaining, "capture exceeds remaining frozen amount")
		}
		captureID := req.CaptureID
		if captureID == "" {
			captureID = s.newID("cap")
		}
		if _, dup := s.snap.Captures[captureID]; dup {
			return nil, newBizError(ErrCodeDuplicateMismatch, "capture already exists: "+captureID)
		}
		now := s.now()
		capRec := &domain.Capture{
			ID:        captureID,
			AuthID:    auth.ID,
			Amount:    req.Amount,
			CreatedAt: now,
			Version:   1,
		}
		acc := s.snap.Accounts[auth.AccountID]
		acc.Frozen -= req.Amount  // 离开冻结桶
		acc.Balance -= req.Amount // 客户资金实际扣转
		acc.Version++
		auth.Captured += req.Amount
		auth.Version++
		if auth.Remaining() == 0 {
			auth.Status = domain.AuthClosed
		}
		s.snap.Captures[captureID] = capRec
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		return &CaptureResult{Capture: copyPtr(capRec), Auth: copyPtr(auth)}, nil
	})
}

// Void 主动撤销（部分）冻结额度，释放事件留痕。
func (s *Service) Void(req VoidRequest) (*VoidResult, error) {
	fp := fingerprint(req)
	return runIdempotent(s, req.IdempotencyKey, domain.OpVoid, fp, func() (*VoidResult, error) {
		if req.Amount <= 0 {
			return nil, newBizError(ErrCodeInvalidAmount, "void amount must be positive")
		}
		auth, err := s.requireActiveAuth(req.AuthID)
		if err != nil {
			return nil, err
		}
		if auth.Remaining() < req.Amount {
			return nil, newBizError(ErrCodeExceedRemaining, "void exceeds remaining frozen amount")
		}
		ev := s.releaseLocked(auth, req.Amount, domain.ReleaseVoid, req.ReleaseID)
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		return &VoidResult{Release: copyPtr(ev), Auth: copyPtr(auth)}, nil
	})
}

// SweepExpired 扫描并释放所有已到期但仍有剩余额度的预授权。
// 释放全部留痕；对同一预授权重复扫描是幂等的，不会重复释放。
// 返回本次实际发生的释放事件数量。
func (s *Service) SweepExpired() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	count := 0
	closedOnly := 0
	for _, auth := range s.snap.PreAuths {
		if auth.Status != domain.AuthActive || now.Before(auth.ExpiresAt) {
			continue
		}
		if auth.Remaining() == 0 {
			auth.Status = domain.AuthClosed
			closedOnly++
			continue
		}
		s.releaseLocked(auth, auth.Remaining(), domain.ReleaseExpiry, "")
		count++
	}
	if count+closedOnly > 0 {
		if err := s.persistLocked(); err != nil {
			return 0, err
		}
	}
	return count, nil
}

// requireActiveAuth 返回有效的预授权；若已到期则先完成到期释放再拒绝操作。
// 必须在持锁状态下调用。
func (s *Service) requireActiveAuth(id string) (*domain.PreAuth, error) {
	auth, ok := s.snap.PreAuths[id]
	if !ok {
		return nil, newBizError(ErrCodeNotFound, "preauth not found: "+id)
	}
	now := s.now()
	if auth.Status == domain.AuthActive && !now.Before(auth.ExpiresAt) {
		if auth.Remaining() > 0 {
			s.releaseLocked(auth, auth.Remaining(), domain.ReleaseExpiry, "")
		} else {
			auth.Status = domain.AuthClosed
		}
	}
	if auth.Status != domain.AuthActive {
		if now.Before(auth.ExpiresAt) {
			return nil, newBizError(ErrCodeExceedRemaining, "preauth fully consumed: "+id)
		}
		return nil, newBizError(ErrCodeAuthExpired, "preauth is expired: "+id)
	}
	return auth, nil
}

// releaseLocked 释放指定金额并追加留痕事件，额度用尽时关闭预授权。
// 必须在持锁状态下调用。
func (s *Service) releaseLocked(auth *domain.PreAuth, amount domain.Money, reason domain.ReleaseReason, eventID string) *domain.ReleaseEvent {
	if eventID == "" {
		eventID = s.newID("rel")
	}
	ev := &domain.ReleaseEvent{
		ID:        eventID,
		AuthID:    auth.ID,
		Amount:    amount,
		Reason:    reason,
		CreatedAt: s.now(),
	}
	acc := s.snap.Accounts[auth.AccountID]
	acc.Frozen -= amount
	acc.Version++
	auth.Released += amount
	auth.Version++
	if auth.Remaining() == 0 {
		auth.Status = domain.AuthClosed
	}
	s.snap.Releases = append(s.snap.Releases, ev)
	return ev
}

// GetPreAuth 查询预授权。
func (s *Service) GetPreAuth(id string) (*domain.PreAuth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	auth, ok := s.snap.PreAuths[id]
	if !ok {
		return nil, newBizError(ErrCodeNotFound, "preauth not found: "+id)
	}
	return copyPtr(auth), nil
}

// ListReleases 返回某预授权的全部资金释放事件（到期/撤销/下调）。
func (s *Service) ListReleases(authID string) ([]*domain.ReleaseEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.snap.PreAuths[authID]; !ok {
		return nil, newBizError(ErrCodeNotFound, "preauth not found: "+authID)
	}
	out := make([]*domain.ReleaseEvent, 0)
	for _, ev := range s.snap.Releases {
		if ev.AuthID == authID {
			out = append(out, copyPtr(ev))
		}
	}
	return out, nil
}

func durationMillis(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
