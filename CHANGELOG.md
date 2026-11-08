# runtime CHANGELOG

> 本文件由 `matrix-tools/scripts/gen_changelogs.py` 从 git 历史生成，按 tag 切段、取每段内的 conventional commit 渲染。

## [v0.12.2] — 2026-11-08

### 依赖地板：oekit 基座追平 BOM v0.2.9——pkg v0.37.4 / logx v0.8.10 / concurrency v0.28.4

oekit BOM 兼容地板门禁（oekit/oekit#163）对齐轮；无代码变更。

## [v0.12.1] — 2026-11-08

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.28.3

oekit/concurrency v0.28.3（审计二轮 bugfix：supervisor 锁内回调死锁/滑窗预占记账/
poll 退避归零/budget 存档拷贝等）落地，地板同步追平（matrix-tools#188 发版链）。
兼容性实测：go build / go vet / go test -race -count=1 ./... 全绿，go mod tidy -diff 无漂移。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.28.2

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.28.1` | `v0.28.2` |
| `github.com/oekit/pkg` | `v0.37.1` | `v0.37.3` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.28.1

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.28.0` | `v0.28.1` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(build)：补统一 Makefile——本地 ci 门禁与共享 CI 同口径（agentkit#138）

Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>


## [v0.12.0] — 2026-11-07

### 新增(procx)：windows 进程树支持——Job Object 落地，解除 windows/amd64 编不过

本包此前只有 POSIX 实现：newGroupCmd 直接写 syscall.SysProcAttr{Setpgid: true}，
pgidKill 直接调 syscall.Kill(-pid, sig)。两处都是 unix 专有符号，于是任何
GOOS=windows 的下游都编不过：
  process.go:163: unknown field 'Setpgid' in struct literal of type syscall.SysProcAttr
  process.go:171: undefined: syscall.Kill
这个缺口在宿主侧已经暴露过（agent-harness 的交叉构建门禁反向验证时撞上），
而下游能做的只有「显式声明不支持 windows」——把缺口写清楚，不是关掉它。
# 做法：机制按平台拆开，调用面只认三个动作
procx 的承诺是「超时/取消后整棵进程树一起收场」，而不是只杀直接子进程。
这条承诺在两个平台上必须用不同机制兑现，故拆成：
  group.go          调用面：killMode（graceful/forceful）+ processGroup 接口
                    （attach / kill / close）+ errGroupGone 哨兵
  group_unix.go     Setpgid 建组 → TERM 整组 → 宽限 SIGKILL（原逻辑逐字搬运）
  group_windows.go  Job Object 装树 → TerminateJobObject 整树终止
process.go 只认 attach/kill/close：Cancel 接管、WaitDelay 宽限、Wait 失败后
兜底清树、defer close 收口——平台差异不再泄进主流程。
# Windows 为什么用 Job Object
Windows 没有 pgid、没有 SIGTERM/SIGKILL，也没有 syscall.Kill。按 pid 逐个
Kill 拿不到孙进程，而孙进程残留正是本包要防的那类缺陷（包装器类命令常把
真实工作进程守护化到后台，TestRunLeaderExitsWithDaemonGrandchild 锁的就是
这个场景）。Job Object 是 Windows 上唯一能把「一棵树」当作一个单元终止的
内核机制，故 attach 时 CreateJobObject + AssignProcessToJobObject，终止时
TerminateJobObject。
# 如实声明的语义降级（不假装等价）
1. 没有优雅终止档。Windows 无 SIGTERM 等价物，killGraceful 退化为整树强制
   终止：「TERM → 宽限 3s → KILL」变成「立即终止」。killGrace 仍生效，但它
   兜的只剩 WaitDelay 的管道收口。
2. 不设 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE。那需要 SetInformationJobObject
   与按位布局的结构体（32/64 位大小不同），而本仓 CI 不在 Windows 上跑，
   无法验证布局正确性；布局写错时 SetInformationJobObject 只回一个 bool，
   静默失败比不做更危险。替代做法是正常路径与错误路径都显式
   TerminateJobObject（kill / close），覆盖「孙进程残留」这一实际失效模式。
   宿主进程被强杀时 Job 不会自动清树——这是与 unix 侧的已知差别。
# 验证
  CGO_ENABLED=0 go build ./...                           （darwin，绿）
  CGO_ENABLED=0 GOOS=windows  GOARCH=amd64 go build ./... （绿，改前红）
  CGO_ENABLED=0 GOOS=linux    GOARCH=arm64 go build ./... （绿）
  CGO_ENABLED=0 GOOS=windows  GOARCH=amd64 go vet ./procx （绿）
  CGO_ENABLED=1 go test ./procx -count=1 -race            （绿，unix 行为未变）
  CGO_ENABLED=1 go test ./...    -count=1                 （全仓绿）
新增 procx/group_windows_test.go 并**如实标注**：这两个用例没有在真机上跑过
（本仓 CI 只有 ubuntu runner），编译期由 GOOS=windows go vet 保证，运行期
留给第一个在 Windows 上跑本仓的人。一条没跑过的用例不该被当成证据。
无新增依赖：Job Object 走 stdlib syscall 的 LazyDLL，故 tier0 的依赖地板不变。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.28.0

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.27.0` | `v0.28.0` |
| `github.com/oekit/pkg` | `v0.36.0` | `v0.37.1` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。


## [v0.11.0] — 2026-11-07

### 新增(notifyx)：官方 hook shim——notifyx/hook 归一化映射表+跨进程节流+notifyx-hook 命令行（#36）

notifyx 的 Go 库契约已标准化，但平台挂点是 stdin-JSON/argv 的 shell 协议，
消费方（octopus script/octopus-notify.sh 实证）在 bash 里二次实现映射表/
ack 轮询/节流。本提交补上挂点侧官方对称物：
- notifyx/hook 子包：Normalize 把平台载荷归一化为 notifyx.Payload，映射表
  是主包 doc.go 那张表的可执行单源（hook_test.go 逐条同测钉两边）；事件名
  归形后查精确表，未命中走「类/failure 类」兜底，仍命中不了显式报错。
- Throttler：文件时间戳跨进程节流，键 agent+session+event（比实证脚本的
  agent+会话收紧一维），窗口可配（缺省 5s），fail-open。
- cmd/notifyx-hook：一行接入。ack 轮询直接组合主包 PollAck+HTTPAckEndpoint
  （缺省同款：间隔 2s、连续失败上限 5）。退出码契约：0 送达/节流跳过/
  approve/done，2 输入错误，3 外发失败，4 ack 轮询失败/超时，5 deny。
用法：
  notifyx-hook --agent zcode --url https://notify.example/hook \
      --header "Authorization: Bearer $TOKEN" < payload.json
安装：go install github.com/oagentkit/runtime/notifyx/hook/cmd/notifyx-hook@latest
close #36

- 杂务(deps)：依赖地板追到最新——github.com/oekit/pkg v0.36.0

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/pkg` | `v0.35.2` | `v0.36.0` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。


## [v0.10.0] — 2026-11-07

### 新增(procx)：新增 InheritAllExcept——可信父进程场景不再靠白名单补枚举（#26）

feat(procx): 新增 InheritAllExcept——可信父进程场景（SDK 包装用户自己的 CLI）不再靠白名单补枚举（#26）
## 根因
`procx` 的子进程环境纪律是**白名单**（`ChildEnv` + `baseEnvAllow`），那条纪律
服务于「cli agent / MCP server 在用户工作目录执行任意代码」——全量继承等于把
宿主凭证交给待执行任务。但它只覆盖了一种威胁模型。
另一种是 **SDK 包装用户主动发起的 CLI**：go-git-platform 的 `gitbackend` 替用户
操作**用户自己机器上的仓库**，父进程环境**就是**用户的信任上下文。这类场景用
白名单是**补不全**的，不是「多补几个就够」：
- `GIT_*` 是开放式命名空间（`GIT_CONFIG_COUNT` + `GIT_CONFIG_KEY_n` / `VALUE_n`、
  `GIT_AUTHOR_*`、`GIT_SSH_*`、`GIT_TRACE*`…），组合式命名静态列不到第 n 项；
