package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oagentkit/runtime/notifyx"
)

// sinkServer 记录到达载荷的测试 webhook：POST 解码存档，可选按序返回状态码。
func sinkServer(t *testing.T, statuses ...int) (*httptest.Server, *sinkRecorder) {
	t.Helper()
	rec := &sinkRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p notifyx.Payload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("sink 收到非 JSON: %v", err)
		}
		rec.add(p, r.Header.Get("Authorization"))
		status := http.StatusOK
		if len(statuses) > 0 {
			status = statuses[0]
			statuses = statuses[1:]
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

type sinkRecorder struct {
	mu       sync.Mutex
	payloads []notifyx.Payload
	auths    []string
}

func (s *sinkRecorder) add(p notifyx.Payload, auth string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads = append(s.payloads, p)
	s.auths = append(s.auths, auth)
}

func (s *sinkRecorder) snapshot() ([]notifyx.Payload, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	auth := ""
	if len(s.auths) > 0 {
		auth = s.auths[0]
	}
	return append([]notifyx.Payload(nil), s.payloads...), auth
}

// baseArgs 公共参数：状态目录一律指到测试临时目录，绝不碰真实用户缓存。
func baseArgs(sinkURL, throttleDir string) []string {
	return []string{"--agent", "zcode", "--url", sinkURL, "--throttle-dir", throttleDir}
}

// 端到端：stdin 收 zcode Stop → 归一化为 task_complete → POST 送达。
func TestRunDeliversNormalizedPayload(t *testing.T) {
	srv, rec := sinkServer(t)
	code := run(baseArgs(srv.URL, t.TempDir()),
		strings.NewReader(`{"hook_event_name":"Stop","session_id":"s-9","message":"done"}`),
		new(strings.Builder), new(strings.Builder))
	if code != exitOK {
		t.Fatalf("exit=%d, want 0", code)
	}
	got, _ := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("应送达 1 条, got %d", len(got))
	}
	if got[0].Event != notifyx.EventTaskComplete || got[0].SessionID != "s-9" {
		t.Fatalf("归一化结果不对: %+v", got[0])
	}
}

// --header k=v 凭证注入透传到 webhook 请求头。
func TestRunDeliversWithHeader(t *testing.T) {
	srv, rec := sinkServer(t)
	args := append(baseArgs(srv.URL, t.TempDir()), "--header", "Authorization=Bearer tok")
	if code := run(args, strings.NewReader(`{"event":"Stop"}`), new(strings.Builder), new(strings.Builder)); code != exitOK {
		t.Fatalf("exit=%d", code)
	}
	if _, auth := rec.snapshot(); auth != "Bearer tok" {
		t.Fatalf("Authorization 头 = %q", auth)
	}
}

// 节流端到端：同键第二次在窗口内被跳过（正常退出，服务端只见 1 条）。
func TestRunThrottleSkipsDuplicate(t *testing.T) {
	srv, rec := sinkServer(t)
	dir := t.TempDir()
	args := baseArgs(srv.URL, dir)
	stdin := strings.NewReader(`{"event":"Stop","session_id":"s-1"}`)
	if code := run(args, stdin, new(strings.Builder), new(strings.Builder)); code != exitOK {
		t.Fatalf("首次 exit=%d", code)
	}
	if code := run(args, strings.NewReader(`{"event":"Stop","session_id":"s-1"}`), new(strings.Builder), new(strings.Builder)); code != exitOK {
		t.Fatalf("节流命中应按正常退出（重复不是错误）, exit=%d", code)
	}
	if got, _ := rec.snapshot(); len(got) != 1 {
		t.Fatalf("应只送达 1 条, got %d", len(got))
	}
}

// codex argv 形态：--arg 从首个位置参数取 payload（JSON 追加在命令尾部）。
func TestRunArgPayload(t *testing.T) {
	srv, rec := sinkServer(t)
	args := append([]string{"--agent", "codex", "--url", srv.URL, "--throttle-dir", t.TempDir(), "--arg"},
		`{"type":"agent_permission_request","session_id":"cx-1"}`)
	code := run(args, strings.NewReader("irrelevant-stdin"), new(strings.Builder), new(strings.Builder))
	if code != exitOK {
		t.Fatalf("exit=%d", code)
	}
	got, _ := rec.snapshot()
	if len(got) != 1 || got[0].Event != notifyx.EventPermissionRequest {
		t.Fatalf("codex argv 归一化不对: %+v", got)
	}
}

