package service

import "github.com/guoyyit/payment-preauth-capture-go/internal/domain"

// RefundRequest 退款请求，针对单笔捕获全额或部分退款。
type RefundRequest struct {
	IdempotencyKey string
	CaptureID      string
	RefundID       string // 可选，留空自动生成
	Amount         domain.Money
}

// Refund 对已捕获金额退款。
//
// 资金状态：捕获时款项已从客户余额扣转；退款将等额资金退回客户余额。
// 退款累计不得超过该捕获“已捕获且未退款”金额。
//
// 与捕获/结算并发时由服务级互斥锁串行化，结果确定：
//   - 捕获尚未发生则找不到捕获；
//   - 捕获已进入未确认（open）批次，退款被拒（批次处于待定窗口）；
//   - 捕获已随批次确认，退款被拒（退款窗口在结算确认时关闭）。
func (s *Service) Refund(req RefundRequest) (*domain.Refund, error) {
	fp := fingerprint(req)
	return runIdempotent(s, req.IdempotencyKey, domain.OpRefund, fp, func() (*domain.Refund, error) {
		if req.Amount <= 0 {
			return nil, newBizError(ErrCodeInvalidAmount, "refund amount must be positive")
		}
		capRec, ok := s.snap.Captures[req.CaptureID]
		if !ok {
			return nil, newBizError(ErrCodeNotFound, "capture not found: "+req.CaptureID)
		}
		if capRec.BatchID != "" {
			if batch, exists := s.snap.Batches[capRec.BatchID]; exists && batch.Status == domain.BatchConfirmed {
				return nil, newBizError(ErrCodeCaptureSettled, "capture already settled in batch "+capRec.BatchID)
			}
			return nil, newBizError(ErrCodeIllegalState, "capture is locked by open batch "+capRec.BatchID)
		}
		if capRec.Refundable() < req.Amount {
			return nil, newBizError(ErrCodeExceedRefundable, "refund exceeds captured and not-yet-refunded amount")
		}
		refundID := req.RefundID
		if refundID == "" {
			refundID = s.newID("ref")
		}
		ref := &domain.Refund{
			ID:        refundID,
			CaptureID: capRec.ID,
			AuthID:    capRec.AuthID,
			Amount:    req.Amount,
			CreatedAt: s.now(),
		}
		auth := s.snap.PreAuths[capRec.AuthID]
		acc := s.snap.Accounts[auth.AccountID]
		acc.Balance += req.Amount // 款项退回客户
		acc.Version++
		capRec.Refunded += req.Amount
		capRec.Version++
		s.snap.Refunds = append(s.snap.Refunds, ref)
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		return copyPtr(ref), nil
	})
}

// GetCapture 查询单笔捕获及其退款进度。
func (s *Service) GetCapture(id string) (*domain.Capture, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	capRec, ok := s.snap.Captures[id]
	if !ok {
		return nil, newBizError(ErrCodeNotFound, "capture not found: "+id)
	}
	return copyPtr(capRec), nil
}

// ListCaptures 返回某预授权下的全部捕获。
func (s *Service) ListCaptures(authID string) ([]*domain.Capture, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.snap.PreAuths[authID]; !ok {
		return nil, newBizError(ErrCodeNotFound, "preauth not found: "+authID)
	}
	out := make([]*domain.Capture, 0)
	for _, c := range s.snap.Captures {
		if c.AuthID == authID {
			out = append(out, copyPtr(c))
		}
	}
	return out, nil
}

// ListRefunds 返回某预授权下的全部退款记录。
func (s *Service) ListRefunds(authID string) ([]*domain.Refund, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.snap.PreAuths[authID]; !ok {
		return nil, newBizError(ErrCodeNotFound, "preauth not found: "+authID)
	}
	out := make([]*domain.Refund, 0)
	for _, r := range s.snap.Refunds {
		if r.AuthID == authID {
			out = append(out, copyPtr(r))
		}
	}
	return out, nil
}