- `SSH_AUTH_SOCK`（agent 转发认证）、`GIT_ASKPASS` / `SSH_ASKPASS` / 桌面
  credential helper（经 PATH 发现）——凭据助手链是活的；
- locale 族（`LC_*`）、各类 SDK 自己的代理与配置变量。
漏一项的形态是「认证莫名失败」或「读到别人的配置」，**不是编译错误**，也不会有
任何门禁红。
## 修法
新增独立导出函数（**不是** `ChildEnv` 的 mode 参数）：
```go
func InheritAllExcept(except []string) []string
```
语义 = 「用户在自己的 shell 里跑这条命令」+ 两条既有纪律。
### 为什么不做成 `ChildEnv(extra, mode)` 开关
`ChildEnv` 的白名单是安全纪律的单一事实源；给它加一个 `Mode: InheritAll` 旁路，
等于让 agent 场景**一行就能漏掉全部凭证**。独立函数让两种威胁模型在调用点字面
可见：`procx.Run` 仍是白名单；`exec.Cmd{Env: procx.InheritAllExcept(nil)}` 的
继承语义读代码即知。审计面因此分离。
### except 的口径
- 认**变量名**；含 `=` 的项按 `=` 之前取名（`"GIT_TERMINAL_PROMPT=1"` 与裸名
  同义）——避免「想剔掉它」与「想改它的值」因写法不同而行为不同；
- **剔除优先于追加**：被剔除的项不会被尾部缺省项加回来；
- 父进程已有同名项时保留用户的取值（`CI=0` 不该被悄悄改成 `CI=1`），
  且每个 key 至多出现一次（映射 environ 同名多项、子进程取值依 libc 实现而异的
  同族问题，runtime#31）；
- `nil` 与空切片等价 = 全量继承 + 两条纪律。
`ChildEnv` / `Run` / `baseEnvAllow` **逐字未变**，既有调用方零改动。
## 反向验证（5 条，均实测红）
| 注入 | 判据 | 报错 |
|---|---|---|
| 退化成白名单实现（`return ChildEnv(except)`） | 继承语义 | `SSH_AUTH_SOCK 应被继承且唯一: n=0 vals=[]` |
| 父进程循环不剔除 | except 生效 | `被剔除的变量仍在（1 次）` |
| 缺省追加不看剔除 | 剔除优先于追加 | `GIT_TERMINAL_PROMPT 被剔除后不得再被缺省追加: n=1 vals=[0]` |
| 缺省追加无视父进程已有值 | 用户值优先 | `CI 应恰好一次，实得 2 次: [0 1]` |
| except 不做 `=` 归一 | 写法等价 | `GIT_TERMINAL_PROMPT 被剔除后不得再被缺省追加: n=1 vals=[1]` |
首条**成对**断言：同一变量 `SSH_AUTH_SOCK` 在 `InheritAllExcept` 里必须在、在
`ChildEnv` 里必须不在——否则「它区别于白名单」这件事没被证明，`baseEnvAllow`
哪天被顺手加了这一项，单边断言照样绿。
另有一条**结构性不变量**（比逐个变量断言更强）：`InheritAllExcept(nil)` 的结果
必须覆盖父进程 `os.Environ()` 的**每一个** name（按名去重后）——约束的是
「没有遗漏」，不是「我想到的那几个在」。
## 落地范围
只加函数与文档。`procx.Run` 的缺省行为不变（仍走白名单）：把「继承」变成默认值
会让所有既有调用方静默降级安全姿态，那才是真正的破坏性变更。

## [v0.9.0] — 2026-11-07

### 新增(hookx)：新增 Runner.FailClosedEvents——安全事件上 hook 失败不再静默放行（#34）

包文档写着「需要强一致的场景由调用方在 OnError 里自行收紧」，但
```go
out, err := runHook(ctx, h, payload)
if err != nil {
    if r.OnError != nil { r.OnError(h, err) }
    continue                    // ← 失败即「无意见」，调用方无从干预
}
```
`OnError func(h Hook, err error)` 是**纯观察者**：它拿不到结果、也无法中断。
于是对 PreToolUse / PermissionRequest 这类安全 hook，二进制缺失、超时、坏 JSON
全部静默等于「放行」。文档承诺的那条收紧通道在机制上不存在——不是调用方不肯
写，是写了也没用。
新增 `Runner.FailClosedEvents []Event`：列出的失事件类型上，hook 执行失败落一条
**保守判定**（`BlockRequested` + `PreventContinuation` + 权限事件上
`PermissionBehavior="deny"` + 说明成因的 `StopReason`）。两条通道并存：`OnError`
照旧上报（调用方既要判定也要成因）。
三条设计取舍：
1. **缺省不开**。fail-open 仍是缺省——扩展点故障不该无差别阻断业务，这个原设计
   成立；需要安全的场景显式列出事件。列表**逐事件生效**，不是全局开关。
2. **Stop 上刻意不产生判定**。该事件 block 的语义是反的（否决停止、继续跑），
   一次执行失败不足以支撑「必须继续」这种强结论——把失败强扭成继续跑与「保守」
   正好相反。此事件上的失败仍经 OnError 可见，不静默。
3. **与既有 deny 单调同向**。失败落 deny 不会顶掉更严的判定，也不会被正常
   hook 的 approve 盖掉（`merge` 的 deny 单调保证）。
## 反向验证
把 `Run` 里那一句 `merged.merge(failClosedResult(...))` 去掉（即退回
「失败即 continue」）：
```
--- FAIL: TestFailClosedDeniesSafetyEvent/pretool-exit3
    失败应判 deny（否则等于静默放行）: {… PermissionBehavior: …}
--- FAIL: TestFailClosedDeniesSafetyEvent/pretool-badjson
--- FAIL: TestFailClosedDeniesSafetyEvent/permission-exit3
--- FAIL: TestFailClosedKeepsHealthyHookVerdict
    失败 hook 的 deny 应压过 approve: {… PermissionBehavior:allow …}
```
最后一行的 `PermissionBehavior:allow` 正是缺陷形态：一个 hook 放行、另一个 hook
干脆没跑成，结果读作「放行」。恢复后绿。
## 用例
新增 `hookx/failclosed_test.go` 六条（全部端到端 `Runner.Run`，hook 用真实 sh）：
- `TestFailClosedDeniesSafetyEvent`：三个子用例（非零退出 / stdout 非 JSON /
  PermissionRequest 上非零退出）——两种失败形态都覆盖。
- `TestFailOpenIsDefault`：**成对对照**——不列事件时保持无意见，否则「一律
  fail-closed」也能让上一条绿，而扩展点故障开始阻断全部业务。
- `TestFailClosedOnStopAddsNoVerdict`：Stop 上不产生判定，但 OnError 仍被调用。
- `TestFailClosedStillReportsOnError`：两条通道并存。
- `TestFailClosedOnlyAffectsListedEvents`：只列 PreToolUse 时
  PermissionRequest 仍 fail-open。
- `TestFailClosedKeepsHealthyHookVerdict`：失败落 deny 压过另一 hook 的 approve。
包文档与 `Runner` 字段文档同步（并删掉「由调用方在 OnError 里自行收紧」这句做了
不到的话，改成指向 FailClosedEvents）。
`go vet` + `go test -race -count=1 ./hookx/` 绿。

### 修复(notifyx)：Close 限期到点后真正丢弃并计量——「未排空部分按丢弃计」不再两头不成立（#37）