// ack 轮询端到端：前两次 null（继续等）、第三次 approve → exit 0 + stdout 回执。
func TestRunAckApprove(t *testing.T) {
	sinkSrv, rec := sinkServer(t)
	var polls atomic.Int32
	ackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("session_id"); got != "s-ack" {
			t.Errorf("ack 轮询应带 session_id, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if polls.Add(1) < 3 {
			_, _ = w.Write([]byte(`{"ack": null}`))
			return
		}
		_, _ = w.Write([]byte(`{"ack": {"decision":"approve","operator":"murphy"}}`))
	}))
	t.Cleanup(ackSrv.Close)

	args := append(baseArgs(sinkSrv.URL, t.TempDir()),
		"--ack-url", ackSrv.URL, "--ack-interval", "10ms")
	var out strings.Builder
	code := run(args, strings.NewReader(`{"event":"PermissionRequest","session_id":"s-ack"}`), &out, new(strings.Builder))
	if code != exitOK {
		t.Fatalf("exit=%d, want 0", code)
	}
	var ack notifyx.Ack
	if err := json.Unmarshal([]byte(out.String()), &ack); err != nil {
		t.Fatalf("stdout 应是 ack JSON, got %q: %v", out.String(), err)
	}
	if ack.Decision != "approve" || ack.Operator != "murphy" {
		t.Fatalf("ack 内容不对: %+v", ack)
	}
	if got, _ := rec.snapshot(); len(got) != 1 {
		t.Fatalf("主通知应已送达: %d", len(got))
	}
}

// ack=deny → 专用退出码 5（bash 侧 approve/deny/done 分派）。
func TestRunAckDeny(t *testing.T) {
	sinkSrv, _ := sinkServer(t)
	ackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ack": {"decision":"deny"}}`))
	}))
	t.Cleanup(ackSrv.Close)
	args := append(baseArgs(sinkSrv.URL, t.TempDir()),
		"--ack-url", ackSrv.URL, "--ack-interval", "5ms", "--session-id", "s-deny")
	code := run(args, strings.NewReader(`{"event":"PermissionRequest"}`), new(strings.Builder), new(strings.Builder))
	if code != exitAckDeny {
		t.Fatalf("exit=%d, want 5", code)
	}
}

// ack 轮询超时（一直 null）→ 退出码 4。
func TestRunAckTimeout(t *testing.T) {
	sinkSrv, _ := sinkServer(t)
	ackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ack": null}`))
	}))
	t.Cleanup(ackSrv.Close)
	args := append(baseArgs(sinkSrv.URL, t.TempDir()),
		"--ack-url", ackSrv.URL, "--ack-interval", "5ms", "--ack-timeout", "60ms", "--session-id", "s-t")
	code := run(args, strings.NewReader(`{"event":"PermissionRequest"}`), new(strings.Builder), new(strings.Builder))
	if code != exitAckPoll {
		t.Fatalf("exit=%d, want 4", code)
	}
}

// 输入错误 → 退出码 2：缺必填参数、事件不在映射表、--arg 无位置参数、stdin 为空。
func TestRunInputErrors(t *testing.T) {
	srv, _ := sinkServer(t)
	dir := t.TempDir()
	cases := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"缺 --url", []string{"--agent", "zcode"}, ""},
		{"事件不在映射表", baseArgs(srv.URL, dir), `{"event":"Whatever"}`},
		{"--arg 无位置参数", append(baseArgs(srv.URL, dir), "--arg"), ""},
		{"stdin 为空", baseArgs(srv.URL, dir), ""},
	}
	for _, tc := range cases {
		if code := run(tc.args, strings.NewReader(tc.stdin), new(strings.Builder), new(strings.Builder)); code != exitInput {
			t.Errorf("%s: exit=%d, want 2", tc.name, code)
		}
	}
}

// ack 轮询需要 session_id：payload 与 --session-id 都没有 → 退出码 2。
func TestRunAckRequiresSession(t *testing.T) {
	sinkSrv, _ := sinkServer(t)
	ackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(ackSrv.Close)
	args := append(baseArgs(sinkSrv.URL, t.TempDir()), "--ack-url", ackSrv.URL)
	if code := run(args, strings.NewReader(`{"event":"Stop"}`), new(strings.Builder), new(strings.Builder)); code != exitInput {
		t.Fatalf("exit=%d, want 2", code)
	}
}

// 外发失败（服务端一直 500，重试耗尽）→ 退出码 3，而不是误报成功。
func TestRunSendFailure(t *testing.T) {
	srv, _ := sinkServer(t, http.StatusInternalServerError, http.StatusInternalServerError, http.StatusInternalServerError)
	args := append(baseArgs(srv.URL, t.TempDir()), "--throttle-window", "0") // 关节流，聚焦外发失败路径
	var stderr strings.Builder
	start := time.Now()
	code := run(args, strings.NewReader(`{"event":"Stop"}`), new(strings.Builder), &stderr)
	if code != exitSend {
		t.Fatalf("exit=%d, want 3（stderr: %s）", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "外发失败") {
		t.Fatalf("stderr 应说明外发失败: %q", stderr.String())
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("重试退避拖太久: %s", elapsed)
	}
}
