package procx

import (
	"os"
	"strings"
	"testing"
)

// TestInheritAllExceptKeepsNonWhitelistedVars 继承式入口必须带走白名单之外
// 的变量（runtime#26）——这就是它存在的全部理由。
//
// 判据是**成对**的：同一个变量在 InheritAllExcept 里必须在、在 ChildEnv 里
// 必须不在。只断言前一半不足以证明它区别于白名单（万一 baseEnvAllow 哪天被
// 顺手加了一项，单边断言照样绿）。
func TestInheritAllExceptKeepsNonWhitelistedVars(t *testing.T) {
	// SSH_AUTH_SOCK 是 issue 点名的第一条：ssh agent 转发认证。
	t.Setenv("SSH_AUTH_SOCK", "/tmp/procx-itest-agent.sock")

	got := InheritAllExcept(nil)
	if n, vals := countKey(got, "SSH_AUTH_SOCK"); n != 1 || vals[0] != "/tmp/procx-itest-agent.sock" {
		t.Fatalf("SSH_AUTH_SOCK 应被继承且唯一: n=%d vals=%v", n, vals)
	}

	// 对照：白名单式入口必须把它挡在外面（同一变量、同一时刻）。
	if n, _ := countKey(ChildEnv(nil), "SSH_AUTH_SOCK"); n != 0 {
		t.Fatalf("ChildEnv 不该透传 SSH_AUTH_SOCK（实得 %d 次）——若是，本条对照失去区分力", n)
	}
}

// TestInheritAllExceptCoversOpenGitNamespace 开放式命名空间必须整体带上。
//
// 这组变量正是「白名单补枚举不可收敛」的证据：GIT_CONFIG_COUNT + GIT_CONFIG_KEY_n
// /VALUE_n 是**约定式**组合命名，静态清单列不到第 n 项。
func TestInheritAllExceptCoversOpenGitNamespace(t *testing.T) {
	want := map[string]string{
		"GIT_CONFIG_COUNT":   "1",
		"GIT_CONFIG_KEY_0":   "user.signingkey",
		"GIT_CONFIG_VALUE_0": "ABCD1234",
		"GIT_SSH_COMMAND":    "ssh -i /tmp/procx-itest-key",
		"GIT_ASKPASS":        "/tmp/procx-itest-askpass",
		"LC_MESSAGES":        "zh_CN.UTF-8",
	}
	for k, v := range want {
		t.Setenv(k, v)
	}
	got := InheritAllExcept(nil)
	for k, want := range want {
		n, vals := countKey(got, k)
		if n != 1 || vals[0] != want {
			t.Fatalf("%s 应被继承: n=%d vals=%v want=%q", k, n, vals, want)
		}
	}
}

// TestInheritAllExceptDropsListedNames except 名单里的变量必须剔除，且未列出的
// 相邻变量不受影响（成对：同一批变量，只有被点名的那个消失）。
//
// 失效形态：把 except 当成「前缀」或当成「保留名单」都能让这条的某一半变红。
func TestInheritAllExceptDropsListedNames(t *testing.T) {
	t.Setenv("PROCX_ITEST_TOKEN", "keep-me")
	t.Setenv("PROCX_ITEST_SECRET", "drop-me")

	got := InheritAllExcept([]string{"PROCX_ITEST_SECRET"})
	if n, _ := countKey(got, "PROCX_ITEST_SECRET"); n != 0 {
		t.Fatalf("被剔除的变量仍在（%d 次）", n)
	}
	if n, vals := countKey(got, "PROCX_ITEST_TOKEN"); n != 1 || vals[0] != "keep-me" {
		t.Fatalf("未被点名的相邻变量不该受影响: n=%d vals=%v", n, vals)
	}
}

// TestInheritAllExceptDropBeatsDiscipline 剔除优先于纪律追加（issue 原话）。
//
// 两件事一起验：① 剔除会连带压掉尾部缺省项，不会「删了又加回来」；
// ② except 里带 `=` 的写法按名生效（`GIT_TERMINAL_PROMPT=1` 与裸名同义）。
func TestInheritAllExceptDropBeatsDiscipline(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	got := InheritAllExcept([]string{"GIT_TERMINAL_PROMPT=1", "CI"})
	for _, k := range []string{"GIT_TERMINAL_PROMPT", "CI"} {
		if n, vals := countKey(got, k); n != 0 {
			t.Fatalf("%s 被剔除后不得再被缺省追加: n=%d vals=%v", k, n, vals)
		}
	}
}

