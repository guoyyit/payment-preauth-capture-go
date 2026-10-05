# 本地支付预授权与捕获结算服务

纯本地、无外部依赖的支付预授权（pre-auth）/捕获（capture）/撤销（void）/退款（refund）/
批量结算（settlement）参考实现。全部状态以整数最小货币单位（分）记账，并原子落盘，
进程重启后状态一致恢复。

## 目录结构

| 路径 | 职责 |
| --- | --- |
| `domain/` | 领域模型、状态枚举、可区分的哨兵错误 |
| `store/` | 文件型持久化：事务、快照克隆、原子 `rename` 落盘、崩溃恢复 |
| `service/` | 状态机与全部业务操作（预授权/捕获/撤销/调整/退款/结算/过期扫描） |
| `expiry/` | 定时触发到期自动释放的后台定时器 |
| `config/` | 配置项与 JSON 加载 |
| `cmd/demo/` | 本地并发冒烟演示，`go run ./cmd/demo` |

## 账户记账不变量

- `Balance`：账户当前资金；`Reserved`：被有效预授权冻结的金额。
- `Available = Balance - Reserved`：仍可被新预授权/上调冻结的金额。
- 创建预授权：`Reserved += hold`（校验 `Available`）。
- 捕获：冻结资金转为已扣款，`Balance -= amount`、`Reserved -= amount`。
- 撤销/到期释放：`Reserved -= amount`，资金重新可用。
- 退款：`Balance += amount`，把已捕获资金退回；不触碰冻结额度。
- 任意时刻 `Reserved == 该账户所有未提交冻结额之和`，`Balance >= 0`。

## 预授权状态模型

```
                 ┌─────────┐  capture 部分(>0)且仍有剩余   ┌────────────────────┐
   create ─────▶ │ ACTIVE  │ ───────────────────────────▶ │ PARTIALLY_CAPTURED │
                 └─────────┘                                 └────────────────────┘
                    │   │  capture 打满 / capture+void 打满     │ capture 打满
          void 全部 │   └──────────────┐        void 剩余       ▼
                    ▼                  ▼                    ┌──────────┐
              ┌─────────┐        ┌───────────┐              │ CAPTURED │
              │ VOIDED  │        │ CAPTURED  │              └──────────┘
              └─────────┘        └───────────┘                    ▲
                    │                  ▲                            │ 到期且有捕获
          到期、无捕获│        到期、有捕获(剩余已释放)              │
                    ▼                  │                    ┌──────────┐
              ┌──────────┐             └────────────────────│ RELEASED │
              │ RELEASED  │  到期、捕获=0 的全部释放          └──────────┘
              └──────────┘
```

- `ACTIVE`：冻结有效，可捕获/撤销/调整（未过期）。
- `PARTIALLY_CAPTURED`：已部分捕获，剩余冻结仍可捕获或撤销。
- `CAPTURED`：冻结额全部提交（捕获或捕获+撤销），终态。
- `VOIDED`：未发生捕获即全部撤销，终态。
- `RELEASED`：到期时剩余冻结被自动释放（若曾有部分捕获则同时进入 `CAPTURED`），终态。

合法转移之外的操作一律拒绝，并返回**可区分错误**（见下文）。
过期时刻（`now >= ExpiresAt`）后，对仍有剩余冻结的预授权执行捕获/撤销/调整会返回
`ErrPreAuthExpired`；需先由 `SweepExpired` 完成自动释放。

## 操作规则

- **预授权**：必须携带有效期（`TTL > 0`）；到期未捕获部分由扫描器自动释放并写
  `AUTO_RELEASED` 事件，事件可通过 `ListEvents` 审计。
- **部分捕获/撤销**：同一预授权下
  `CapturedAmount + VoidedAmount <= HeldAmount`；超额返回 `ErrAmountExceedsHeld`。
  撤销金额传 `0` 表示释放全部剩余冻结。
