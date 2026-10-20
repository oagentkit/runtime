# AGENTS.md — oagentkit/runtime

> Agent 进程运行时——LLM token 流式节控/泛型事件总线/子进程最小环境/运行时热插拔/生命周期钩子
> module：`github.com/oagentkit/runtime` · org：[oagentkit = Open Agent Kit](https://github.com/oagentkit)
> 矩阵分层：**tier 0**（基础层，零仓内依赖或仅依赖同仓包）

## 本仓包含

```
`pacer/`
`progress/`
`procx/`
`hotplug/`
`hookx/`
```

## 硬规则

### 分层与依赖

- 依赖严格自底向上，**禁止反向依赖**。本仓是 tier 0，只允许依赖 tier ≤ 0 的仓。
- 本仓无矩阵内依赖，可被任意层引用。
- 改 module 路径必须同步：全部 import、go.mod、README 安装行。

### 错误处理

- 一律使用 `github.com/oekit/pkg/errors`；业务错误用 BizError。
- 所有 error 禁止 `_` 忽略。

### 并发安全

- 禁止裸 `go func`：用 `github.com/oekit/concurrency/async` 的 GoSafe 系列（panic 被 recover）。
- 共享状态用 mutex / atomic 保护；包级全局可变状态用 `atomic.Pointer[T]`。

### API 设计

- 函数选项模式命名 `XxxOption`（禁止裸 `Option`，多包 co-import 会冲突）。
- 导出符号 PascalCase + godoc 注释；包文档放 `doc.go`。

### 日志

- **本仓不引入日志依赖。** tier0 加日志等于给所有下游抬地板；需要观测输出时返回 error 或交给调用方。

### 发版顺序

本仓的 `CHANGELOG.md` 由 `matrix-tools/scripts/gen_changelogs.py` 从 git 历史生成，
**不要手改**。发版顺序的**单一副本**见 org 规则仓
[CONTRIBUTING §发版](https://github.com/oagentkit/.github/blob/main/CONTRIBUTING.md#发版)：
**预写待发版段 → 提交 CHANGELOG.md → 在那个提交上打 annotated tag**。
tag 必须落在 CHANGELOG 提交**之后** —— 下游 `go get` 拿到的是 tag 快照，
快照里的 CHANGELOG.md 若缺自己那一段，发布说明就是残缺的。

⚠️ 无参重生成 CHANGELOG 会**吞掉待发版段**：生成器只按已存在的 tag 切段，
`--next-version` 是唯一能产出待发版段的开关。**预写之后、打 tag 之前，
不要跑无参重生成。**

本仓是 tier 0 基础层，全矩阵都依赖它 —— tag 快照里的 CHANGELOG 残缺会直接传导到所有下游。

## 仓特有纪律

### `progress` 撞名但语义相反（最容易踩）

- `oekit/concurrency/progress` = 长任务**阶段进度追踪器**（`NewTracker`/`PhaseConfig`/`PhaseStatus`）
- 本仓 `progress` = 泛型**事件总线**（`Bus[T]`，发布订阅 + 慢消费者丢弃计数）

**任何按包名对齐的迁移/退役判断都会做错。对齐必须按语义。**

### `pacer` ≠ `ratelimit`

`oekit/concurrency/ratelimit` 是**准入控制**（允许发/不允许发），本仓 `pacer` 是**聚合缓冲**
（攒够窗口再发）。不是同一语义，不要互相替代。

### `procx` 的分层硬约束

`procx` 被 tier1 的 `mcpkit`、`promptx` 使用，**必须留在 tier0**。
把它并入 tier2 的 `tools` 会造成 tier1→tier2 分层倒挂，违反矩阵不变量。

### `breaker` 已退役

原三态熔断器已迁到 `oekit/concurrency/breaker`，由 `llm` 仓的准入适配层消费。
本仓不再有熔断实现 —— **不要在本仓重新实现熔断**。

上游已知缺口：`WithConsecutiveFailures` 只切状态、切不了放行决策
（实测阈值 3、第 12 次才真正拒绝），见 https://github.com/oekit/concurrency/issues/25 。
`llm` 侧用本地计数型准入否决兜底，上游修好后删掉那段即可。

## 明确不属于本仓的

| 能力 | 归属 |
|---|---|
| 文本/差异/会话/报告 | `github.com/oagentkit/textx` |
| 事件总线/流控/子进程/热插拔/钩子 | `github.com/oagentkit/runtime` |
| 脱敏/出口围栏/权限/沙箱解析/审计 | `github.com/oagentkit/guardx` |
| DB 队列与选主 | `github.com/oagentkit/queue` |
| 通用 errors/stringx/encoding/mathx | `github.com/oekit/pkg` |
| goroutine 池/单飞/重试/令牌桶 | `github.com/oekit/concurrency` |
