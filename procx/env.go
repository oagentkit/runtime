// env.go —— 环境纪律委托壳：单源在 oekit/pkg/procx（跨 org procx 收敛，
// runtime#26 系列判例的最终形态）。
//
// 本包自 2026-11-23 起是**兼容壳**：全部实现已下沉 oekit/pkg/procx（机制件
// 归位，pkg#284 决策闭环——下游 pigeonbox/core 曾为 ChildEnv 直依赖本仓而
// 新增跨 org 依赖边）。本文件保留旧入口签名，行为对齐下沉前形态：
//
//   - [ChildEnv] 在 pkg 基础白名单之上**显式补回 agent 域配置目录键**
//     （compatAgentEnvKeys）——旧 runtime 白名单里有、pkg 纪律里属「工具
//     特定配置走 extra」的四个键。存量外部消费方（goagent-kernel ptc 等）
//     依赖它们透传登录态，壳层补齐保证行为不因升级漂移；新代码请直接
//     import github.com/oekit/pkg/procx 并按需声明 extra。
//   - [InheritAllExcept] 逐字委托 pkg 同名函数（含 windows 大小写归一）。
//
// 弃用时间表：与 agentkit 门面同款——compatShells 登记，待外部消费方迁移
// 列车完成后删除本包。
package procx

import (
	pkgprocx "github.com/oekit/pkg/procx"
)

// compatAgentEnvKeys 壳层为行为兼容补回的 agent 域配置目录键：CLI agent
// （claude/codex/opencode 系）的登录态与 XDG 约定。pkg 纪律里这些是「工具
// 特定配置」，不经基础白名单——壳层按名透传以保持 runtime 历史行为。
var compatAgentEnvKeys = []string{
	"CLAUDE_CONFIG_DIR", "OPENCODE_CONFIG",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME",
}

// ChildEnv 构造子进程最小环境（弃用：改用 github.com/oekit/pkg/procx.ChildEnv）。
// 语义 = pkg 基础白名单（路径/账户/临时目录/语言时区/终端/代理/CA/windows
// 必需组，尾部 GIT_TERMINAL_PROMPT=0 与 CI=1）∪ compatAgentEnvKeys ∪ extra。
// extra 两种形态（纯名透传 / KEY=VALUE 字面注入）与默认拒绝语义见 pkg 单源。
func ChildEnv(extra []string) []string {
	return pkgprocx.ChildEnv(append(compatAgentEnvKeys, extra...))
}

// InheritAllExcept 返回「继承当前进程环境、剔除 except 名单」的子进程环境
// （弃用：改用 github.com/oekit/pkg/procx.InheritAllExcept）。逐字委托 pkg
// 单源；except 口径（变量名 / 含 = 取名剔除）与失败方向声明见 pkg 文档。
func InheritAllExcept(except []string) []string {
	return pkgprocx.InheritAllExcept(except)
}
