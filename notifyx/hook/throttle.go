package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
//
// 状态文件的生命周期（runtime#126）：键含 sessionID ⇒ 每会话一文件，长跑
// 宿主会持续累积。Allow 在成功写入当前键后做一次惰性清扫：删掉 mtime 早于
// 窗口的 .stamp——窗口外的键本就不再判中，留着只会无界增长。清扫失败不影响
// 本次放行（已在放行路径上），错误随返回值带回观测。
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
// 回系统临时目录）。dir 为空串时**保持缺省目录不变**（runtime#84）：空串
// 不是有效目录，套上去只会让 MkdirAll 必败、Allow 每次走 fail-open 放行，
// 节流整体静默失效。守卫放在库层而非调用点——CLI 的 flag 缺省值是空串、
// 装配时无条件套本选项，库层守卫让「没配 = 用缺省」成为唯一语义，下一个
// 调用点想再犯也犯不了。
func WithThrottleDir(dir string) ThrottlerOption {
	return func(t *Throttler) {
		if dir != "" {
			t.dir = dir
		}
	}
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
	// 当前键已落盘且放行；顺手清扫过期状态（runtime#126）。清扫失败仍放行，
	// 错误带回给调用方——清扫是资源卫生，不是判定的一部分。
	if err := t.sweepStale(now); err != nil {
		return true, fmt.Errorf("notifyx/hook: 节流状态清扫失败: %w", err)
	}
	return true, nil
}

// sweepStale 惰性清扫窗口外的状态文件（runtime#126）：只处理本目录顶层的
// .stamp 文件，mtime 早于窗口（含未来时间戳的时钟回拨场景一律保留）的被删除。
// 删除按 mtime 判定而不是读内容：窗口外的键本就不再判中，读内容只是多一次
// 解析；并发进程同时清扫也安全（Remove 竞态按 not-exist 忽略）。
func (t *Throttler) sweepStale(now time.Time) error {
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".stamp") {
			continue
		}
		info, ierr := entry.Info()
		if ierr != nil {
			errs = append(errs, ierr)
			continue
		}
		if now.Sub(info.ModTime()) <= t.window {
			continue // 窗口内（或 mtime 在未来）：仍可能判中，保留
		}
		if rerr := os.Remove(filepath.Join(t.dir, entry.Name())); rerr != nil && !os.IsNotExist(rerr) {
			errs = append(errs, rerr)
		}
	}
	return errors.Join(errs...)
}

// throttleKey 键哈希成文件名：维度直接拼原文可能带路径分隔符/超长，
// sha256 前 16 字节十六进制既文件名安全又防碰撞足够。
func throttleKey(agent, sessionID, event string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + sessionID + "\x00" + event))
	return hex.EncodeToString(sum[:16])
}