- **金额调整**：
  - 上调：重新校验 `Available >= delta`，不足返回 `ErrInsufficientFunds`；
  - 下调：立即释放差额（`Reserved -= |delta|`）；
  - 新冻结额不得低于已提交金额（`captured + voided`）；
  - 调整与捕获/撤销串行化，结果确定（同一全局提交顺序下重放，永远得到一致状态）。
- **退款**：针对单笔捕获，累计退款不得超过 `capture.Amount - capture.RefundedAmount`，
  超额返回 `ErrAmountExceedsCaptured`；金额传 `0` 退该捕获的全部可退余额。
- **批量结算**：
  - 以 `PeriodID` 标识结算周期，同周期只生成一个批次；
  - 每笔捕获带 `SettledBatchID`，**绝不重复计入**；
  - 批次重试与并发结算幂等（相同幂等键/周期返回同一批次）；
  - 批次生成时已发生的退款从毛额中排除；批次完成后再退款会冲减该批次
    （`RefundAmount`/`TotalAmount`/`RefundIDs`），因此结算与退款并发时
    `GrossAmount - RefundAmount = TotalAmount` 恒成立。

## 幂等语义

- 每个写操作必须携带 `IdempotencyKey`。
- 首次执行：记录 `COMPLETED`（含结果引用 ID）或 `FAILED`（含错误码），与业务变更
  **在同一原子事务**落盘。
- 重复提交同一键：直接返回首次结果（成功回放原对象，失败回放原错误），
  **不产生任何二次副作用**。
- 幂等键与 `(操作类型, 预授权ID)` 绑定，跨操作复用返回 `ErrDuplicateRequest`。
- 批次结算同时以幂等键与 `PeriodID` 双重去重。

可区分错误（`domain/errors.go`，均可用 `errors.Is` 判断）：
`ErrAccountNotFound`、`ErrPreAuthNotFound`、`ErrInsufficientFunds`、
`ErrAmountExceedsHeld`、`ErrAmountExceedsCaptured`、`ErrInvalidState`、
`ErrPreAuthExpired`、`ErrDuplicateRequest`、`ErrAlreadySettled`、
`ErrInvalidAmount`、`ErrNotFound`。

## 并发模型

`store.FileStore` 以单一 RWMutex 串行化所有写事务，事务内对快照**克隆副本**操作，
业务函数成功后才原子写回。由此：

- 并发捕获/撤销/调整/退款/结算按某一全序提交，不可能超额占用或重复释放；
- 业务失败仅回滚内存副本，磁盘保持上一已提交快照；
- 读事务基于快照克隆，不加长锁、不阻塞写。

## 崩溃恢复

- 提交单元为整份快照：写入同目录临时文件 → `fsync` → 原子 `rename` → 目录 `fsync`。
- 崩溃只可能发生在 `rename` 前（保留旧快照）或后（已是新快照），不存在半截状态。
- 重启时 `store.NewFileStore(path)` 重新加载快照，冻结额、捕获进度、退款与结算状态、
  幂等记录、审计事件全部恢复；已确认操作不丢失，未完成事务视为未发生、可安全重试。

## 配置项（`config.Config`）

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `store_path` | 持久化快照文件路径，重开即恢复 | `./data/payment-store.json` |
| `sweep_interval` | 到期自动释放扫描间隔（字符串如 `5s`；`<=0` 关闭定时器，可手动 `SweepExpired`） | `5s` |
| `default_preauth_ttl` | 默认预授权有效期 | `24h` |
| `amount_unit` | 金额单位说明，全系统统一为整数分 | `minor-unit (integer cents)` |

JSON 中时长使用 Go duration 字符串（如 `"250ms"`、`"2h"`）。

## 运行与测试

```bash
go run ./cmd/demo          # 并发捕获 + 结算 + 到期释放冒烟演示
go test ./... -race        # 全部自动化测试（含竞态检测）
go vet ./...
```

测试覆盖：并发捕获不超额、重复提交回放首次结果（含失败回放）、部分捕获/撤销边界、
部分/超额退款边界、到期自动释放与留痕、金额调整并发确定性、结算幂等与不重复计入、
结算与退款并发最终金额一致、重启崩溃恢复、存储原子提交/回滚、定时器停止。