## 根因
`Notifier.Close` 的文档写着「超时未排完的部分按丢弃计」，实现却是：
```go
case <-ctx.Done():
	return ctx.Err()
```
**既没丢弃，也没计数**，后台 `deliverLoop` 照常把队列里剩下的件投递成功。
调用方据此判定「未送达」而实际送达了——与它据此判定「已送达」而实际丢弃的
一样糟：契约必须只有一种读法。
## 修法
新增 `abort` 信号（Close 的限期耗尽时置位）：
- `deliverLoop` 取到货时若已 abort ⇒ 不再投递，逐件计入丢弃并触发 `OnDrop`；
- `deliver` 每次尝试前、以及退避等待中检查 abort，命中则中止本次投递并交回
  调用方计入丢弃（`sleep` 改为可中断，返回 `false` 表示限期已到）；
- `Close` 超时路径自己把队列排干并记账。
### 为什么记账不能交给投递循环
第一版实现把「排空剩余并计数」交给 `deliverLoop`，用例当场红：
循环是**串行**的，正卡在一次不理 ctx 的 sink 调用里时，它连下一次取件都做不到，
队列里剩下的件就永远不会被计——正是 issue 报的「既不丢弃也不计数」。
故由 `Close` 自己 `for { p, ok := <-n.queue }` 排干。channel 的每次接收只归
一个接收者，与循环并发排空不会重复计数：循环取到的那件走它自己的 abort 分支，
`Close` 取到的计在超时路径里。
### 「关闭态不打断重试」仍然成立
正常 Close（排空前 ctx 未到期）不会置位 `abort`，已入队的件照声明的重试契约
走完。**只有**限期已耗尽时才打断——那一刻契约已宣告这些件按丢弃计，继续重试
只会让 Close 返回之后还有成功投递。
## 用例
新增 `notifyx/close_drain_test.go`：
1. **`TestCloseTimeoutDropsRemaining`**（判别式）——5 件入队，sink 卡住首件，
   用**已取消**的 ctx 调 Close：断言剩下的 4 件计入 `Dropped`、`OnDrop` 收到
   4 次、此时 `Delivered()==0`；放行在飞件后 `Delivered()==1`、
   `sink 只收到 1 件`、`Delivered+Dropped==5`（账平）。
2. **`TestCloseWithinDeadlineDrainsEverything`**（成对对照）——限期充裕时
   5 件全部送达、`Dropped()==0`。否则「一律放弃」也能让上一条绿，
   而正常停机开始丢通知。
## 反向验证
退回「超时只返回 ctx.Err()」：
```
--- FAIL: TestCloseTimeoutDropsRemaining (3.00s)
    close_drain_test.go:76: 超时等待：剩余 4 件计入 Dropped
```
## 验证
`go test ./...` 全绿；`go test -race ./notifyx/` 绿。
关联 oagentkit/runtime#37

### 修复(notifyx)：Emit 与 Close 的竞窗封死——closed 检查与入队在锁内，「closed 后一律丢弃」成为真承诺（#28）

`Emit` 是 check-then-act：
```go
if n.closed.Load() { n.drop(p, errClosed); return }
select {
case n.queue <- p:   // ← closed=false 后、send 前，Close 插入 close(n.queue)
default:             //    select 的 send case 对已关闭 channel 照样 panic
}
```
而 `Close` 直接 `close(n.queue)`。两者之间没有同步 ⇒ `send on closed channel`
**进程级 crash**。包注释宣称的「closed 置位后 Emit 一律丢弃（防 panic）」当时
并不成立——那条注释描述的是意图，不是事实。
## 改法：检查与入队同一把锁，关闭也在锁内
- 新增 `sendMu sync.RWMutex`。`enqueue` 在**读锁**内做「closed 检查 + 非阻塞
  send」；`Close` 在**写锁**内做「置 closed + close(queue)」。竞窗不可达。
- 抽 `enqueue(p) error` 作为唯一入队口，返回 `errClosed` / `errQueueFull` 供
  两个入口分别计数；`Emit` 的「永不阻塞主流程」由 `select + default` 成为结构
  性事实，不再依赖调用点自觉。
- `EmitWait` 改为「enqueue 失败 → 等 `space` 信号 → 重试」的循环：
  - **等待期间不持锁**——否则一个被慢 sink 堵住的 `EmitWait` 会让 `Close` 卡在
    锁上，连带 `Close` 的 ctx 限期失效（比 panic 更隐蔽）；
  - `space` 是容量 1 的信号 channel，`deliverLoop` 每取走一件非阻塞投一个；
    事件驱动，不轮询睡眠，唤醒后重试即可（信号是「可能有空位」，不是计数）。
## 顺带修掉同一生命周期的挂死
`Close` 的 `done` 由投递循环关闭，而循环是**惰性启动**的：从没 Emit 过就
`Close` ⇒ 等一个永不关闭的 `done`，**永久挂死**。`Close` 现在在关闭队列后补一次
`startLoop()`（`sync.Once`，与并发 Emit 的启动互斥），循环起来看到已关闭的空队列
即排空退出。这是写并发用例时撞上的真实挂死路径，不是假想。
## 反向验证
把 `Emit` 退回 check-then-act、`Close` 退回锁外 `close`，并发用例 `-count=30`：
```
panic: send on closed channel
```
**逐字复现 issue 描述的崩溃**。恢复后 `-count=3` 全绿。
## 用例
新增 `notifyx/concurrency_test.go` 四条：
- `TestConcurrentEmitDuringCloseDoesNotPanic`：8 个 emitter × 300 件与 `Close`
  并发（同一 `start` 通道放行，让竞窗真的落在「已过检查、尚未 send」）。断言
  ① 不 panic；② **`Delivered + Dropped == 调用次数`**（不漏账——漏账意味着某件
  既没送达也没计入丢弃，正是竞态吞件的形态）；③ `Close` 之后 Emit 确实丢弃。
- `TestCloseWithoutEmitReturns`：无 Emit 的 `Close` 必须返回（挂死回归），重复
  `Close` 幂等。
- `TestEmitWaitUnblocksOnClose`：`EmitWait` 不再向已关闭队列发送，也不把调用方
  永久挂住。
- `TestEmitWaitHonorsCtxWhenQueueStuck`：队列永远腾不出空间时按 ctx 收口且计入
  丢弃。构造是确定性的——先等 sink 真正开始处理第一件（队列必空、投递 goroutine
  必堵住），再填满一格，`EmitWait` 必然进「等 space」分支（第一版用两次 `Emit`
  就断言「队列已满」，是脆的时序断言，已改）。
`go vet` + `go test -race -count=3 ./notifyx/` 绿。

### 修复(hookx)：权限判定在家门口也要 deny 单调——continue:false 的 deny 不再被 approve 顶掉（#29）

`processOutput` 对同一个 hook 的输出依次跑
```go
applyContinueFlag(event, out, &res)  // continue:false → PermissionBehavior="deny"
applyDecision(event, out, &res)      // decision:"approve" → 覆盖成 "allow"
```
两个字段写**同一个 res**，后置的 approve 直接顶掉先置的 deny。而跨 hook 归并
（`merge`）是 deny 单调的——**同一份语义在两个层级上方向相反**，且家门口那个
方向不安全：一个同时说「别继续」和「放行」的自相矛盾输出，被静默解析为放行。
改三处：
1. **`setPermission` 成为 `PermissionBehavior` 的唯一写入口**，allow 不得覆盖
   已置的 deny。`applyContinueFlag` 与 `applyDecision` 都走它，两层从此同口径：
   权限面只向保守方向收敛。
2. **decision 大小写不敏感**（`strings.ToLower(strings.TrimSpace(...))`）。
   `"BLOCK"` / `"Approve"` 是常见手写形态，原实现精确匹配落空 ⇒ 静默忽略 ⇒
   hook 想拦、库当没看见。这不是「容忍脏输入」而是安全方向：**丢掉的
   `BLOCK` 等于丢拦截面**。
