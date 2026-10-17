# AGENTS.md — oagentkit/runtime

> Agent 进程运行时，6 个包：`breaker` `pacer` `progress` `procx` `hotplug` `hookx`
> module：`github.com/oagentkit/runtime` · org：[oagentkit = Open Agent Kit](https://github.com/oagentkit)
> 矩阵分层：**tier 0（基础层）**

## 本仓包含

```
breaker/  hookx/  hotplug/  pacer/  procx/  progress/
```

## 硬规则

### 分层与依赖

- 依赖严格自底向上，**禁止反向依赖**。本仓是 tier 0，任何矩阵内模块都不得被本仓依赖。
- 外部依赖仅 `github.com/oekit/concurrency`（只用其 `async`）。**不要在本仓重新实现
  goroutine 池、单飞、重试、令牌桶、滑动窗口、时间轮** —— 那是 `oekit/concurrency` 的职责。

### 与 oekit/concurrency 的边界

| 能力 | 归属 |
|---|---|
| GoSafe / gopool / 单飞 / 重试 / 令牌桶 / 滑动窗口 / 时间轮 | `oekit/concurrency` |
| 概率丢弃式自适应熔断 | `oekit/concurrency/breaker` |
| **三态熔断 + 半开探针时限 + 陈旧探测失效** | 本仓 `breaker`（待退役） |
| **LLM token 流式节控** | 本仓 `pacer` |
| **泛型事件总线** | 本仓 `progress` |

⚠️ **`progress` 语义与 `oekit/concurrency/progress` 相反**：
上游是长任务**阶段进度追踪器**，本仓是**事件总线**。任何按包名对齐的迁移/退役判断都会做错。

⚠️ **`pacer` 与 `oekit/concurrency/ratelimit` 语义不同**：
`ratelimit` 是**准入控制**（允许发不允许），`pacer` 是**聚合缓冲**（攒够窗口再发）。
不是同一语义，不要互相替代。

### 并发安全

- 禁止裸 `go func`：用 `github.com/oekit/concurrency/async` 的 GoSafe 系列（panic 被 recover）。
- 共享状态用 mutex / atomic 保护；包级全局可变状态用 `atomic.Pointer[T]`。
- `hotplug.Holder[T]` / `progress.Bus[T]` 这类泛型容器的并发正确性**必须**有 `-race`
  回归测试兜底。

### 错误处理

- 一律使用 `github.com/oekit/pkg/errors`；业务错误用 BizError。
- 所有 error 禁止 `_` 忽略。

### 日志

- **本仓不引入日志依赖**（tier 0 不该抬地板）。需要观测输出时返回 error 或交给调用方。

### API 设计

- 函数选项模式命名 `XxxOption`（禁止裸 `Option`，多包 co-import 会冲突）。
- 导出符号 PascalCase + godoc 注释；包文档放 `doc.go`。

### `procx` 的分层约束（硬）

`procx` 被 tier1 的 `mcpkit`、`promptx` 使用，**因此必须留在 tier0**。
任何把它并入 tier2 `tools` 仓的提议都会造成 tier1→tier2 分层倒挂，直接违反矩阵不变量。

## `breaker` 退役纪律

本包待退役到 `oekit/concurrency/breaker`，上游需求见
https://github.com/oekit/concurrency/issues/17 。

- **在上游三项（代绑定 / 探测时限 / 冷却不被延长）落地前，不得删除本包** ——
  消费方仍在 import，删了直接构建断裂。
- 落地后：删本包 → 消费方改依赖上游 → 仓数不变，只减代码量。

## 明确不属于本仓的

| 能力 | 归属 |
|---|---|
| 文本/差异/会话/报告 | `github.com/oagentkit/textx` |
| 脱敏/出口围栏/权限/审计 | `github.com/oagentkit/guardx` |
| 提示词组装/转向/文件状态 | `github.com/oagentkit/promptx` |
| DB 队列与选主 | `github.com/oagentkit/queue` |
| 通用 errors/stringx/encoding/mathx | `github.com/oekit/pkg` |
