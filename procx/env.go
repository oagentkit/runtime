package procx

import (
	"os"
	"strings"
)

// baseEnvAllow 子进程缺省环境白名单：路径/临时目录/语言/代理/CA。
// 绝不全量继承——cli agent / MCP server 等待启动的子进程会在用户工作目录执行
// 任意代码，全量环境等于把宿主凭证交给待执行任务。
// 本表是全仓库子进程环境纪律的单一事实源（procx.Run / acpx 适配器 / mcp
// stdio server 共用）；新增基础变量只改这里。
//
// 继承式的入口**不在这里**，它是独立导出的 InheritAllExcept（可信父进程场景）。
// 刻意不做成 ChildEnv 的 mode 开关：那等于给这条纪律留一个旁路，agent 场景
// 误用一行就漏凭证；独立函数让两种威胁模型在调用点字面可见（见其文档）。
var baseEnvAllow = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "USER": true,
	"LOGNAME": true, "SHELL": true,
	"LANG": true, "LC_ALL": true, "TERM": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	// agent 自身的配置目录（登录态）：按名显式放行
	"CLAUDE_CONFIG_DIR": true, "OPENCODE_CONFIG": true,
	"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
}

// ChildEnv 构造子进程最小环境（导出：mcp 等自行 spawn 外部子进程的包共用同一
// 纪律；经 procx.Run 执行时无需调用——Run 内部已应用）：
// 白名单 + 显式透传项 + 禁交互。
//
// 语义是**默认拒绝**（runtime#83）：环境集 = baseEnvAllow 白名单 ∪ extra
// 显式放行，白名单之外的一切变量——包括未来新出现的敏感变量——一律不进
// 子进程环境。刻意用 allowlist 而非 denylist：denylist 只能列已知凭据名，
// 新凭据永远漏；allowlist 的漏只表现为「少了一个无害变量」，错了也错在
// 安全侧。extra 两种形态：纯名（如 "HF_TOKEN"）从当前进程按名透传（不存在
// 则丢弃）；含 "="（如 "HF_TOKEN=xxx"）按 KEY=VALUE 字面注入，且同名父进程
// 值不透传（每 key 唯一，避免 environ 同名两项时子进程取值依实现而异）。
// 尾部固定追加 GIT_TERMINAL_PROMPT=0（git 弹终端提问会挂到超时）与 CI=1——
// 对非 git 子进程无害。
func ChildEnv(extra []string) []string {
	keep := map[string]bool{}
	literal := map[string]string{} // name → KEY=VALUE
	var litOrder []string
	for _, k := range extra {
		if i := strings.IndexByte(k, '='); i > 0 {
			name := k[:i]
			if _, dup := literal[name]; !dup {
				litOrder = append(litOrder, name)
			}
			literal[name] = k
		} else {
			keep[k] = true
		}
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if _, isLit := literal[name]; isLit {
			continue // 字面注入项优先，不透传父进程同名值
		}
		if baseEnvAllow[name] || keep[name] {
			env = append(env, kv)
		}
	}
	for _, name := range litOrder {
		env = append(env, literal[name])
	}
	// 尾部的两个缺省项**按名去重**后再追加（runtime#31）：无条件 append 会在
	// extra 已给 `CI=0` / `GIT_TERMINAL_PROMPT=1` 时产出同名两项，而
	// getenv() 取首个还是末个依 libc 实现而异——这正是本函数上一段注释声称
	// 「每 key 唯一」已消灭的形态。
	//
	// 去重口径统一为**先到先得**：显式给的（字面注入 / 按名透传）压过缺省项，
	// 父进程 environ 里的同名项压过…什么也压不过它自己（见下）。这与
	// getenv() 的「首个命中」语义一致，所以去重不改变子进程实际读到的值，
	// 只是把「读到哪个」从实现相关变成确定的。
	//
	// seen 同时兜住 os.Environ() 自身可能出现的同名项（同为先到先得）。
	out := make([]string, 0, len(env)+2)
	seen := make(map[string]bool, len(env)+2)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, kv)
	}
	for _, kv := range []string{"GIT_TERMINAL_PROMPT=0", "CI=1"} {
		name, _, _ := strings.Cut(kv, "=")
		if seen[name] {
			continue // 显式项在位：缺省值不得覆盖，也不得并列出第二项
		}
		seen[name] = true
		out = append(out, kv)
	}
	return out
}