3. **契约外的取值经 `Runner.OnError` 上报**。`processOutput` 改为返回
   `(RunResult, error)`，`Run` 里结果照常参与归并、错误转 OnError——静默忽略
   正是这次要消灭的形态。取值为空仍是「无意见」，不算越界；越界值**不猜语义**
   （不置任何权限/阻断位），只让它可见。
## 反向验证
三处各自单独回退，都红：
1. 去掉 `setPermission` 的 deny 单调守卫——
   ```
   TestContinueFalseBeatsApprove: continue:false 的 deny 被 approve 顶掉了:
       {AdditionalContexts:[] BlockRequested:true PreventContinuation:true
        StopReason:矛盾输出 PermissionBehavior:allow}
   ```
   权限位是 `allow`、阻断位还置着——正是 issue 描述的自相矛盾结果。
2. 退回大小写精确匹配（`decision := out.Decision`）——
   ```
   --- FAIL: TestDecisionCaseInsensitive/BLOCK
       decision="BLOCK" 应按契约识别为 deny+block: {… BlockRequested:false
        PreventContinuation:false PermissionBehavior: …}
   --- FAIL: TestDecisionCaseInsensitive/Block
   --- FAIL: TestDecisionCaseInsensitive/Approve
   ```
   三条全落空 ⇒ 拦截/放行判定整个丢失。
3. 把 default 分支改回静默忽略——
   ```
   TestUnknownDecisionReported: 越界 decision 应上报一次 OnError，实得 0 次
   ```
三条都红过才恢复。
## 用例
新增 `hookx/decision_arbitration_test.go` 六条（都是端到端 `Runner.Run` + 真实
sh hook，不打桩内部函数）：
- `TestContinueFalseBeatsApprove`：issue 复现。
- `TestApproveStillAllows`：**成对对照**——无矛盾时 approve 照常生效，否则
  「一律算 deny」也能让上一条绿而放行能力整体消失。
- `TestDecisionCaseInsensitive`：`BLOCK` / `Block` / `Approve` / 带空白的
  `approve` 四个子用例。
- `TestUnknownDecisionReported`：`"reject"` 上报一次且信息点名取值与字段；
  同时断言不猜语义（不置任何位）。
- `TestContractDecisionNoError`：`""` / `approve` / `block` **不得**触发
  OnError——否则上一条退化成「每次调用都报错」的噪声。
- `TestCrossHookDenyMonotonic`：跨 hook 归并的既有语义回归。
字段文档同步：`Output.Decision` 写明大小写不敏感与越界上报；
`RunResult.PermissionBehavior` 写明「两个层级都单调」。
`go vet` + `go test -race -count=1 ./hookx/` 绿。

### 修复(procx)：lineWriter 重同步改用 IndexByte——超限行之后的完整行不再被整段吞掉（#30）

`lineWriter.Write` 在 overflow 态下用
```go
if i := bytes.LastIndexByte(p, '\n'); i >= 0 {
    w.overflow = false
    w.partial = append(w.partial[:0], p[i+1:]...)
}
```
这是**跳到本 chunk 的最后一个换行**：超限行的行尾与该换行之间的所有内容
（可能是一整批完整事件行）被静默丢弃且不回调。触发面正是注释自己点名的形态
——单行 >1MB（含超大 diff 的 stream-json result 事件）之后，同一 read chunk
里的后续事件行全部丢失，消费方只看到事件流断片，**没有任何信号**。
重写成「按 chunk 内的位置逐段推进」的循环：
- overflow 态用 `bytes.IndexByte` 找**第一个**换行（那是超限行的行尾），
  之后从行尾 +1 起**继续按行处理**，不再跳。
- 顺带修掉同一判定里的第二个吞行面：旧代码的限容判据是
  `len(partial)+len(p) > maxLineLen`，量的是**整个 chunk**——一个含多行、
  总字节超 1MB 但**每行都远小于上限**的 chunk 会被整段丢弃。改为逐行判定
  `len(partial)+i > maxLineLen`（i 为本行行尾偏移），超限的只有那一行。
- 跨 chunk 与被 chunk 边界切开的行保持原行为（partial 拼接），空行仍不回调。
- `Write` 现在始终返回**入参原长度**（io.Writer 契约），先存 `n := len(p)`
  再从各出口返回 n。
## 反向验证
把 `Write` 整体退回旧实现（其余不动）跑本批用例：
```
--- FAIL: TestLineWriterResyncKeepsTrailingLines
    超限行行尾之后的完整行被吞了: got [] want [{"type":"step","n":1} {"type":"result","result":"ok"}]
--- FAIL: TestLineWriterMultiLineChunkBeyondCap
    逐行都在上限内，不应进 overflow
--- FAIL: TestLineWriterChunkInternalOverflow
    got [ok-2 ok-3] want [ok-1 ok-2 ok-3]
--- FAIL: TestLineWriterSingleOversizedLineInChunk
    本 chunk 内已扫到行尾，不应保持 overflow
```
第一条与 issue 的描述逐字对应（`got []`——两条完整行一行都没回调）；
第二、四条是同一处判定的另两个面，旧实现同样红。恢复后绿。
## 用例
新增 `procx/linewriter_test.go` 六条：
- `TestLineWriterResyncKeepsTrailingLines`：**issue 复现**——超限行行尾 + 同
  chunk 两条完整事件行，断言两条都回调且内容逐字一致。
- `TestLineWriterMultiLineChunkBeyondCap`：成对保护——400 条 4KB 行（合计
  ~1.6MB，每行都在上限内）必须全部回调，防止用「整 chunk 超限即丢弃」换修复。
- `TestLineWriterChunkInternalOverflow`：洪泛行收尾后连续两次写入，状态复原。
- `TestLineWriterSingleOversizedLineInChunk`：单行自身超限（行尾在本 chunk 内）。
- `TestLineWriterPrefixSplitAcrossChunks`：行被 chunk 边界切开仍拼回一行。
- `TestLineWriterEmptyLinesSkipped`：空行不回调（既有语义）。
既有 `TestLineWriterFloodCap`（3MB 洪泛后仍能恢复）继续绿。
`go vet` + `go test -race -count=1 ./procx/` 绿。

### 修复(notifyx)：零值 Timestamp 真的补 now——条件写反使「静默丢字段」成为常态（#27）

`Payload.Timestamp` 的契约（`notifyx.go:40` 与包文档）是「Emit 时零值自动补
now」，代码写的是
```go
if !p.Timestamp.IsZero() {   // ← 条件写反：非零才进
    p.Timestamp = time.Now().UTC()
}
```
调用方**不填** Timestamp 才是 Emit 的常规形态——于是常规路径上时间戳永远是零值
时刻，`omitempty` 把 `timestamp` 字段整个序列化掉：下游按时间排序、按窗口过滤、
按事件时刻对账全部失效，而且没有任何信号（字段是「缺」不是「错」）。反过来，
调用方显式带的时刻反而被本地时钟覆盖——两个方向都错。
修法两步：
1. 去掉 `!`；
2. **把两处合并成一份 `stamp(*Payload)`**。`Emit` 与 `EmitWait` 此前各写一遍
   同样的三行，于是一起写反（issue 点名 `:96-97` 与 `:120-121`）。单写一处，
   这类「同一判据两处副本一起漂」的结构性缺陷不会再复发。
非零值一律保留——调用方自带的事件时刻优先于本地时钟。
## 反向验证
1. 把条件写回 `!p.Timestamp.IsZero()`：
   ```
   TestEmitStampsZeroTimestamp: 零值时间戳未补 now——下游按时间排序/过滤全哑
   TestEmitWaitStampsZeroTimestamp: EmitWait 零值时间戳未补 now
   TestEmitPreservesExplicitTimestamp: 显式时刻被改写: got 2026-11-06 … want 2020-03-04 … CST
   ```
   第三条正是「同时修坏另一半」的证据：条件反向之后，显式时刻被本地时钟顶掉。
