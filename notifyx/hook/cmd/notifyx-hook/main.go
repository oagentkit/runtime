// Command notifyx-hook 平台挂点 → notifyx 归一化通知的官方 shim：
// stdin/argv 收平台 payload，查 notifyx/hook 映射表归一化，经 notifyx
// 可靠外发（重试由 Notifier 驱动），可选 ack 轮询与跨进程节流。
// hooks 配置从此退化为一行（oagentkit/runtime#36）：
//
//	~/.agents/hooks/notifyx-hook --agent zcode \
//	    --url https://notify.example/hook --header "Authorization: Bearer $T" \
//	    < payload.json
//
// 退出码（bash 侧分派用）：
//
//	0 送达（或节流窗口内跳过；ack=approve/done）
//	2 输入错误（参数/payload/事件不在映射表）
//	3 外发失败（重试耗尽）
//	4 ack 轮询失败/超时（未取到回执）
//	5 ack 决策为 deny
//
// ack 轮询参数缺省与 notifyx.PollAck 同款（间隔 2s、连续失败上限 5）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/oagentkit/runtime/notifyx"
	"github.com/oagentkit/runtime/notifyx/hook"
)

// 退出码契约（doc 注释里的表）。
const (
	exitOK      = 0
	exitInput   = 2
	exitSend    = 3
	exitAckPoll = 4
	exitAckDeny = 5
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// headerFlags 可重复的 --header k=v（凭证注入点，语义同 HTTPSink.Headers）。
type headerFlags map[string]string

func (h headerFlags) String() string {
	pairs := make([]string, 0, len(h))
	for k, v := range h {
		pairs = append(pairs, k+"="+v)
	}
	return strings.Join(pairs, ",")
}

func (h headerFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("header 需要 k=v 形态，得到 %q", v)
	}
	h[k] = val
	return nil
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("notifyx-hook", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agent := fs.String("agent", "", "平台标识：zcode/cc/codex/kimi/opencode/mimo")
	url := fs.String("url", "", "webhook 端点（notifyx.HTTPSink）")
	var headers headerFlags = make(map[string]string)
	fs.Var(&headers, "header", "附加请求头 k=v（可重复，凭证注入）")
	argPayload := fs.Bool("arg", false, "payload 取自首个位置参数（codex argv 形态）而非 stdin")
	sessionID := fs.String("session-id", "", "覆盖 payload 里的 session_id")
	project := fs.String("project", "", "项目标识（载荷补齐）")
	branch := fs.String("branch", "", "分支（载荷补齐）")
	priority := fs.String("priority", "", "优先级 low/normal/high（空 = normal）")
	throttleWindow := fs.Duration("throttle-window", hook.DefaultThrottleWindow, "同键（agent+session+event）节流窗口，0 = 关闭")
	throttleDir := fs.String("throttle-dir", "", "节流状态目录（缺省用户缓存目录下 notifyx-hook）")
	ackURL := fs.String("ack-url", "", "ack 轮询端点（空 = 不轮询）")
	ackInterval := fs.Duration("ack-interval", 2*time.Second, "ack 轮询间隔（PollAck 同款缺省）")
	ackMaxFails := fs.Int("ack-max-fails", 5, "ack 连续失败上限（PollAck 同款缺省）")
	ackTimeout := fs.Duration("ack-timeout", 10*time.Minute, "ack 轮询总时限，<=0 = 无限等")
	if err := fs.Parse(args); err != nil {
		return exitInput
	}
	if *agent == "" || *url == "" {
		fmt.Fprintln(stderr, "notifyx-hook: --agent 与 --url 必填")
		return exitInput
	}

	data, err := readPayload(fs, *argPayload, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "notifyx-hook:", err)
		return exitInput
	}
	p, err := hook.Normalize(hook.Agent(*agent), data)
	if err != nil {
		fmt.Fprintln(stderr, "notifyx-hook:", err)
		return exitInput
	}
	// CLI 侧覆盖优先于 payload 提取值：会话/仓库维度 hooks 配置比事件载荷可靠。
	if *sessionID != "" {
		p.SessionID = *sessionID
	}
	p.Project, p.Branch, p.Priority = *project, *branch, *priority

	// 节流：窗口内重复触发按正常退出跳过（重复不是错误）；状态文件故障
	// fail-open 放行，错误只落 stderr 供观测。
	th := hook.NewThrottler(
		hook.WithThrottleWindow(*throttleWindow),
		hook.WithThrottleDir(*throttleDir),
	)
	allowed, err := th.Allow(*agent, p.SessionID, string(p.Event))
	if err != nil {
		fmt.Fprintln(stderr, "notifyx-hook:", err)
	}
	if !allowed {
		fmt.Fprintf(stderr, "notifyx-hook: 节流窗口 %s 内重复的 %s/%s，跳过外发\n", *throttleWindow, *agent, p.Event)
		return exitOK
	}

	sink := notifyx.NewHTTPSink(*url)
	for k, v := range headers {
		sink.WithHeader(k, v)
	}
	// 重试耗尽不报在 Close 返回值上（丢弃是 Notifier 内部计量），用 OnDrop
	// 捕获最后一次错误、以 Delivered 判定唯一一件是否真的送达。
	var dropErr error
	n := notifyx.NewNotifier(sink, notifyx.WithOnDrop(func(_ notifyx.Payload, err error) { dropErr = err }))
	// 「等人工」类必须送达：用 EmitWait 背压形态而不是 Emit 的尽力丢弃。
	if err := n.EmitWait(context.Background(), p); err != nil {
		fmt.Fprintln(stderr, "notifyx-hook: 外发失败:", err)
		return exitSend
	}
	if err := n.Close(context.Background()); err != nil {
		fmt.Fprintln(stderr, "notifyx-hook: 排空失败:", err)
		return exitSend
	}
	if n.Delivered() != 1 {
		fmt.Fprintln(stderr, "notifyx-hook: 外发失败（重试耗尽）:", dropErr)
		return exitSend
	}
	if *ackURL == "" {
		return exitOK
	}
	if p.SessionID == "" {
		fmt.Fprintln(stderr, "notifyx-hook: ack 轮询需要 session_id（payload 或 --session-id）")
		return exitInput
	}

	ctx := context.Background()
	if *ackTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *ackTimeout)
		defer cancel()
	}
	ack, err := notifyx.PollAck(ctx, notifyx.HTTPAckEndpoint(*ackURL), p.SessionID,
		notifyx.WithPollInterval(*ackInterval),
		notifyx.WithPollMaxConsecutiveFails(*ackMaxFails),
	)
	if err != nil {
		fmt.Fprintln(stderr, "notifyx-hook: ack 轮询失败:", err)
		return exitAckPoll
	}
	out, err := json.Marshal(ack)
	if err != nil {
		fmt.Fprintln(stderr, "notifyx-hook: ack 序列化失败:", err)
		return exitAckPoll
	}
	fmt.Fprintln(stdout, string(out))
	if ack.Decision == "deny" {
		return exitAckDeny
	}
	return exitOK
}

// readPayload 取平台 payload：--arg 时读首个位置参数（codex notify 把 JSON
// 追加在命令尾部），否则读 stdin（zcode/cc/kimi 等 hooks 形态）。
func readPayload(fs *flag.FlagSet, fromArg bool, stdin io.Reader) ([]byte, error) {
	if fromArg {
		rest := fs.Args()
		if len(rest) == 0 {
			return nil, errors.New("--arg 需要一个位置参数携带 payload")
		}
		return []byte(rest[0]), nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("读 stdin 失败: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("stdin 为空（payload 缺失）")
	}
	return data, nil
}