// InheritAllExcept 返回「继承当前进程环境、剔除 except 名单」的子进程环境
// （runtime#26）：语义上等价于「用户在自己的 shell 里跑这条命令」。
//
// # 什么时候该用它，什么时候绝对不该
//
// 它与 ChildEnv 服务的是**两套威胁模型**，选错哪一边都出事：
//
//   - **ChildEnv（白名单）**：子进程会执行**不可信代码**（cli agent / MCP
//     server 在用户工作目录里跑任务）。全量继承 = 把宿主凭证交给待执行任务，
//     所以必须白名单。
//   - **InheritAllExcept（继承）**：子进程是**用户主动发起的可信 CLI** 的
//     替身——SDK 包装 git / ssh / docker / aws 这类命令，替用户操作他自己的
//     仓库与账号。此时父进程环境**就是**用户的信任上下文，砍掉它才是不对的。
//
// 判断标准是「这段子进程代码是谁写的」：用户自己的工具 → 继承；别人投递来
// 待执行的代码 → 白名单。混淆这条线是本节存在的唯一理由。
//
// # 为什么可信场景不能用白名单「补枚举」
//
// 按名枚举在这个场景里是**不可收敛**的清单，不是「多补几个就够」：
//
//   - `GIT_*` 是开放式命名空间（GIT_AUTHOR_* / GIT_COMMITTER_* / GIT_SSH_* /
//     GIT_CONFIG_COUNT + GIT_CONFIG_KEY_n / GIT_TRACE*…），组合式配置靠约定
//     命名，静态列不全；
//   - `SSH_AUTH_SOCK`（agent 转发认证）与 `GIT_ASKPASS` / `SSH_ASKPASS` /
//     桌面 credential helper（经 PATH 发现）——凭据助手链是活的；
//   - locale 族（LC_* 逐项）影响编码与错误信息；
//   - 各类 SDK 自己的代理/配置变量。
//
// 新 git 版本读新变量、用户装新助手，枚举就漏——而漏的形态是「认证莫名失败」
// 或「读到别人的配置」，不是编译错误。
//
// # except 的口径
//
// except 是**变量名**名单（`[]string{"SSH_AUTH_SOCK"}`）；含 `=` 的项按
// `=` 之前的部分取名（`"GIT_TERMINAL_PROMPT=1"` 同样剔除该项并抑制下方缺省
// 追加），这样「我想剔掉它」与「我想改它的值」不会因为写法不同而行为不同。
//
// 尾部纪律与 ChildEnv 一致，且**剔除优先于追加**：
//
//   - 追加 `GIT_TERMINAL_PROMPT=0`（git 弹终端提问会挂到超时）与 `CI=1`
//     （对非 git 子进程无害）；
//   - 但若某项被 except 剔除，或父进程已有同名项，则不再追加——父进程的
//     值就是用户的值（`CI=0` 不该被悄悄改成 `CI=1`），且每个 key 至多出现
//     一次（environ 同名多项会让子进程取值依 libc 实现而异，runtime#31）。
//
// except 传 nil 即「全量继承 + 两条纪律」，这是最常见的用法。
func InheritAllExcept(except []string) []string {
	drop := make(map[string]bool, len(except))
	for _, k := range except {
		name, _, _ := strings.Cut(k, "=")
		if name != "" {
			drop[name] = true
		}
	}
	parent := os.Environ()
	out := make([]string, 0, len(parent)+2)
	seen := make(map[string]bool, len(parent)+2)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		// drop 优先于 seen：被剔除的项不该因为「先到」而留下。
		if drop[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, kv)
	}
	// 缺省项按名去重后追加，口径与 ChildEnv 逐字一致（先到先得 + 剔除优先）。
	for _, kv := range []string{"GIT_TERMINAL_PROMPT=0", "CI=1"} {
		name, _, _ := strings.Cut(kv, "=")
		if drop[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, kv)
	}
	return out
}
