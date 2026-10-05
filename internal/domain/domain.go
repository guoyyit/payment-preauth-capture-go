package domain

import "time"

// Money 以最小货币单位（分）表示金额。
type Money int64

// AuthStatus 预授权状态机。
//
//	Active ──(捕获/撤销用尽额度，或显式关闭)──> Closed
//	Active ──(到期扫描释放剩余额度)──────────> Closed
type AuthStatus string

const (
	AuthActive AuthStatus = "active"
	AuthClosed AuthStatus = "closed"
)

// ReleaseReason 冻结资金释放（解冻）原因。
type ReleaseReason string

const (
	ReleaseExpiry     ReleaseReason = "expiry"      // 到期自动释放
	ReleaseVoid       ReleaseReason = "void"        // 主动撤销
	ReleaseAdjustDown ReleaseReason = "adjust_down" // 金额下调释放差额
)

// BatchStatus 结算批次状态。
type BatchStatus string

const (
	BatchOpen      BatchStatus = "open"      // 已生成、待确认
	BatchConfirmed BatchStatus = "confirmed" // 已确认（重试幂等落点）
)

// OpKind 幂等操作种类。
type OpKind string

const (
	OpCreateAuth OpKind = "create_auth"
	OpCapture    OpKind = "capture"
	OpVoid       OpKind = "void"
	OpAdjust     OpKind = "adjust"
	OpRefund     OpKind = "refund"
	OpSettle     OpKind = "settle"
)

// Account 本地账户，余额 = 可用 + 冻结（恒等不变量）。
type Account struct {
	ID      string `json:"id"`
	Balance Money  `json:"balance"` // 账户总余额
	Frozen  Money  `json:"frozen"`  // 被预授权冻结的总额
	Version int64  `json:"version"`
}

// Available 返回账户可用余额。
func (a *Account) Available() Money {
	return a.Balance - a.Frozen
}

// PreAuth 预授权单。
type PreAuth struct {
	ID        string     `json:"id"`
	AccountID string     `json:"account_id"`
	Amount    Money      `json:"amount"`     // 当前冻结额度（可能被调整）
	Captured  Money      `json:"captured"`   // 已捕获累计
	Released  Money      `json:"released"`   // 已释放（解冻）累计
	ExpiresAt time.Time  `json:"expires_at"` // 有效期截止时刻
	Status    AuthStatus `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	Version   int64      `json:"version"`
}

// Remaining 返回尚未被捕获或释放的冻结额度。
func (p *PreAuth) Remaining() Money {
	return p.Amount - p.Captured - p.Released
}

// Capture 单笔捕获记录。
type Capture struct {
	ID        string    `json:"id"`
	AuthID    string    `json:"auth_id"`
	Amount    Money     `json:"amount"`   // 捕获金额
	Refunded  Money     `json:"refunded"` // 已退款累计
	Settled   bool      `json:"settled"`  // 是否已计入结算批次
	BatchID   string    `json:"batch_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Version   int64     `json:"version"`
}

// Refundable 返回该捕获可退金额。
func (c *Capture) Refundable() Money {
	return c.Amount - c.Refunded
}

// Refund 退款记录。
type Refund struct {
	ID        string    `json:"id"`
	CaptureID string    `json:"capture_id"`
	AuthID    string    `json:"auth_id"`
	Amount    Money     `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}

// ReleaseEvent 资金释放事件，永久留痕可查。
type ReleaseEvent struct {
	ID        string        `json:"id"`
	AuthID    string        `json:"auth_id"`
	Amount    Money         `json:"amount"`
	Reason    ReleaseReason `json:"reason"`
	CreatedAt time.Time     `json:"created_at"`
}

// Adjustment 预授权金额调整记录。
type Adjustment struct {
	ID        string    `json:"id"`
	AuthID    string    `json:"auth_id"`
	OldAmount Money     `json:"old_amount"`
	NewAmount Money     `json:"new_amount"`
	CreatedAt time.Time `json:"created_at"`
}

// SettlementBatch 结算批次。
type SettlementBatch struct {
	ID          string      `json:"id"`
	PeriodStart time.Time   `json:"period_start"`
	PeriodEnd   time.Time   `json:"period_end"`
	CaptureIDs  []string    `json:"capture_ids"`
	TotalAmount Money       `json:"total_amount"`
	Status      BatchStatus `json:"status"`
	Attempts    int         `json:"attempts"`
	CreatedAt   time.Time   `json:"created_at"`
	ConfirmedAt time.Time   `json:"confirmed_at,omitempty"`
}

// IdempotencyRecord 记录一次操作请求的首次执行结果。
type IdempotencyRecord struct {
	Key         string    `json:"key"`
	Kind        OpKind    `json:"kind"`
	Fingerprint string    `json:"fingerprint,omitempty"` // 首次请求参数指纹
	ResultJSON  []byte    `json:"result_json,omitempty"`
	ErrorCode   string    `json:"error_code,omitempty"`
	ErrorMsg    string    `json:"error_msg,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Snapshot 全量持久化状态，原子写盘；重启后整体恢复。
type Snapshot struct {
	Accounts    map[string]*Account           `json:"accounts"`
	PreAuths    map[string]*PreAuth           `json:"preauths"`
	Captures    map[string]*Capture           `json:"captures"`
	Refunds     []*Refund                     `json:"refunds"`
	Releases    []*ReleaseEvent               `json:"releases"`
	Adjustments []*Adjustment                 `json:"adjustments"`
	Batches     map[string]*SettlementBatch   `json:"batches"`
	Idempotency map[string]*IdempotencyRecord `json:"idempotency"`
}
