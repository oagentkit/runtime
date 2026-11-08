package hook

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// 首调放行、窗口内第二次拦截。
func TestThrottlerSuppressesWithinWindow(t *testing.T) {
	th := NewThrottler(WithThrottleDir(t.TempDir()), WithThrottleWindow(30*time.Second))
	for i, want := range []bool{true, false, false} {
		allowed, err := th.Allow("zcode", "s-1", "task_complete")
		if err != nil {
			t.Fatalf("第 %d 次: %v", i+1, err)
		}
		if allowed != want {
			t.Fatalf("第 %d 次 allowed=%v, want %v", i+1, allowed, want)
		}
	}
}

// 键维度是 agent+session+event：任一维度不同都各自独立放行——
// permission_request 不该被同会话 task_complete 的窗口吃掉（官方 shim
// 对实证脚本「agent+会话」键位收紧一维的契约点）。
func TestThrottlerKeyDimensions(t *testing.T) {
	th := NewThrottler(WithThrottleDir(t.TempDir()), WithThrottleWindow(time.Minute))
	if _, err := th.Allow("zcode", "s-1", "task_complete"); err != nil {
		t.Fatal(err)
	}
	for _, key := range [][3]string{
		{"cc", "s-1", "task_complete"},         // agent 不同
		{"zcode", "s-2", "task_complete"},      // session 不同
		{"zcode", "s-1", "permission_request"}, // event 不同
	} {
		allowed, err := th.Allow(key[0], key[1], key[2])
		if err != nil {
			t.Fatal(err)
		}
		if !allowed {
			t.Errorf("键 %v 不应被节流", key)
		}
	}
}

// 窗口过期后放行并刷新时间戳（跨进程语义：状态落在文件里）。
func TestThrottlerWindowExpiry(t *testing.T) {
	dir := t.TempDir()
	th := NewThrottler(WithThrottleDir(dir), WithThrottleWindow(50*time.Millisecond))
	if allowed, _ := th.Allow("kimi", "s", "error"); !allowed {
		t.Fatal("首调应放行")
	}
	time.Sleep(80 * time.Millisecond) // 越过窗口；余量对 -race 并行也够
	allowed, err := th.Allow("kimi", "s", "error")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("窗口过期后应放行")
	}
	// 刷新过的时间戳落了盘：立即再调又被拦。
	if allowed, _ := th.Allow("kimi", "s", "error"); allowed {
		t.Fatal("刷新后窗口内应拦截")
	}
}

// window<=0 关闭节流：一律放行且不落状态文件。
func TestThrottlerDisabled(t *testing.T) {
	// 用尚未存在的子目录做状态目录：关闭节流时它不该被创建。
	dir := filepath.Join(t.TempDir(), "state")
	th := NewThrottler(WithThrottleDir(dir), WithThrottleWindow(0))
	for i := 0; i < 3; i++ {
		if allowed, err := th.Allow("cc", "s", "input_required"); !allowed || err != nil {
			t.Fatalf("关闭节流后第 %d 次 allowed=%v err=%v", i+1, allowed, err)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("关闭节流不应创建状态目录: %v", err)
	}
}

// WithThrottleDir("") 空值守卫（runtime#84）：空串语义是「保持缺省目录」，
// 不是「把目录覆盖成空串」——后者让 MkdirAll 必败、Allow 每次走 fail-open
// 放行，节流整体静默失效。环境变量全部指进临时目录隔离：断言用的是缺省
// 目录本身，绝不触碰真实用户缓存。
func TestWithThrottleDirEmptyKeepsDefault(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache) // linux
	t.Setenv("HOME", cache)           // darwin
	t.Setenv("LocalAppData", cache)   // windows
	want := NewThrottler().dir
	got := NewThrottler(WithThrottleDir(""))
	if got.dir == "" || got.dir != want {
		t.Fatalf("WithThrottleDir(\"\") 应保持缺省目录: got %q, want %q", got.dir, want)
	}
	// 缺省装配下节流真实生效：首调放行、窗口内二调拦截，全程无 fail-open 错误
	// （带错误信号的是故障路径，缺省装配不该进它）。
	allowed, err := got.Allow("zcode", "s-84", "task_complete")
	if err != nil || !allowed {
		t.Fatalf("缺省目录首调应放行且无错误: allowed=%v err=%v", allowed, err)
	}
	if allowed, err := got.Allow("zcode", "s-84", "task_complete"); allowed || err != nil {
		t.Fatalf("缺省目录窗口内二调应拦截且无错误: allowed=%v err=%v", allowed, err)
	}
}

// fail-open 纪律：状态文件损坏按过期处理；目录不可写仍放行并带出错误。
func TestThrottlerFailOpen(t *testing.T) {
	dir := t.TempDir()
	th := NewThrottler(WithThrottleDir(dir), WithThrottleWindow(time.Minute))
	key := throttleKey("zcode", "s", "task_complete")
	stamp := filepath.Join(dir, key+".stamp")
	// 1) 损坏的时间戳内容 → 解析失败按过期，放行并重写。
	if err := os.WriteFile(stamp, []byte("not-a-timestamp"), 0o600); err != nil {
		t.Fatal(err)
	}
	allowed, err := th.Allow("zcode", "s", "task_complete")
	if err != nil || !allowed {
		t.Fatalf("损坏状态应 fail-open: allowed=%v err=%v", allowed, err)
	}
	// 重写后窗口内恢复拦截。
	if allowed, _ := th.Allow("zcode", "s", "task_complete"); allowed {
		t.Fatal("重写后窗口内应拦截")
	}
	// 2) 未来的时间戳（时钟回拨/他机写入）在窗口内仍拦。
	if err := os.WriteFile(stamp, []byte(strconv.FormatInt(time.Now().Add(time.Hour).UnixNano(), 10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if allowed, _ := th.Allow("zcode", "s", "task_complete"); allowed {
		t.Fatal("未来时间戳应视为窗口内")
	}
	// 3) 目录被文件占位（MkdirAll 失败）→ 放行 + 错误可观测。
	blocked := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	th2 := NewThrottler(WithThrottleDir(blocked), WithThrottleWindow(time.Minute))
	allowed, err = th2.Allow("cc", "s", "permission_request")
	if !allowed {
		t.Fatalf("目录不可写应 fail-open 放行: %v", err)
	}
	if err == nil {
		t.Fatal("fail-open 时错误仍应带回给调用方观测")
	}
}
