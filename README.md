# runtime

Agent 进程运行时——LLM token 流式节控/泛型事件总线/子进程最小环境/运行时热插拔/生命周期钩子 —— oagentkit 模块矩阵 **tier 0**（基础层，零仓内依赖或仅依赖同仓包）。

```
go get github.com/oagentkit/runtime
```

| 包 | 职责 |
|---|---|
| `pacer` | 流式增量节流器：LLM token 分片攒够窗口再发，避免逐条发布洪泛事件通道 |
| `progress` | 泛型事件总线 `Bus[T]`（发布订阅 + 慢消费者丢弃计数） |
| `procx` | 子进程最小环境构造 `ChildEnv` + 执行封装，供 spawn 外部进程的包共用 |
| `hotplug` | 运行时插拔与原子热替换：`Plugboard` 启停视图 + `Holder[T]` 快照持有点 |
| `hookx` | 外部 hook 拦截协议：在关键拦截点派生子进程，stdin 喂 JSON |

## 定位

**tier 0** — 基础层，零仓内依赖或仅依赖同仓包。

- 本仓无矩阵内依赖，可被任意层引用。

## 外部依赖地板

- 零外部依赖。

## 与 oekit 的关系

本矩阵（oagentkit，Agent/LLM 编排工具链）**依赖** oekit（Open Easy Kit，Go 微服务工具链）：
`oekit/pkg` / `oekit/concurrency` / `oekit/logx` 等。oekit 是另一套独立矩阵，不是子品牌。

## 开发

```bash
go test ./... -race
go vet ./...
gofmt -l .
```

规范见 [AGENTS.md](AGENTS.md)。

## License

MIT
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