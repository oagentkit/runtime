// Package procx 子进程托管纪律单源：进程**树**执行（unix：Setpgid 建组 →
// 超时/取消 TERM 整组 → 宽限 SIGKILL；windows：Job Object 托管整树 →
// TerminateJobObject，语义差异见 group_windows.go 头注）、限容采集
// （stdout/单行双上限）、stdout 按行拆分、子进程环境白名单（绝不继承宿主全量环境）。
// acpx 各 CLI agent 适配器与 mcp/workcopy 等自行 spawn 外部命令的包共用本包，
// 纪律只有一份。
package procx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/oekit/concurrency/async"
	"io"
	"os/exec"
	"strings"
	"time"
)

// maxChildStdout 子进程 stdout 采集上限（异常命令刷屏防内存放大）。
const maxChildStdout = 8 << 20 // 8MB（stream-json 事件量大，比 Argus 的 1MB 放宽）

// killGrace 超时/取消后 TERM 整组到 SIGKILL 的宽限期。
const killGrace = 3 * time.Second

// sentinel 错误：调用方可 errors.Is 分类（超时 / 被取消）。
var (
	// ErrTimeout 运行超过 Timeout 被终止。
	ErrTimeout = errors.New("执行超时")
	// ErrCanceled 运行被调用方 context 取消。
	ErrCanceled = errors.New("执行被取消")
)

// cappedBuffer 限容写入器：超限后丢弃后续写入并标记截断。
type cappedBuffer struct {
	limit     int
	buf       bytes.Buffer
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	if limit <= 0 {
		limit = maxChildStdout
	}
	return &cappedBuffer{limit: limit}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		room := c.limit - c.buf.Len()
		if room > 0 {
			c.buf.Write(p[:room])
		}
		c.truncated = true
		return len(p), nil // 吸收写入让子进程继续跑（管道不阻塞）
	}
	c.buf.Write(p)
	return len(p), nil
}