2. 把 helper 改成无条件覆盖（`p.Timestamp = time.Now().UTC()`）——
   ```
   TestEmitPreservesExplicitTimestamp: 显式时刻被改写 …
   ```
   这条钉住「不要为了补零而吞掉调用方的时刻」。两条都红过才恢复。
## 用例
新增 `notifyx/timestamp_test.go` 四条，全部事件驱动（sink 把载荷转发到 channel
后 `select` 等待，不用 sleep 猜时序）：
- `TestEmitStampsZeroTimestamp`：零值补 now，且落在 emit 前后窗口内、时区为 UTC。
- `TestEmitPreservesExplicitTimestamp`：**成对的反向对照**——显式时刻原样保留。
- `TestEmitWaitStampsZeroTimestamp`：EmitWait 同病同治。
- `TestPayloadJSONKeepsTimestamp`：线格式钉子——序列化结果里真的存在
  `timestamp` 键（`omitempty` 吃掉零值正是缺陷的「静默」机理，只断言结构体
  字段非零抓不到它）。
`go vet` + `go test -race -count=1 ./notifyx/` 绿。

### 修复(hookx)：Stop 事件不再置 PreventContinuation——与「否决停止」语义相反（#33）

`applyDecision` 的 `block` 分支在进 `event == EventStop` 特判**之前**就无条件
`res.PreventContinuation = true`，于是 Stop 事件上两个字段同时为真：
- `PreventContinuation` = 「该事件不允许继续」（字段文档限定
  PreToolUse/PermissionRequest）；
- `StopShouldContinue` = 「否决停止、继续跑」。
**这两个信号方向相反。** 通用处理若按「`PreventContinuation` → 不继续」分派，
Stop 上「要求继续」会被读成「禁止继续」——语义 180 度反转，而且两个字段都
「有值」，调用方无从察觉。
## 改动
`block` 分支与同文件 `continue:false` 的分支改成**逐字同构**：非 Stop 才置
`PreventContinuation` 并收口；Stop 落到否决停止分支。
顺带把 `RunResult.PreventContinuation` 的字段文档补上不变量
「Stop 事件上恒为 false」——文档原来只写「限定 PreToolUse/PermissionRequest」，
读者得自己反推出 Stop 上不可能同时为真是站不住的。
## 反向验证
只把 `PreventContinuation = true` 挪回特判之前（其余不动）：
```
Stop 事件不得置 PreventContinuation: {BlockRequested:true
  PreventContinuation:true StopShouldContinue:true ...}
```
两个子用例红，输出里 `PreventContinuation:true` 与 `StopShouldContinue:true`
并列——正是要消灭的那个自相矛盾状态。
## 用例取舍
三对钉：
- `TestPreventContinuationNeverOnStop`：`decision:block` /
  `continue:false` / `decision:block` + `systemMessage` 三条入口都要
  `PreventContinuation == false` 且 `StopShouldContinue == true`。
  `BlockRequested` 按入口分别断言——`continue:false` 在 Stop 上既有语义**不置**
  该位，本次不改动它，只钉住 PreventContinuation（改了就是另一件事）。
- `TestPreventContinuationStillSetForToolEvents`：**成对的反向对照**——
  PreToolUse / PermissionRequest / PostToolUse 上的 block 必须仍然置位。
  只测「Stop 不再置位」的话，把字段整个删掉也能全绿，而安全阻断面会静默失效。
- `TestStopBlockKeepsAdditionalContexts`：回归，Stop 上 block 的
  `systemMessage` / `reason` 仍要进 `AdditionalContexts`。
`go vet` + `go test -race -count=1 ./hookx/... ./procx/...` 绿。

### 修复(procx)：ChildEnv 尾部缺省项按名去重——「每 key 唯一」不再被自家追加破坏（#31）

函数文档承诺「每 key 唯一，避免 environ 同名两项时子进程取值依实现而异」，
但最后一行 `append(env, "GIT_TERMINAL_PROMPT=0", "CI=1")` **无条件**追加：
`extra` 已给 `CI=0` 时 environ 出现两个 `CI` —— 正是这段注释声称已消灭的形态。
失效形态不是「值错」，是**值不可预测**：子进程 `getenv()` 取首个还是末个依
libc 实现而异，同一份配置在不同平台上跑出不同行为。
## 改动
去重口径统一为**先到先得**：显式给的（字面注入 / 按名透传）压过缺省项；
缺省项只在同名键缺席时补入。这与 `getenv()` 的「首个命中」语义一致，所以
去重**不改变**子进程实际读到的值，只是把「读到哪个」从实现相关变成确定的。
`seen` 顺带兜住 `os.Environ()` 自身可能出现的同名项（同为先到先得），
「每 key 唯一」因此成了无条件成立的不变量，而不是「只要调用方别乱传」的约定。
注意保留的既有语义：字面注入仍**压过**父进程同名值（`literal[name]` 命中时跳过
父进程项），这条有回归用例钉着。
## 反向验证
逐字退回 `return append(env, "GIT_TERMINAL_PROMPT=0", "CI=1")`：
| 用例 | 结果 |
|---|---|
| `TestChildEnvTailDefaultsNotDuplicated` | 红 ×3 —— `CI 出现 2 次（[0 1]）` / `GIT_TERMINAL_PROMPT 出现 2 次（[1 0]）` |
| `TestChildEnvKeyUniqueness` | 红 —— case #1（按名透传 `CI`）、#2（字面 `CI=0`）、#4（两键都覆盖）各报一次重复 |
| `TestChildEnvTailDefaultsKeptWhenAbsent` | **绿**（缺省值仍在） |
| `TestChildEnvLiteralBeatsParent` | **绿**（既有优先级未变） |
## 用例取舍
`TestChildEnvKeyUniqueness` 不看具体哪一项，只看「整份 env 有没有同名键」——
这是把文档承诺本身可执行化，比逐条断言更能防住将来新增的追加点。
`TestChildEnvTailDefaultsKeptWhenAbsent` 是**成对的反向对照**：若去重把缺省项
整个丢了，前一条只会看到「恰好一次」而不会发现 `CI=1` / `GIT_TERMINAL_PROMPT=0`
已经不在——只测重复、不测存在，会放过「全删了」这种「更彻底」的错修。
`go vet` + `go test -race -count=1 ./procx/...` 绿。

### 修复(procx)：截断留痕改在返回前统一盖上——失败/超时路径不再静默丢标记（#38）

`execCLI` 有三条返回路径，`noteTruncated` 只挂在成功那条上：
- 成功 / `waitDelayIsBenign` → `noteTruncated(out, outBuf, maxStdout)` ✅
- 非零退出 / 超时 / 取消 → **裸返 `out`** ❌
`cappedBuffer` 的超限截断在任何等待路径都可能发生，于是失败路径返回的 stdout
已被裁到 `maxStdout` 却没有任何标记 —— 排障时把「截断导致看不到结论」误读成
「命令根本没输出结论」。纯观测面缺陷，但代价落在最需要信息的时刻。
## 改动
不在三个 `return` 处各补一遍，而是**把留痕上提到计算 `out/errStr` 的那一行**：
「任何路径都带标记」成了结构性事实，而不是三处各写一遍的自觉（下一条返回路径
加进来时不会再漏）。三个 `return` 随之回到裸 `out, errStr`。
**stderr 同病同治**：它的上限是 `maxChildStdout`（8MB），此前两条路径都不标记。
同一次改动一并覆盖，否则就是「修了 stdout、留 stderr 静默」的半修。
## 反向验证
逐字退回原实现（只把留痕上提那一行拆回去、`return` 补回 `noteTruncated`）：
| 用例 | 结果 |
|---|---|
| 失败路径 | 红 —— 末尾只有 `aaaa…`，无标记 |
| 超时路径 | 红 —— 末尾只有 `bbbb…`，无标记 |
| stderr 超限 | 红 |
| 成功路径（回归） | **绿**（既有行为未被破坏） |
| 未超限负对照 | **绿**（证明标记不是无条件加的） |
## 用例取舍
- `MaxStdout` 压到 4096 + `head -c 200000`，避免搬真 1MB 数据；
- 超时路径刻意「先刷满 stdout 再长睡」，让截断确定性地发生在超时之前
  （否则可能超时先到、缓冲还没超限，用例偶发绿）；
