package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultThrottleWindow 缺省节流窗口：只拦亚秒级到数秒内的重复触发
// （挂点双发/重试风暴），不吞人话节奏的正常重复通知。
const DefaultThrottleWindow = 5 * time.Second

// Throttler 跨进程按键节流器：状态是文件里的时间戳（CLI 每次调用都是
// 新进程，内存态没有意义），键维度 agent+session+event。
//
// 语义是**尽力而为**的抗重复，不是精确计量：同键并发调用在窗口边界可能
// 双双放行（读-判-写非原子），这对通知场景可接受——fail-open 是本类型的
// 总纪律：状态文件读不了/写不进一律放行（宁可重发通知，不可吞掉
// 「等人工」事件），错误带回给调用方观测。
type Throttler struct {
	window time.Duration
	dir    string
}

// ThrottlerOption 节流器可选项。
type ThrottlerOption func(*Throttler)

// WithThrottleWindow 节流窗口（<=0 关闭节流：一律放行，不落状态文件）。
func WithThrottleWindow(d time.Duration) ThrottlerOption {
	return func(t *Throttler) { t.window = d }
}

// WithThrottleDir 状态目录（缺省用户缓存目录下 notifyx-hook，取不到时退
// 回系统临时目录）。
func WithThrottleDir(dir string) ThrottlerOption {
	return func(t *Throttler) { t.dir = dir }
}

// NewThrottler 构造节流器（缺省窗口见 DefaultThrottleWindow）。
func NewThrottler(opts ...ThrottlerOption) *Throttler {
	t := &Throttler{
		window: DefaultThrottleWindow,
		dir:    defaultThrottleDir(),
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// defaultThrottleDir 缺省状态目录：优先用户缓存目录（按用户隔离），
// 取不到（无 HOME 等极端环境）退回系统临时目录。
func defaultThrottleDir() string {
	if d, err := os.UserCacheDir(); err == nil && d != "" {
		return filepath.Join(d, "notifyx-hook")
	}
	return filepath.Join(os.TempDir(), "notifyx-hook")
}

// Allow 该键（agent+session+event）是否放行。窗口内第二次及以后的调用
// 返回 false；窗口过期或首调返回 true 并刷新时间戳。
//
// 返回 (true, err) = 放行但状态文件操作失败（fail-open，调用方可观测）；
// (false, nil) = 节流命中。
func (t *Throttler) Allow(agent, sessionID, event string) (bool, error) {
	if t.window <= 0 {
		return true, nil
	}
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return true, fmt.Errorf("notifyx/hook: 节流状态目录不可用: %w", err)
	}
	path := filepath.Join(t.dir, throttleKey(agent, sessionID, event)+".stamp")
	now := time.Now()
	if content, err := os.ReadFile(path); err == nil {
		if last, perr := strconv.ParseInt(strings.TrimSpace(string(content)), 10, 64); perr == nil {
			if now.Sub(time.Unix(0, last)) < t.window {
				return false, nil
			}
		}
		// 解析失败（损坏/旧格式）按过期处理：fail-open 并顺手重写。
	}
	if err := os.WriteFile(path, []byte(strconv.FormatInt(now.UnixNano(), 10)), 0o600); err != nil {
		return true, fmt.Errorf("notifyx/hook: 节流状态写入失败: %w", err)
	}
	return true, nil
}

// throttleKey 键哈希成文件名：维度直接拼原文可能带路径分隔符/超长，
// sha256 前 16 字节十六进制既文件名安全又防碰撞足够。
func throttleKey(agent, sessionID, event string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + sessionID + "\x00" + event))
	return hex.EncodeToString(sum[:16])
}