// execCLI 进程组执行外部命令（照抄 Argus adapter_cli 生产范式）：
// Setpgid 建组 → ctx 取消/超时 TERM 整组（cmd.Cancel）→ killGrace 后框架 SIGKILL
// leader（cmd.WaitDelay）→ Wait 返回后兜底 KILL 整组清残留孙进程。
// maxStdout stdout 采集上限（0 = maxChildStdout 缺省）。
// 分工：newGroupCmd 建组与杀组纪律 / startAndWait 启动写 stdin 并收割 /
// classifyWait 错误语义 / noteTruncated 截断留痕。
func execCLI(ctx context.Context, dir string, argv []string, env []string,
	timeout time.Duration, onLine func(string), maxStdout int, stdin []byte) (stdout, stderr string, exitCode int, err error) {

	if len(argv) == 0 {
		return "", "", -1, fmt.Errorf("procx: 命令为空")
	}
	// argv[0] 只允许是可执行名：以 "-" 开头会被 exec 层之下的参数解析当选项
	// （argument injection）；来自装配配置的越界即配置错误，fail-fast
	if strings.HasPrefix(argv[0], "-") {
		return "", "", -1, fmt.Errorf("procx: 非法可执行名 %q", argv[0])
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd, group := newGroupCmd(cctx, dir, argv, env)

	outBuf, errBuf := newCappedBuffer(maxStdout), newCappedBuffer(0)
	var lineW *lineWriter
	var lineCb func(string)
	if onLine == nil {
		cmd.Stdout = outBuf
	} else {
		lineCb = guardOnLine(onLine) // 回调 panic 库层收编（runtime#70）
		lineW = &lineWriter{buf: outBuf, onLine: lineCb}
		cmd.Stdout = lineW
	}
	cmd.Stderr = errBuf

	waitErr, err := startAndWait(cmd, stdin, group, argv[0])
	if err != nil {
		return "", "", -1, err
	}
	// 行缓冲收尾（放在错误判断之前）：失败路径的流尾事件同样要 flush——
	// mimo 的 error JSON / kimi 的最后一行可能无尾换行，claude 的 result
	// 事件到达后进程仍可能非零退出，"result 优先"策略依赖这里救回结果。
	// flush 也走同一个 panic 守卫（runtime#70）——它在调用方 goroutine 上，
	// 但纪律只有一份：外部回调缺陷一律收编，不靠调用方自觉 recover。
	if lineCb != nil && len(lineW.partial) > 0 && !lineW.overflow {
		lineCb(string(lineW.partial))
		lineW.partial = nil
	}

	// 截断留痕在**所有**返回路径之前统一盖上（runtime#38）：此前只有成功路径
	// 过 noteTruncated，非零退出/超时/取消路径裸返 out —— 失败现场被裁剪到
	// maxStdout 却没有任何标记，排障时把「截断导致看不到结论」误读成
	// 「命令没输出结论」。放在这里而不是逐个 return 补，是为了让「任何路径
	// 都带标记」成为结构性事实，而不是三处各写一遍的自觉。
	//
	// stderr 同病同治：它的上限是 maxChildStdout（8MB），此前两条路径都不标记。
	// 与 stdout 的唯一区别是它的缺省上限取自函数内的缺省值（传 0）。
	out, errStr := noteTruncated(outBuf.buf.String(), outBuf, maxStdout),
		noteTruncated(errBuf.buf.String(), errBuf, 0)
	if waitErr != nil {
		// ErrWaitDelay：leader 已成功退出，但遗留的后台孙进程仍握 stdout 写端，
		// WaitDelay 到点强关管道所致（os/exec 语义：Cancel 未被调用 + 退出成功时
		// 才返回）。组已被 startAndWait 兜底 SIGKILL 清理——这是一次成功执行，
		// 不是失败（npm 类包装器守护化后台进程曾把成功跑整体误判为失败；
		// 第六轮审计）。成功特判的**完整条件**见 waitDelayIsBenign。
		if waitDelayIsBenign(ctx, cctx, waitErr) {
			return out, errStr, 0, nil
		}
		return out, errStr, exitStatus(waitErr),
			classifyWait(ctx, cctx, argv[0], waitErr, errBuf, timeout)
	}
	return out, errStr, 0, nil
}

// waitDelayIsBenign 报告 ErrWaitDelay 是否属于「成功执行 + 孙进程握管道」的
// 良性形态（runtime#35）。
//
// 三个条件缺一不可：
//   - waitErr 是 exec.ErrWaitDelay（否则本次失败与 WaitDelay 无关）；
//   - ctx.Err() == nil：调用方没取消（自己取消的 deadline 也归 ErrCanceled，
//     那是一次被中止的执行，不是成功）；
//   - cctx.Err() == nil：**本地 Timeout 没到**。这一条此前缺失，而它恰是
//     ErrWaitDelay 最可能出现的时刻——leader 恰在本地超时点以 0 退出、孙进程
//     仍握管道时，`ctx`（调用方 ctx）确实是干净的，只有 `cctx`（WithTimeout
//     派生）已 DeadlineExceeded。只看 ctx ⇒ 被超时终止的执行被上报为
//     「exit 0 成功」，ErrTimeout 语义整条丢失，调用方以为命令跑完了。
//
// 为什么单独成函数：这个判据的正确性取决于「两个 ctx 分别代表谁」，而
// 端到端的竞态窗口（超时点与 leader 退出点重合）无法用真实进程稳定复现——
// 把判据抽出来才能用构造的 ctx 逐条钉死，e2e 用例只管良性路径仍绿。
func waitDelayIsBenign(ctx, cctx context.Context, waitErr error) bool {
	return errors.Is(waitErr, exec.ErrWaitDelay) && ctx.Err() == nil && cctx.Err() == nil
}

// startAndWait 启动 + 托管进程树 + 异步喂 stdin + 收割（含失败兜底清树）。
// 返回的 err 仅启动/托管期失败（未跑到 Wait）；waitErr 为 Wait 结果。
func startAndWait(cmd *exec.Cmd, stdin []byte, group processGroup, name string) (waitErr error, err error) {
	// StdinPipe 必须在 Start 之前建（os/exec 守卫：进程启动后 StdinPipe 报错，
	// 后建等于把 payload 丢进 /dev/null——子进程实际读到空 stdin）
	var stdinPipe io.WriteCloser
	if stdin != nil {
		pipe, perr := cmd.StdinPipe()
		if perr != nil {
			return nil, fmt.Errorf("procx: stdin 管道建立失败: %w", perr)
		}
		stdinPipe = pipe
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("procx: 启动 %s 失败: %w", name, err)
	}
	// 托管紧跟 Start：windows 要拿到 pid 才能把进程放进 Job（早于 Start 没有
	// pid），而 unix 的 Setpgid 已在 Start 时生效，故这里对 unix 是空操作。
	// 托管失败一律致命——不托管就兑现不了「整树收场」，故先杀再 Wait 回收，
	// 不让一个不受管的进程树跑在后台。
	if aerr := group.attach(); aerr != nil {
		_ = group.kill(killForceful)
		_ = cmd.Wait()
		return nil, aerr
	}
	// 先于 Wait 注册：无论 Wait 成功还是失败，平台资源（windows 的 Job 句柄）
	// 与可能残留的孙进程都必须收口。
	defer group.close()
	if stdinPipe != nil {
		// 异步写：子进程不读 stdin 且长寿时，同步 Write 会满管道阻塞到超时才进
		// Wait，错误分类被拖死。进程退出/被杀后 Write 得 EPIPE 自然收口。
		async.GoSafe(func() {
			_, _ = stdinPipe.Write(stdin)
			_ = stdinPipe.Close()
		})
	}
	waitErr = cmd.Wait()
	if waitErr != nil {
		// 兜底清树：leader 已死，这里清的是 TERM 宽限后仍未退出的孙进程
		_ = group.kill(killForceful)
	}
	return waitErr, nil
}

// classifyWait 把 Wait 错误翻译成调用方可 errors.Is 的语义（取消/超时/失败+stderr 尾巴）。
func classifyWait(ctx, cctx context.Context, name string, waitErr error, errBuf *cappedBuffer, timeout time.Duration) error {
	tail := errBuf.buf.String()
	if len(tail) > 400 {
		tail = tail[len(tail)-400:]
	}
	switch {
	// 本地 Timeout 先判（runtime#63）：调用方恰在本地超时落定之后才 cancel
	// 时，ctx.Err() 与本地 deadline 同时非 nil，真实超时必须报 ErrTimeout——
	// 旧判据 ctx.Err() 优先把这条边界整条吞成 ErrCanceled，调用方按
	// ErrTimeout 重试 / 按 ErrCanceled 放弃的分派走错分支。
	// 两个 ctx 同为 DeadlineExceeded 时谁是肇事发判据见 localDeadlineFired。
	case errors.Is(cctx.Err(), context.DeadlineExceeded) && localDeadlineFired(ctx, cctx):
		return fmt.Errorf("procx: %s %w（%s）", name, ErrTimeout, timeout)
	case ctx.Err() != nil: // 调用方主动取消（含调用方自己的 deadline）
		return fmt.Errorf("procx: %s %w: %w", name, ErrCanceled, ctx.Err())
	default:
		return fmt.Errorf("procx: %s 执行失败: %w: %s", name, waitErr, strings.TrimSpace(tail))
	}
}

// localDeadlineFired 报告 cctx 的 DeadlineExceeded 是否出自**本地 Timeout**
// （本地预算耗尽）而非调用方自己的 deadline 经 parent 传播（runtime#63）。
//
// 两者在 cctx.Err() 上同形（都是 DeadlineExceeded），但语义相反：调用方的
// deadline 更早时，WithDeadline 退化为对 parent 的 cancel 拷贝（cctx.Deadline()
// 与 parent 相等），那是一次被调用方中止的执行，归 ErrCanceled（沿用既有
// 语义）；本地 deadline 是两者中较早、真正到点的那一个时才是 ErrTimeout。
// 判据：parent 无 deadline，或 parent deadline 晚于 cctx deadline（本地的
// 先到点）。两者相等（调用方刻意传入同一时刻）时取保守侧——按调用方自己的
// deadline 计，归 ErrCanceled。
func localDeadlineFired(ctx, cctx context.Context) bool {
	parentDeadline, ok := ctx.Deadline()
	cctxDeadline, _ := cctx.Deadline()
	return !ok || parentDeadline.After(cctxDeadline)
}

// noteTruncated 超限截断时在 stdout 尾部留痕（调用方可见「有内容被丢」）。
func noteTruncated(out string, outBuf *cappedBuffer, maxStdout int) string {
	if !outBuf.truncated {
		return out
	}
	limit := maxStdout
	if limit <= 0 {
		limit = maxChildStdout
	}
	return out + fmt.Sprintf("\n…（stdout 超过 %d 字节上限，已截断）", limit)
}

// RunRequest 独立进程执行请求：只要进程组托管纪律的调用方使用
// （acpx 各 CLI agent 适配器经 acpx.runCLI 复用同一纪律）。
type RunRequest struct {
	// Argv 可执行 + 参数（argv 直传，无 shell、无注入面）。
	Argv []string
	// Dir 工作目录（空 = 继承当前进程）。
	Dir string
	// Env 子进程环境白名单（按名从当前进程透传；含 "=" 按 KEY=VALUE 字面），
	// 叠加在 baseEnvAllow 基础集之上——绝不全量继承。
	Env []string
	// Timeout 单次执行超时（0 = 10 分钟缺省）。
	Timeout time.Duration
	// OnLine 可选 stdout 按行回调（nil = 只缓冲；回调在读取 goroutine 中执行，
	// 不得阻塞；单行超 maxLineLen 会被丢弃）。回调 panic 由库层收编（runtime#70）：
	// 本次执行后续行不再回调、stdout 全文仍采集兜底，宿主进程不受影响——
	// 「不得 panic」不再是宿主进程的生死线，但仍是纪律（panic 后行回调丢失）。
	OnLine func(string)
	// MaxStdout stdout 采集上限（0 = 8MB 缺省）。
	MaxStdout int
	// Stdin 可选：启动后写入子进程 stdin 并关闭（hookx 等交互式协议用）。
	// 子进程不读 stdin 提前退出时写入失败静默容忍（printf 类命令无害）。
	Stdin []byte
}

// defaultTimeout 单次执行缺省超时。
const defaultTimeout = 10 * time.Minute

// Run 以进程组纪律执行外部命令：Setpgid 建组 → 超时/取消 TERM 整组 →
// killGrace 宽限后 SIGKILL。超时/取消可经 errors.Is(err, ErrTimeout/ErrCanceled)
// 程序化区分；非零退出返回 error（含 stderr 尾巴），exitCode 一并返回。
// 环境走白名单——待执行目录里的命令可执行任意代码，全量环境等于泄漏宿主凭证。
func Run(ctx context.Context, req RunRequest) (stdout, stderr string, exitCode int, err error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return execCLI(ctx, req.Dir, req.Argv, ChildEnv(req.Env), timeout, req.OnLine, req.MaxStdout, req.Stdin)
}

// exitStatus 从 Wait 错误提取退出码。
func exitStatus(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}