- `TestTruncationUnmarkedWhenUnderLimit` 是**成对的反向对照**：只有「开着绿」的
  用例等于没钉住 —— 若标记无条件加，前面几条照样过；
- stderr 那条要往 stderr 写 9MB，`testing.Short()` 下跳过。
`go vet` + `go test -race -count=1 ./procx/...` 绿。

### 修复(progress)：退订时关闭订阅 channel，for range 不再永久阻塞（#32）

根因：unsubscribe 只从 subs 表摘注册，channel **永不关闭**。消费方写
`for e := range ch` 时，退订/ctx 取消后 range 永不退出——goroutine 泄漏，
且已入 64 缓冲的事件被钉在无人消费的 channel 上。
旧注释把「不 close」归因为「close 与并发 send 的竞窗不可消除」，
**这个前提是错的**：Publish 遍历 subs 并发送时持 b.mu，unsubscribe 摘注册
与 close 也在 b.mu 内 ⇒ 两者同锁互斥，不存在「send 到已关闭 channel」的窗口。
故改为**持锁 close**。
连带修掉一个隐蔽缺陷：`delete(b.subs, ch)` 原先在 `once.Do` **之外**，
关闭 channel 之前重复 delete 是幂等的、看着没问题；一旦加上 close，
重复调 cancel 就是二次 close ⇒ panic。现在 close(stop)/delete/close(ch)
全部收进同一个 once（重复退订、以及「显式 cancel + ctx 取消」双向收口都只关一次）。
契约变更（已写进 Subscribe 的 godoc）：
- `for range ch` 现在能正常退出，且会先把关闭前已入缓冲的事件取完；
- **select 消费必须检查 ok**（`v, ok := <-ch`）——关闭后该 case 恒可就绪并
  反复给零值，写 `case v := <-ch:` 而不查 ok 的循环会空转。这是 Go 里所有
  可关闭 channel 的通用前提，不是本包的额外要求，但属于**行为变更**，需在
  发版说明里点名。
反向验证（两处，各自只拆一处）：
- 去掉持锁 close ⇒ TestUnsubscribeClosesChannelForRange 红（range 未退出）；
- 把 delete 退回 once 之外（只加 close 忘了幂等）⇒ TestUnsubscribeIdempotentNoDoubleClose
  红，panic: close of closed channel。
用例：`-race` 下跑过并发 Publish + 反复订阅/退订（8 发布者 × 2000 事件 ×
200 次退订），无 send on closed channel；另有「缓冲事件关闭前取完」一条。
既有 TestSubscribeCtxCleanup 按新契约改写——它原先用「收到即失败」判，
而关闭后 `<-ch` 恒可就绪给零值，会把「已关闭」误判成「收到了事件」。

### 修复(procx)：ErrWaitDelay 成功特判补 cctx 条件——本地超时不再被上报为成功（#35）

根因：`errors.Is(waitErr, exec.ErrWaitDelay) && ctx.Err() == nil` 只看**调用方**
ctx。而触发 WaitDelay 的场景（leader 已退出、孙进程仍握 stdout 写端）恰好是
本地 Timeout 最可能到点的时刻：leader 恰以 0 退出时，`ctx` 确实干净，
只有 `cctx`（context.WithTimeout 派生）已 DeadlineExceeded ⇒ 被**超时终止**的
执行走上「exit 0 成功」分支，ErrTimeout 语义整条丢失，调用方以为命令跑完了。
修法：判据抽成具名函数 waitDelayIsBenign，三个条件缺一不可——
waitErr 是 ErrWaitDelay / ctx.Err()==nil / **cctx.Err()==nil**。
非良性一律落 classifyWait，其 `errors.Is(cctx.Err(), DeadlineExceeded)` 分支
给出 ErrTimeout（既有实现，未改）。
为什么抽函数而不是就地加一个 &&：这个竞态要求「leader 退出点与本地超时点重合」，
用真实进程**无法稳定复现**——写出来的 e2e 要么靠 sleep 猜时序（flaky），
要么永远打不中窗口（假绿）。抽成纯判据后可以用构造的 ctx 逐条钉死：
本地超时那条用 `<-cctx.Done()` 等 deadline 真正落定，不靠 sleep。
反向验证：把判据退回只查 `ctx.Err() == nil` ⇒
`ErrWaitDelay + 本地超时已到 = 失败` 立即红（其余四条仍绿，证明不是整包失灵）。
覆盖：双 ctx 干净=成功（npm 类守护化孙进程不可误判为失败）/ 本地超时=失败 /
调用方取消=失败 / 非 ErrWaitDelay=失败 / 多层 %w 包裹的判别。
良性路径的端到端仍由既有 TestRunLeaderExitsWithDaemonGrandchild 覆盖（未改）。

- 杂务(deps)：依赖地板追到最新——concurrency v0.27.0 / pkg v0.35.2

- 测试(procx)：超时截断留痕用例的时间余量给足——400ms 在 -race 并行下会假红

`TestTruncationMarkedOnTimeoutPath` 要求「子进程先写满 stdout、再被本地超时
杀掉」，超时设的是 400ms。在 `-race` + 全量并行的机器上，400ms 不够 `sh` 起来
（`head -c 200000 /dev/zero | tr …` 这条管道还没产出就被进程组 TERM 掉），
stdout 为空 ⇒ 断言「超时路径必须带截断标记」**误判为没留痕**：
```
--- FAIL: TestTruncationMarkedOnTimeoutPath (0.40s)
    truncation_marker_test.go:94: 超时路径的截断 stdout 必须带标记: 末尾 ""
```
单独跑必绿、`go test ./procx/ -race` 跑全量偶红——典型的「时间边距伪装成断言」。
本用例要钉的是**返回路径的留痕**（runtime#38），不是「400ms 内能起一个进程」，
故把余量放到 2s（子进程启动只需几十 ms，余量 ~40x），并把这条理由写进用例注释。
不变式未放松：断言仍是「stdout 必须带截断标记」，只是不再让「子进程没来得及
写」这种与被测行为无关的失败混进来。
`go test ./procx/ -race -count=1` 连跑绿。


## [v0.8.0] — 2026-11-06

### 新增(notifyx)：生命周期事件通知出口——归一化契约 + 可靠外发 + 确认回执轮询

* feat(notifyx): 生命周期事件通知出口——归一化契约 + 可靠外发 + 确认回执轮询（#18）
runtime 已有 hookx（外部拦截→子进程，控制入向），缺对称的通知出向原语
（agent→外部）：octopus 为接入 6 个 agent 平台各维护一份 shell 胶水，事件
翻译散在各平台配置里、归一化契约无单源；无 hooks 机制的平台（minimax）
永远接不进来——运行时原生出口对这类平台是唯一解。
- 归一化事件契约：Event 枚举（task_complete / permission_request /
  input_required / error / session_end）+ 标准 Payload（session_id / title /
  message / project / branch / priority / extras），doc 带各平台挂点映射表
  （zcode Stop / cc notification / codex notify / kimi TOML hooks /
  opencode·mimo JS 插件）
- 可靠外发：Notifier 出站队列 + 指数退避重试 + 单次尝试超时；Emit 永不
  阻塞主流程（队列满丢弃计数 + OnDrop 观测），EmitWait 背压形态供
  「必须送达」的通知；Close 排空在途件（重试契约不受关闭打断）
