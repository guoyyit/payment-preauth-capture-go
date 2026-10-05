package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/guoyyit/payment-preauth-capture-go/internal/domain"
)

// Store 负责全部状态的落盘与恢复。
type Store struct {
	path string
}

// New 创建一个基于本地文件的 Store。
// 文件不存在时初始化为空状态；存在时整体加载并做一致性恢复。
func New(path string) (*Store, *domain.Snapshot, error) {
	s := &Store{path: path}
	snap, err := s.load()
	if err != nil {
		return nil, nil, err
	}
	changed, err := recoverSnapshot(snap)
	if err != nil {
		return nil, nil, fmt.Errorf("recovery failed: %w", err)
	}
	if changed {
		if err := s.Save(snap); err != nil {
			return nil, nil, fmt.Errorf("persist recovered snapshot: %w", err)
		}
	}
	return s, snap, nil
}

// Save 原子写入全量快照：先写临时文件并 fsync，再 rename 覆盖，
// 保证崩溃时要么看到旧版本要么看到新版本，不会出现半写文件。
func (s *Store) Save(snap *domain.Snapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".snapshot-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func (s *Store) load() (*domain.Snapshot, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return newSnapshot(), nil
		}
		return nil, err
	}
	snap := newSnapshot()
	if len(data) == 0 {
		return snap, nil
	}
	if err := json.Unmarshal(data, snap); err != nil {
		return nil, fmt.Errorf("corrupt snapshot: %w", err)
	}
	return snap, nil
}

func newSnapshot() *domain.Snapshot {
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

// recoverSnapshot 在重启后重建可派生状态并校验核心不变量：
//   - 账户冻结额 = 其名下所有有效预授权剩余冻结额之和；
//   - 账户总余额 >= 冻结额；
//   - 每张预授权 已捕获 + 已释放 <= 额度。
//
// 两阶段结算中崩溃在“批次已生成、尚未确认”之间时，重启按既定策略回滚
// 未确认（open）批次并释放捕获占用，客户端可重新发起结算；已确认批次不回滚。
//
// 由于每次业务操作都是整份快照原子提交，不存在“半完成”操作；
// 已确认操作不丢失，未提交操作等同于从未发生，可由客户端安全重试。
// 返回 changed 表示恢复修改了快照，需要重新落盘。
func recoverSnapshot(snap *domain.Snapshot) (bool, error) {
	changed := false

	rollback := map[string]bool{}
	for id, batch := range snap.Batches {
		if batch.Status == domain.BatchOpen {
			for _, capID := range batch.CaptureIDs {
				rollback[capID] = true
			}
			delete(snap.Batches, id)
			changed = true
		}
	}
	for capID := range rollback {
		if c, ok := snap.Captures[capID]; ok {
			c.BatchID = ""
			c.Settled = false
		}
	}

	frozenByAccount := map[string]domain.Money{}
	for _, auth := range snap.PreAuths {
		if auth.Captured < 0 || auth.Released < 0 {
			return false, fmt.Errorf("auth %s has negative captured/released", auth.ID)
		}
		if auth.Captured+auth.Released > auth.Amount {
			return false, fmt.Errorf("auth %s exceeds frozen amount", auth.ID)
		}
		if auth.Status == domain.AuthActive {
			frozenByAccount[auth.AccountID] += auth.Remaining()
		}
	}
	for id, acc := range snap.Accounts {
		want := frozenByAccount[id]
		if acc.Frozen != want {
			acc.Frozen = want
			changed = true
		}
		if acc.Balance < acc.Frozen {
			return false, fmt.Errorf("account %s balance %d below frozen %d", id, acc.Balance, acc.Frozen)
		}
	}
	return changed, nil
}
