package service

import (
	"sort"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/internal/domain"
)

func timeMillis(ms int64) time.Time {
	return time.UnixMilli(ms)
}

// SettlementRequest 生成结算批次的请求。
type SettlementRequest struct {
	IdempotencyKey string
	PeriodStart    int64  // 结算周期起（Unix 毫秒）
	PeriodEnd      int64  // 结算周期止（Unix 毫秒）
	BatchID        string // 可选，留空自动生成
}

// ConfirmBatchRequest 确认批次请求；重试时原样返回首次结果。
type ConfirmBatchRequest struct {
	IdempotencyKey string
	BatchID        string
}

// GenerateBatch 将结算周期内已捕获、未退款净额为正且尚未计入任何批次的捕获
// 汇总为一个 open 批次，并把这些捕获标记为“被该批次锁定”。
// 同一捕获同一时刻只能属于一个批次，因此不会重复计入。
func (s *Service) GenerateBatch(req SettlementRequest) (*domain.SettlementBatch, error) {
	fp := fingerprint(req)
	return runIdempotent(s, req.IdempotencyKey, domain.OpSettle, fp, func() (*domain.SettlementBatch, error) {
		return s.generateBatchLocked(req, domain.BatchOpen)
	})
}

// ConfirmBatch 确认批次；对同一批次重复确认是幂等的，
// 始终返回同一批次且不产生二次副作用。
func (s *Service) ConfirmBatch(req ConfirmBatchRequest) (*domain.SettlementBatch, error) {
	fp := fingerprint(req)
	return runIdempotent(s, req.IdempotencyKey, domain.OpSettle, fp, func() (*domain.SettlementBatch, error) {
		batch, ok := s.snap.Batches[req.BatchID]
		if !ok {
			return nil, newBizError(ErrCodeNotFound, "batch not found: "+req.BatchID)
		}
		if batch.Status == domain.BatchConfirmed {
			return copyPtr(batch), nil
		}
		batch.Status = domain.BatchConfirmed
		batch.Attempts++
		batch.ConfirmedAt = s.now()
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		return copyPtr(batch), nil
	})
}

// SettleCycle 在一次原子操作内生成并确认结算批次。
// 与并发退款串行执行：要么退款先入账从而不计入本批次，要么批次先生成
// 从而拒绝该捕获的退款，最终金额确定一致。重复提交返回首次批次。
func (s *Service) SettleCycle(req SettlementRequest) (*domain.SettlementBatch, error) {
	fp := fingerprint(req)
	return runIdempotent(s, req.IdempotencyKey, domain.OpSettle, fp, func() (*domain.SettlementBatch, error) {
		batch, err := s.generateBatchLocked(req, domain.BatchConfirmed)
		if err != nil {
			return nil, err
		}
		batch.Attempts = 1
		batch.ConfirmedAt = s.now()
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		return copyPtr(batch), nil
	})
}

func (s *Service) generateBatchLocked(req SettlementRequest, status domain.BatchStatus) (*domain.SettlementBatch, error) {
	start := timeMillis(req.PeriodStart)
	end := timeMillis(req.PeriodEnd)
	if !end.After(start) {
		return nil, newBizError(ErrCodeInvalidAmount, "settlement period end must be after start")
	}

	type netCapture struct {
		c   *domain.Capture
		net domain.Money
	}
	picked := make([]netCapture, 0)
	for _, c := range s.snap.Captures {
		if c.BatchID != "" {
			continue // 已计入（含 open）批次，跳过
		}
		if c.CreatedAt.Before(start) || !c.CreatedAt.Before(end) {
			continue
		}
		net := c.Amount - c.Refunded
		if net <= 0 {
			continue
		}
		picked = append(picked, netCapture{c: c, net: net})
	}
	if len(picked) == 0 {
		return nil, newBizError(ErrCodeSettleEmpty, "no unsettled captures in the period")
	}

	sort.Slice(picked, func(i, j int) bool { return picked[i].c.CreatedAt.Before(picked[j].c.CreatedAt) })

	batchID := req.BatchID
	if batchID == "" {
		batchID = s.newID("batch")
	}
	if _, dup := s.snap.Batches[batchID]; dup {
		return nil, newBizError(ErrCodeDuplicateMismatch, "batch already exists: "+batchID)
	}
	batch := &domain.SettlementBatch{
		ID:          batchID,
		PeriodStart: start,
		PeriodEnd:   end,
		Status:      status,
		CreatedAt:   s.now(),
		CaptureIDs:  make([]string, 0, len(picked)),
	}
	for _, p := range picked {
		batch.CaptureIDs = append(batch.CaptureIDs, p.c.ID)
		batch.TotalAmount += p.net
		p.c.BatchID = batchID
		p.c.Settled = status == domain.BatchConfirmed
		p.c.Version++
	}
	s.snap.Batches[batchID] = batch
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	return copyPtr(batch), nil
}

// GetBatch 查询结算批次。
func (s *Service) GetBatch(id string) (*domain.SettlementBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, ok := s.snap.Batches[id]
	if !ok {
		return nil, newBizError(ErrCodeNotFound, "batch not found: "+id)
	}
	return copyPtr(batch), nil
}

// ListBatches 返回全部结算批次，按创建时间升序。
func (s *Service) ListBatches() []*domain.SettlementBatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.SettlementBatch, 0, len(s.snap.Batches))
	for _, b := range s.snap.Batches {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	res := make([]*domain.SettlementBatch, len(out))
	for i := range out {
		res[i] = copyPtr(&out[i])
	}
	return res
}