// TestInheritAllExceptParentValueBeatsDiscipline 父进程已有同名项时保留用户的值、
// 且不并列第二项。
//
// 失效形态：无条件追加 ⇒ `CI=0`（用户显式关掉 CI 语义）被悄悄改成 `CI=1`，
// 或出现两个 CI 让子进程取值依 libc 实现而异（runtime#31 的同族问题）。
func TestInheritAllExceptParentValueBeatsDiscipline(t *testing.T) {
	t.Setenv("CI", "0")
	n, vals := countKey(InheritAllExcept(nil), "CI")
	if n != 1 {
		t.Fatalf("CI 应恰好一次，实得 %d 次: %v", n, vals)
	}
	if vals[0] != "0" {
		t.Fatalf("CI = %q, want \"0\"（父进程的值就是用户的值）", vals[0])
	}
}

// TestInheritAllExceptDisciplinePresentWhenAbsent 负对照：父进程没有时缺省项
// 必须补上。
//
// 期望值按父进程实际取值算，而不是写死 1/0——CI 环境的 `CI=true` 会让写死的
// 断言在本地绿、在 CI 红（或反之）。
func TestInheritAllExceptDisciplinePresentWhenAbsent(t *testing.T) {
	got := InheritAllExcept(nil)
	for _, kv := range []string{"GIT_TERMINAL_PROMPT=0", "CI=1"} {
		name, def, _ := strings.Cut(kv, "=")
		want := def
		if parent, ok := os.LookupEnv(name); ok {
			want = parent
		}
		n, vals := countKey(got, name)
		if n != 1 {
			t.Fatalf("%s 应恰好一次，实得 %d 次: %v", name, n, vals)
		}
		if vals[0] != want {
			t.Fatalf("%s = %q, want %q", name, vals[0], want)
		}
	}
}

// TestInheritAllExceptKeyUniqueness 全局不变量：整份 env 里没有任何同名键。
//
// 这是文档承诺的可执行化——不去看具体哪一项，只看「有没有重复」。
func TestInheritAllExceptKeyUniqueness(t *testing.T) {
	t.Setenv("CI", "from-parent")
	t.Setenv("GIT_TERMINAL_PROMPT", "from-parent")
	for i, env := range [][]string{
		InheritAllExcept(nil),
		InheritAllExcept([]string{"CI"}),
		InheritAllExcept([]string{"GIT_TERMINAL_PROMPT", "PATH"}),
		InheritAllExcept([]string{"CI=0", "CI"}),
	} {
		seen := map[string]int{}
		for _, kv := range env {
			name, _, _ := strings.Cut(kv, "=")
			seen[name]++
		}
		for name, n := range seen {
			if n > 1 {
				t.Errorf("case #%d: %q 重复出现 %d 次", i, name, n)
			}
		}
	}
}

// TestInheritAllExceptNilEqualsEmptyExcept 结构性不变量：nil 与空切片等价，
// 且**父进程的每一项**（按 name 去重后）都在结果里——「继承」的定义本身。
//
// 这条比逐个变量断言更强：它约束的是「没有遗漏」而不是「我想到的那几个在」。
func TestInheritAllExceptNilEqualsEmptyExcept(t *testing.T) {
	a := InheritAllExcept(nil)
	b := InheritAllExcept([]string{})
	if len(a) != len(b) {
		t.Fatalf("nil 与空切片应等价: len %d vs %d", len(a), len(b))
	}

	present := map[string]bool{}
	for _, kv := range a {
		name, _, _ := strings.Cut(kv, "=")
		present[name] = true
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "" {
			continue
		}
		if !present[name] {
			t.Errorf("父进程变量 %q 未被继承——「继承全部」被漏掉了这一项", name)
		}
	}
}