- HTTPSink webhook 出口（2xx 送达 / 错误带响应尾片段，凭证走 WithHeader）
- 确认回执（可选）：AckEndpoint 抽象 + HTTPAckEndpoint（octopus 契约：
  ack != null 取回 / ack = null 继续等，顶层 null 与 {"ack":...} 双形态
  兼容）+ PollAck（间隔可配、连续失败超限放弃不空转）
- 零新增依赖（async.GoSafe 已有；无日志依赖——观测走 error 返回与回调，
  tier0 纪律）
测试：重试恢复 / 耗尽丢弃+OnDrop / 队列满不阻塞 / Close 排空+关后丢弃 /
EmitWait / ack 三拍收敛 / 顶层 null / 持续失败及时放弃 / 线格式钉子。
全仓 -race 绿。
关联 oagentkit/runtime#18
* style(notifyx): gofmt 对齐 pollConfig 字段
---------
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.25.1

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.24.0` | `v0.25.1` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.24.0

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.23.3` | `v0.24.0` |
| `github.com/oekit/pkg` | `v0.31.2` | `v0.34.1` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>


- 文档(progress)：Subscribe 显式声明「channel 永不关闭，消费方必须自带退出分支」（#20）

两建议取 doc 声明：unsubscribe 时 close(ch) 与 Publish 的并发 send 有
send-on-closed-channel panic 竞窗（Publish 遍历 subs 持的是 channel 引用，
无全局停顿前提下不可消除）——用阻塞风险换 panic 风险不划算，契约写明
（裸 for range ch 会永久阻塞，必须 select ctx.Done / cancel 后退出），
理由进 doc。
关联 oagentkit/runtime#20
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>


## [v0.7.3] — 2026-11-04

### 重构(hookx)：事件正则编译缓存收编 oekit/pkg/cache SingleFlightCache（并发首编译去重，TTL=0 永不过期无常驻清扫协程）；deps 地板 pkg v0.31.2/concurrency v0.23.3

