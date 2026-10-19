# runtime CHANGELOG

> 本文件由 `matrix-tools/scripts/gen_changelogs.py` 从 git 历史生成，按 tag 切段、取每段内的 conventional commit 渲染。

## [v0.7.0] — 2026-10-19

- 杂务(deps)：依赖地板对齐远端最新（concurrency v0.17.1） (#13)

## [v0.6.0] — 2026-10-19

- 杂务(deps)：oekit 依赖升级（concurrency v0.16.0→v0.17.0）

## [v0.5.0] — 2026-10-18

### 新增：runtime 仓建立——Agent 进程运行时（6 包）

从 agentkit 单仓切出 tier0 基础层模块，module 路径 github.com/oagentkit/runtime。
原 runx 仓去掉 breaker 后的主体更名而来（并发主体已上移 oekit/concurrency）。
收编 6 个包：
- breaker     三态熔断器（半开探针时限/陈旧探测失效），待退役到 oekit/concurrency
- pacer       流式增量节流器，LLM token 分片攒窗口再发
- progress    泛型事件总线 Bus[T]
- procx       子进程最小环境 ChildEnv + 执行封装
- hotplug     Plugboard 启停视图 + Holder[T] 快照持有点
- hookx       外部 hook 拦截协议（派生子进程，stdin 喂 JSON）
外部依赖仅 github.com/oekit/concurrency（只用 async），无矩阵内依赖。
README/AGENTS.md 写明与 oekit/concurrency 的分工，以及 progress 撞名但语义相反、
pacer 与 ratelimit 语义不同两条易踩的坑。
门禁：go build / go vet / go test -race 6 包全绿，gofmt 干净。

### 重构：移除本仓的 breaker 实现，迁到 oekit/concurrency/breaker

三态状态机、代绑定、冷却期、探测时限、计数型触发全部改用上游
（WithConsecutiveFailures / WithCooldown / WithProbeTimeout）。
同时升级 oekit/concurrency v0.9.0 -> v0.12.0。
BREAKING：github.com/oagentkit/runtime/breaker 不再存在。迁移方
  import github.com/oagentkit/runtime/breaker
    -> github.com/oekit/concurrency/breaker
注意语义差异：上游三态由「窗口丢弃概率>0 或连续失败达阈值」触发，
而本仓原实现是纯计数型；且 Breakers 多实例注册表改为按 name 取 NewBreaker/GetBreaker。
已知上游缺口：计数型触发只切状态、切不了放行决策
（https://github.com/oekit/concurrency/issues/25），由 llm 仓的准入适配层兜底。
门禁：go build / go vet / go test -race 5 包全绿，gofmt 干净。

- 杂务(deps)：oekit 依赖升级 (#1)
- 杂务(deps)：oekit/concurrency v0.12.0 -> v0.13.1
- 杂务(deps)：oekit/concurrency v0.6.0 -> v0.9.0

- CI：接入 org 共享门禁（规则本体在 oagentkit/.github）
