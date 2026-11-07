// Package notifyx 生命周期事件的通知出口（notify sink）：把 agent 生命周期
// 事件以归一化契约可靠外发（webhook/HTTP），供通知平台消费。
//
// # 与 hookx 的分工
//
// hookx 是**控制入向**（外部拦截 → 子进程，stdin 喂 JSON、stdout 收决策）；
// 本包是对称的**通知出向**（agent → 外部，生命周期事件外发观测流）。通知
// 不参与控制流——Emit 永不阻塞主流程，外发失败不炸业务（丢弃计数 +
// OnDrop 可观测）；需要强一致的通知（如 permission_request 必须送达）用
// EmitWait 背压形态或自持 Sink 直调。
//
// # 归一化事件契约
//
// 各 agent 平台的 hooks/notify/plugin 挂点异构，消费方此前要为每个平台
// 维护一份事件翻译胶水（zcode hooks / cc notification_type / codex notify
// argv / kimi TOML hooks / opencode·mimo JS 插件 / minimax 无 hooks 机制）。
// 本契约是单源，平台挂点 → Event 的映射：
//
//	task_complete      ← zcode Stop / cc Stop / kimi Stop / opencode session.idle
//	permission_request ← zcode PermissionRequest / cc PermissionRequest 类 /
//	                     kimi PermissionRequest / opencode permission.updated
//	input_required     ← opencode question.asked / cc notification（提问形态）
//	error              ← kimi StopFailure / 各平台 failure 类事件
//	session_end        ← 平台显式会话结束信号（无挂点的平台不映射）
//
// 消费方只配 URL + 凭证，不再写事件翻译（oagentkit/runtime#18）。
// 挂点侧（stdin/argv 的 shell 协议 → 本契约）的官方 shim 在 notifyx/hook
// 子包（含 notifyx-hook 命令行），映射表与本文档同源同测。
//
// # 可靠外发
//
// NewNotifier 的出站队列 + 指数退避重试 + 单次尝试超时：通知失败重试、
// 网络挂了不丢确认请求、队列满按序丢弃（计数可见）。「等人工」事件的确认
// 回执走 AckEndpoint + PollAck（octopus 侧轮询端点契约同源：ack != null
// 取回结果 / ack = null 继续等）。
package notifyx
