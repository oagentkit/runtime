# runtime

Agent 进程运行时 —— oagentkit 模块矩阵 **tier 0**（基础层）。

```
go get github.com/oagentkit/runtime
```

| 包 | 职责 |
|---|---|
| `breaker` | 三态熔断器（closed → open → half-open → closed/reopen），带半开探针时限与陈旧探测失效 —— **待退役，见下** |
| `pacer` | 流式增量节流器：LLM token 级分片攒够窗口再发，避免逐条发布洪泛事件通道 |
| `progress` | 泛型事件总线（`Bus[T]`，发布订阅 + 慢消费者丢弃计数） |
| `procx` | 子进程最小环境构造（`ChildEnv`）+ 执行封装，mcp/workcopy/sast 等 spawn 外部进程共用 |
| `hotplug` | 运行时插拔与原子热替换：`Plugboard` 启停视图 + `Holder[T]` 泛型快照持有点 |
| `hookx` | 外部 hook 拦截协议：在关键拦截点派生子进程，stdin 喂 JSON |

## 与 oekit/concurrency 的分工

并发/流控的主体能力在 `github.com/oekit/concurrency`（GoSafe/gopool/单飞/重试/令牌桶/
滑动窗口/自适应熔断/CPU 降载/攒批/时间轮），本仓**不再重复造**。本仓只承载
oekit 不会做的 agent 场景能力。

⚠️ **包名撞车，务必按语义区分**：

- `oekit/concurrency/progress` = 长任务**阶段进度追踪器**（`NewTracker`/`PhaseConfig`/`PhaseStatus`）
- 本仓 `progress` = 泛型**事件总线**（`NewBus[T]`）

两者语义完全相反，不要混用。

## `breaker` 待退役

`breaker` 与 `oekit/concurrency/breaker` 高度重叠，但**不是同一算法族** ——
后者是 Google SRE 式自适应概率丢弃（`k=1.5`/`minK=1.1`/`WithForcePass`），本仓是三态熔断器。

已在上游提需求补齐三处状态机细节（代绑定 / 探测时限 / 冷却不被延长）：
**https://github.com/oekit/concurrency/issues/17**

上游落地后本包删除，`breaker` 的消费方改依赖 `oekit/concurrency/breaker`。
**在此之前不要删除** —— 消费方仍在 import 它。

## 硬约束

`procx` **不能并入 `tools` 仓**：它同时被 tier1 的 `mcpkit`、`promptx` 使用，
并入 tier2 会造成 tier1→tier2 分层倒挂。

## 开发

```bash
go test ./... -race
go vet ./...
gofmt -l .
```

规范见 [AGENTS.md](AGENTS.md)。

## License

MIT
