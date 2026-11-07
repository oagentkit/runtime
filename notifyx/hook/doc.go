// Package hook notifyx 的挂点侧 shim：把各 agent 平台 hook 挂点的
// shell 协议载荷（stdin JSON / argv JSON）归一化为 notifyx.Payload，
// 让平台 hooks 配置退化为一行调用，替代消费方在 bash 里二次实现的
// 映射表 / ack 轮询 / 节流（oagentkit/runtime#36）。
//
// # 形态
//
// 本包是库（可嵌入 Go 程序），命令行形态在子目录 cmd/notifyx-hook
// （go install github.com/oagentkit/runtime/notifyx/hook/cmd/notifyx-hook），
// 供 hooks 配置直呼：
//
//	~/.agents/hooks/notifyx-hook --agent zcode \
//	    --url https://notify.example/hook --header "Authorization: Bearer $T" \
//	    < payload.json
//
// # 归一化映射表（与主包文档同源同测）
//
// 平台原始事件名 → notifyx.Event 的映射是 notifyx 包文档（doc.go）那张表的
// **可执行单源**，hook_test.go 的用例逐条钉住两边：
//
//	task_complete      ← zcode Stop / cc Stop / kimi Stop / codex task_complete /
//	                     opencode·mimo session.idle
//	permission_request ← zcode PermissionRequest / cc PermissionRequest 类 /
//	                     codex PermissionRequest / kimi PermissionRequest /
//	                     opencode·mimo permission.updated
//	input_required     ← opencode·mimo question.asked / cc notification（提问形态）
//	error              ← kimi StopFailure / 各平台 failure 类事件
//	session_end        ← 平台显式会话结束信号（无挂点的平台不映射）
//
// 事件名匹配先查精确表（大小写/分隔符不敏感：`PermissionRequest`、
// `permission_request`、`session.idle` 视为同名），未命中再走兜底规则
// （对应表内「类 / failure 类」措辞：含 permission+request →
// permission_request；含 failure/error → error），仍命中不了按错误返回——
// 不猜测归一，宁可让调用方看见。
//
// # 节流
//
// Throttler 是跨进程的按键节流（文件时间戳实现，CLI 每次调用都是新进程，
// 内存态没有意义）。键维度为 agent+session+event：比实证脚本的
// agent+会话多一维——permission_request 紧跟 task_complete 的场景不该被
// 前者的节流窗口吃掉。窗口可配（缺省 5s），状态文件损坏/不可写一律
// fail-open（宁可重发通知，不可吞掉「等人工」事件）。
//
// # ack 轮询
//
// 本包不另做轮询实现：CLI 直接组合主包的 PollAck + HTTPAckEndpoint，
// 缺省参数与 PollAck 同款（间隔 2s、连续失败上限 5）。
package hook
