package service

import "github.com/guoyyit/payment-preauth-capture-go/internal/domain"

// AdjustRequest 预授权额度调整请求。
type AdjustRequest struct {
	IdempotencyKey string
	AuthID         string
	NewAmount      domain.Money // 调整后的目标冻结额度（绝对值，非增量）
}

// AdjustResult 调整结果。
type AdjustResult struct {
	Adjustment *domain.Adjustment
	Auth       *domain.PreAuth
	Release    *domain.ReleaseEvent // 仅下调时非空
}

// AdjustAmount 调整预授权冻结额度。
//   - 上调：重新校验账户可用余额（余额不足拒绝），追加冻结；
//   - 下调：立即释放差额并写留痕事件；
//   - 目标额度低于已占用（已捕获+已释放）时拒绝。
//
// 与捕获/撤销的并发由服务级互斥锁串行化：后到者基于前者的结果做校验，
// 因此不存在中间态，最终金额确定且唯一。
func (s *Service) AdjustAmount(req AdjustRequest) (*AdjustResult, error) {
	fp := fingerprint(req)
	return runIdempotent(s, req.IdempotencyKey, domain.OpAdjust, fp, func() (*AdjustResult, error) {
		if req.NewAmount <= 0 {
			return nil, newBizError(ErrCodeInvalidAmount, "new amount must be positive")
		}
		auth, err := s.requireActiveAuth(req.AuthID)
		if err != nil {
			return nil, err
		}
		used := auth.Captured + auth.Released
		if req.NewAmount < used {
			return nil, newBizError(ErrCodeAdjustBelowUsed, "new amount below captured+released")
		}
		acc := s.snap.Accounts[auth.AccountID]
		old := auth.Amount
		var releaseEv *domain.ReleaseEvent

		switch {
		case req.NewAmount > old:
			delta := req.NewAmount - old
			if acc.Available() < delta {
				return nil, newBizError(ErrCodeInsufficientFunds, "available balance below incremental freeze")
			}
			acc.Frozen += delta
			acc.Version++
		case req.NewAmount < old:
			// 以“当前实际冻结额”和“目标冻结额”之差释放，
			// 避免在捕获/撤销之后到达时把已离开冻结桶的资金重复释放。
			targetFrozen := req.NewAmount - auth.Captured - auth.Released
			delta := auth.Remaining() - targetFrozen
			acc.Frozen -= delta
			acc.Version++
			releaseEv = &domain.ReleaseEvent{
				ID:        s.newID("rel"),
				AuthID:    auth.ID,
				Amount:    delta,
				Reason:    domain.ReleaseAdjustDown,
				CreatedAt: s.now(),
			}
			s.snap.Releases = append(s.snap.Releases, releaseEv)
		}

		auth.Amount = req.NewAmount
		auth.Version++
		if auth.Remaining() == 0 {
			auth.Status = domain.AuthClosed
		}
		now := s.now()
		adj := &domain.Adjustment{
			ID:        s.newID("adj"),
			AuthID:    auth.ID,
			OldAmount: old,
			NewAmount: req.NewAmount,
			CreatedAt: now,
		}
		s.snap.Adjustments = append(s.snap.Adjustments, adj)
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		return &AdjustResult{
			Adjustment: copyPtr(adj),
			Auth:       copyPtr(auth),
			Release:    copyPtr(releaseEv),
		}, nil
	})
}

// ListAdjustments 返回某预授权的调整历史。
func (s *Service) ListAdjustments(authID string) ([]*domain.Adjustment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.snap.PreAuths[authID]; !ok {
		return nil, newBizError(ErrCodeNotFound, "preauth not found: "+authID)
	}
	out := make([]*domain.Adjustment, 0)
	for _, a := range s.snap.Adjustments {
		if a.AuthID == authID {
			out = append(out, copyPtr(a))
		}
	}
	return out, nil
}