- 7f6b5d0

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.23.2

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.23.1` | `v0.23.2` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.23.1

oekit 基座又发了一版（`concurrency` v0.23.1，2026-10-29 02:19 CST）⇒ 上一轮抬到
v0.23.0 的地板已经落后 ⇒ 再抬一格。
本仓只用到 `concurrency` 的少量 API ⇒ 实测 go build / go vet / go test -race 全绿，
**零行为差异**；go 指令未被顶起。
只改 go.mod / go.sum ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.23.0

oekit 基座发了新版本（pkg v0.31.0 / concurrency v0.23.0 / logx v0.8.0）⇒ 本仓的
依赖地板落后 ⇒ 抬到最新，让**下一次发版**不再带出落后地板（打 tag 时刻的地板
必须是当时的最新——`verify_tag_floor` / `release.py doctor` 都在查这条）。
兼容性实测：本仓只用这些基座包的少量 API ⇒ go build / go vet / go test -race
全绿，**零行为差异**；go 指令未被顶起（本仓的 go 地板不动）。

- 杂务(deps)：依赖地板追平上游本轮新版本——github.com/oekit/concurrency@v0.22.0

本仓 HEAD 的依赖地板已落后于各依赖的最新版本（`verify_tag_floor.py --org oagentkit --ref HEAD` 判红）。
本仓一旦发版，就会把落后地板发出去——而 tag 受 protect-tags ruleset 保护、打完不能重打，
只能靠后续版本补齐。
本版只抬依赖地板，不改任何行为：
- github.com/oekit/concurrency v0.22.0 → v0.22.0
对应 issue matrix-tools#143。


- 文档(runtime)：刷正依赖红线声明——「零外部依赖」→「零第三方依赖」
- 文档(runtime)：修正 procx 使用方的层位描述——mcpkit 是 tier 2 不是 tier 1

AGENTS.md 里「procx 的分层硬约束」一节把 mcpkit 写成 tier 1，
而矩阵单一数据源 modules.json 里 mcpkit 的 tier = 2（层位 t1 → t2 时带 eino + mcp-go 地板）。
这句是**跨仓提及**（谈论别的仓的层位，不是本仓自述），所以不在 matrix-tools#109
新守卫的判据内——但它会随 mcpkit 本次刷正层位而变成可见的不一致，一并修掉。
promptx 仍是 tier 1，不动。
diff 只含这一行。


## [v0.7.2] — 2026-10-21

- 杂务(deps)：依赖地板追平上游本轮新版本——oekit/concurrency v0.19.0

## 下游要做什么
**`go mod tidy`**。地板抬高对下游是隐式 breaking：不跑就报
`updates to go.mod needed`，且报错不提真正原因（**不是本仓的 API 变了**）。
本仓**自身代码零改动**——只看本仓的 diff 看不出这是一次地板抬高。
## 抬了哪几块、为什么无行为差异
**`github.com/oekit/concurrency` v0.18.0 → v0.19.0**
上游 diff 只动了 2 个包：
- `retry`：**抖动 panic 修复**——`rand.Int63n` 的契约是 n > 0，而「退避幅度为 0」
  是生产可达路径（cfg.Delay=0 / DelayHint 返回 0 / float64→int64 溢出成负数），
  旧实现在这些路径上直接 panic。改为钳到 [0, MaxInt64]。
- `syncx`：新增全局在飞闸门原语（`Gate`）+ 说明同步。
**`breaker` 与 `async` 两个包一字未动。**
**本仓实际 import 的 `oekit/concurrency` 子包：async **
与上游改动相交的：**（无）**
## 门禁
```
go build / vet / test -race   ✅
gofmt -l .                    ✅ 空
```

- 杂务(deps)：oekit 依赖地板抬到 v0.18.0

oekit 姊妹矩阵的依赖地板对齐到最新：
- github.com/oekit/pkg        v0.17.0 → v0.18.0
- github.com/oekit/concurrency v0.17.1 → v0.18.0
- github.com/oekit/logx       已是最新 v0.7.1，不动
oekit v0.18.0 的两个发布都是**纯增量**，无破坏性变更：
- pkg：jsonrepair 补抽数组原语 ExtractArray；
- concurrency：新增 `DoValue[T]`（带返回值的重试），`Do` 改为委托给它——
  退避序列、抖动、MaxDelay 封顶、DelayHint 优先、Retryable 门控、ctx 取消
  **只有一份实现**（此前这类调用方只能自己重写一遍退避，且常丢掉 DelayHint 语义）。
依据 oagentkit/matrix-tools#44 的还债流程：依赖地板对齐到发版时刻的最新。
本仓本轮不发 tag（不在发版集内），地板在 main 上就位，下次发版自然带上。


- 文档(AGENTS)：补「发版顺序」小节，指向 org CONTRIBUTING 单一副本

13 个业务仓的 AGENTS.md 此前对发版顺序零覆盖（只有 textx 有个
「### 版本与发版」标题，但里面只讲 SemVer 与 module 路径），
发版顺序只写在 org 规则仓的 .github/CONTRIBUTING.md 里，单仓读者看不到。
本次只加**指针 + 本仓相关提醒**，runbook 本体保持**单一副本**
（CONTRIBUTING §发版），避免 13 份副本各自漂移。
覆盖四条：
- CHANGELOG.md 由 matrix-tools/scripts/gen_changelogs.py 从 git 历史生成，不要手改
- tag 必须落在 CHANGELOG 提交之后（下游 go get 拿到的是 tag 快照）
- 警告：无参重生成会吞掉待发版段，--next-version 是唯一能产出待发版段的开关
- 本仓相关提醒（见正文）

- 文档(changelog)：按新生成器规则重生成 CHANGELOG.md

生成器修复（oagentkit/matrix-tools#43）落地：
① `[round-N] ` 前缀被剥离 → conventional commit 解析不再失效，
   **commit body 保留**（此前整段被丢弃，发布说明只剩标题）；
② 非 conventional 提交也保留 body；
③ `--check` 识别「待发版段」（预写了但还没打 tag 的那段）。
本 PR 恢复的是**本就在 commit body 里**的正文（迁移步骤、代码块、
判据锚点）——此前被 `[round-N] ` 前缀导致的解析失败丢弃了。
**没有任何发布说明内容被删除**：删除的行都能在新增行里找到对应
（规范化掉 `(#N)` 与 `[round-N] ` 前缀后逐行比对）。
关联：oagentkit/matrix-tools#43、oagentkit/matrix-tools#40


## [v0.7.1] — 2026-10-20

### 修复(go)：tier0 基础包地板降到 go 1.24.0，CI go-version 同步到 1.24

本仓是 tier0 基础包（零重量级第三方地板，可被零依赖核心安全依赖），
但 go 指令声明的是 1.26.0——地板被无谓抬高，把 Go 1.24 的用户挡在门外。
## 依据
**依赖链全是 1.24 的包**：本仓唯一依赖 <base>（go 1.24.0），
依赖链上没有任何要求更高版本的包。
**代码本身兼容 1.24**：用真实 go1.24.0 工具链（`GOTOOLCHAIN=go1.24.0`）
build + test 全绿——不是推断，是实跑。
**门槛是 go 指令，不是 toolchain**（编译器实证）：
- `go 1.28.0` → 被拒：`go.mod requires go >= 1.28.0 (running go 1.27.1; GOTOOLCHAIN=local)`
- `go 1.24.0` + `toolchain go1.28.0` → 通过
## toolchain 行的取舍：删掉
toolchain **不构成门槛**（上面已实证），但 `toolchain go1.27.2` 会让 `go` 命令
**自动下载** 1.27.2，与「最低支持版本是 1.24」的精神相悖——用户以为自己用
1.24 在编译，工具链却被悄悄换成了 1.27.2。故删除该行。
这也与基座 org 的做法一致：oekit 的 pkg / concurrency / core / logx / metrics /
kitx 六个基础仓一律是 `go 1.24.0` + **无 toolchain 行**。
## 改动
- `go.mod`：`go 1.26.0` → `go 1.24.0`；删 `toolchain go1.27.2`
- `.github/workflows/ci.yml`：`go-version: '1.26'` → `'1.24'`
CI 的 go-version 与 go.mod 的 go 指令**成对**——这是跨层通用纪律，不因本次
降级而放松。CI 用高版本构建、go.mod 承诺低版本时，CI 全绿但用户按最低版本
编译失败，门禁绿了而承诺没兑现。
## 未受影响
- `require` 段（依赖地板钉）**未动**，`go mod tidy -diff` 零差异、go.sum 未变
- 内部 require 版本不变 ⇒ 不影响任何下游的 MVS
- 对外产物（versions.json / llms.txt）不记录 go 版本，无需刷新
## 验证
- `GOTOOLCHAIN=go1.24.0 go build ./...` → exit 0
- `GOTOOLCHAIN=go1.24.0 go test ./...` → 全绿
- `go mod tidy -diff` → 零差异
门禁见 oagentkit/matrix-tools PR：`scripts/verify_go_floor.py`
（tier0 必须 1.24.x + CI go-version 与 go.mod 成对，判据独立不复用生成器）。

## [v0.7.0] — 2026-10-19

- 杂务(deps)：依赖地板对齐远端最新（concurrency v0.17.1）

matrix-tools 的 verify_dep_floors.py 首次全矩阵扫描查出：43 条第一方依赖里
28 条落后，横跨 11 个仓；本仓是其中之一。
所升版本与本仓原钉版本相比源码零改动，逐版实测确认：
- textx v0.3.0→v0.4.0 / obs v0.4.1→v0.5.0 / runtime v0.5.0→v0.6.0 /
  mcpkit v0.5.1→v0.6.0  全树逐字节相同（diff -rq 退出码 0）
- oekit/pkg v0.14.0→v0.15.1  仅新增 urlguard 包（纯标准库，pkg 的 go.mod
  连 require 块都没有，零新增三方依赖）
- oekit/concurrency v0.17.0→v0.17.1  仅三个文档文件
- oekit/logx v0.7.0→v0.7.1  仅 CI 配置与文档
无 API 变更、无兼容风险。
本地门禁：go mod tidy -diff 无输出 / go build / go vet / gofmt / go test -race 全通过。


## [v0.6.0] — 2026-10-19

- 杂务(deps)：oekit 依赖升级（concurrency v0.16.0→v0.17.0）

本 PR 含两项改动。
## 1. oekit 依赖地板
姊妹矩阵 oekit 的 `concurrency` 出了 v0.17.0，依赖地板一并对齐。
升级前已核对 `oekit/concurrency` v0.16.0...v0.17.0 的全部 2 个提交：
- `feat(ratelimit): KeyedLimiter.Create 护栏内原子登记 (Fixes #33)` — `ratelimit/keyed.go` +28 行
- `docs: 矩阵总览表 DESC 口径同步` — 仅 AGENTS.md / README.md / CHANGELOG.md
无 breaking / remove 标记，未触及任何已有导出签名，故为纯补丁式升级。
## 2. 迁出被 force 移动过的 tag
本仓原先依赖的姊妹仓 tag（发布后被强推到了重写后的 main）内容已与
sum.golang.org 记录不一致，于是任何走 `proxy.golang.org` 或 `GOPROXY=direct`
的构建都会 SECURITY ERROR。本地往往不报错——模块缓存预热 + goproxy.cn
还留着移动前的字节，把问题掩盖了。
已为 8 个模块重打不可变的**附注 tag**（内容与被移动 tag 指向同一 commit，
只换版本号，让校验和数据库重新记录一次正确哈希），并把依赖迁过去。
`go.sum` 的 h1 全部取自 sum.golang.org，仅就地替换目标模块的两行
（zip h1 + go.mod h1），其余行不动。


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

- 杂务(deps)：oekit 依赖升级

concurrency v0.13.1→v0.16.0
姊妹矩阵 oekit 的依赖地板此前被漏掉（上一轮只扫了 oagentkit 内部依赖），本次一并对齐到最新。
升级前已确认 oekit/concurrency v0.13.1..v0.16.0 的 8 个提交全是 feat/docs/chore，
无 breaking/remove 标记。其中 #28 订正了 `WithCooldown(0)` 的语义——
即 llms.txt 里警告过的那个陷阱，升上去正好拿到修正后的行为。
go.sum 的 h1 取自 sumdb，仅就地替换目标 mod 的两行、其余行不动。

- 杂务(deps)：oekit/concurrency v0.12.0 -> v0.13.1

本仓不使用上游 breaker（熔断只在 llm 仓），v0.13.0 的 #25 修复与
v0.13.1 的 #26（RetryableWithHeaders 头值 OWS 修剪）对本仓无直接影响，
本次为矩阵内版本对齐。
门禁：go mod tidy -diff / build / vet /
test -race / gofmt 全绿。

- 杂务(deps)：oekit/concurrency v0.6.0 -> v0.9.0

拿到上游显式三态状态机（#17）与 singleflight 可观测折叠（#18），并为本次
breaker 替换评估了语义差异。
用到的子包只有 async / singleflight / syncx —— 上游 breaker 包我们并未引用
（runtime/breaker 是本仓自己的三态实现），所以本次升级零 breaking。
关于 breaker 替换的现状：#17 已落地（closed/open/half-open + 代绑定 + 探测时限 +
开放期失败不延长冷却），但两侧的触发语义仍不对齐，已在上游补两个需求：
- #19 冷却期与探测失联时限共用 probeTimeout，无法独立配置
- #20 缺「连续失败 N 次即熔断」（trip）触发
因此本包暂不删除 —— llm.TestResilientBreakerTrips 钉的是 trip 语义，
上游 v0.9.0 只有速率型触发（dropRatio > 0），现在换会让该测试失败。


- CI：接入 org 共享门禁（规则本体在 oagentkit/.github）
