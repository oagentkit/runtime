// process.go —— 执行族委托壳：单源在 oekit/pkg/procx（跨 org procx 收敛）。
//
// 执行纪律（进程组托管 / lineWriter / classifyWait / 截断事实 / 行为测试）
// 已整族下沉 pkg/procx；本文件保留旧入口签名做逐字委托：
//
//	procx.Run(ctx, req)            → pkgprocx.RunDetailed(ctx, req) 的解构薄壳
//	procx.RunDetailed(ctx, req)    → pkgprocx.RunDetailed
//	procx.ErrTimeout / ErrCanceled → pkg 同名 sentinel
//
// 行为对齐说明：旧 RunDetailed 的环境是「runtime 白名单（含 agent 配置
// 目录键）∪ req.Env」；壳层经 compatAgentEnvKeys（见 env.go）补齐该差异，
// 下沉前后对同一请求产出同一环境。行为测试已随实现迁入 pkg（行为逐条
// 钉死）；本包仅保留壳自身的委托一致性测试（shell_test.go）。
//
// 弃用时间表与 env.go 相同：新代码 import github.com/oekit/pkg/procx。
package procx

import (
	"context"

	pkgprocx "github.com/oekit/pkg/procx"
)

// RunRequest 独立进程执行请求（弃用别名：type alias 同型透传 pkg 单源，
// 字段与语义文档见 github.com/oekit/pkg/procx.RunRequest）。
type RunRequest = pkgprocx.RunRequest

// RunResult 结构化执行结果（弃用别名：见 github.com/oekit/pkg/procx.RunResult）。
type RunResult = pkgprocx.RunResult

// ErrTimeout 运行超过 RunRequest.Timeout 被终止（弃用：pkg 同名 sentinel）。
var ErrTimeout = pkgprocx.ErrTimeout

// ErrCanceled 运行被调用方 context 取消（弃用：pkg 同名 sentinel）。
var ErrCanceled = pkgprocx.ErrCanceled

// RunDetailed 以进程组纪律执行外部命令（弃用：改用
// github.com/oekit/pkg/procx.RunDetailed）。壳层先为请求补上 compatAgentEnvKeys
// 再委托——环境形态与下沉前 runtime 实现逐字对齐。
func RunDetailed(ctx context.Context, req RunRequest) (RunResult, error) {
	req.Env = append(compatAgentEnvKeys, req.Env...)
	return pkgprocx.RunDetailed(ctx, req)
}

// Run 以进程组纪律执行外部命令（弃用：改用
// github.com/oekit/pkg/procx.RunDetailed + 解构 RunResult）。历史 4 值签名的
// 解构薄壳：语义与 RunDetailed 完全同源（同一实现），错误语义一致——超时/
// 取消可经 errors.Is(err, ErrTimeout/ErrCanceled) 程序化区分；非零退出返回
// error（含 stderr 尾巴），exitCode 一并返回。
func Run(ctx context.Context, req RunRequest) (stdout, stderr string, exitCode int, err error) {
	res, err := RunDetailed(ctx, req)
	return res.Stdout, res.Stderr, res.ExitCode, err
}
