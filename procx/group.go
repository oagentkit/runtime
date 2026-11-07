// group.go —— 平台无关的「进程组托管」接口。
//
// procx 的承诺是「超时/取消后**整棵进程树**一起收场」，而不是只杀直接子进程。
// 这条承诺在 unix 与 windows 上的落地机制完全不同：
//
//	unix    Setpgid 建组 → TERM 整组 → 宽限后 SIGKILL 整组
//	windows Job Object 装树 → TerminateJobObject 整树终止（无 SIGTERM 等价物）
//
// 故把机制按 build tag 拆成两个文件，本文件只留调用面：process.go 只认
// attach/kill/close 三件事，不认任何一种平台的系统调用。
package procx

import (
	"errors"
)

// killMode 终止档位：graceful 对应 unix 的 SIGTERM，forceful 对应 SIGKILL。
//
// 分两档而不是一个「杀」：unix 上 TERM 给子进程清理的机会（flush 日志、
// 删临时文件），直接 SIGKILL 会把它的中间态留在盘上。
type killMode int

const (
	// killGraceful 先礼后兵的「礼」：unix = SIGTERM 整组。
	killGraceful killMode = iota
	// killForceful 宽限到期后的「兵」：unix = SIGKILL 整组。
	killForceful
)

// errGroupGone 目标进程组已不在（子进程恰好自然退出）。
//
// 单独成哨兵而不是让调用方比 errno：unix 是 ESRCH，windows 上根本没有
// 同名 errno——把 errno 判定写进公共代码，等于把平台差异泄进调用面。
var errGroupGone = errors.New("procx: 进程组已不在")

// processGroup 平台进程树的托管句柄。
type processGroup interface {
	// attach 在 cmd.Start() **之后**调用：windows 必须拿到 pid 才能把进程
	// 放进 Job（早于 Start 没有 pid），unix 的 Setpgid 已在 Start 时生效，
	// 故这里对 unix 是空操作。
	attach() error
	// kill 终止整棵树；目标已不在时返回 errGroupGone。
	kill(mode killMode) error
	// close 释放平台资源（windows 的 Job 句柄）；幂等。
	close()
}
