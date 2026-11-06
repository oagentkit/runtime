package procx

import (
	"strings"
	"testing"
)

// countKey 数 env 里某 name 出现几次，并返回它的值（多项时返回全部值）。
func countKey(env []string, name string) (int, []string) {
	var vals []string
	for _, kv := range env {
		n, v, _ := strings.Cut(kv, "=")
		if n == name {
			vals = append(vals, v)
		}
	}
	return len(vals), vals
}

// TestChildEnvTailDefaultsNotDuplicated 尾部落入的缺省项不得与显式项同名重复
// （runtime#31）。
//
// 失效形态不是「值错」而是「值不可预测」：environ 出现两个 CI 时，子进程
// getenv() 取首个还是末个依 libc 实现而异——同一份配置在不同平台上跑出不同
// 行为，而文档正宣称「每 key 唯一」。
func TestChildEnvTailDefaultsNotDuplicated(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
		key   string
		want  string
	}{
		{"字面注入 CI", []string{"CI=0"}, "CI", "0"},
		{"字面注入 GIT_TERMINAL_PROMPT", []string{"GIT_TERMINAL_PROMPT=1"}, "GIT_TERMINAL_PROMPT", "1"},
		{"两个都覆盖", []string{"CI=0", "GIT_TERMINAL_PROMPT=1"}, "CI", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := ChildEnv(tc.extra)
			n, vals := countKey(env, tc.key)
			if n != 1 {
				t.Fatalf("%s 出现 %d 次（%v），必须恰好一次——重复会让子进程取值依实现而异", tc.key, n, vals)
			}
			if vals[0] != tc.want {
				t.Fatalf("%s = %q, want %q（显式值必须压过缺省）", tc.key, vals[0], tc.want)
			}
		})
	}
}

// TestChildEnvTailDefaultsKeptWhenAbsent 负对照：没显式给时缺省值必须仍在。
//
// 只有「开着绿」的用例等于没钉住——若去重把缺省项整个丢了，上一条只会看到
// 「恰好一次」而不会发现 CI=1 / GIT_TERMINAL_PROMPT=0 已经不在。
func TestChildEnvTailDefaultsKeptWhenAbsent(t *testing.T) {
	env := ChildEnv(nil)
	for _, want := range []struct{ key, val string }{
		{"GIT_TERMINAL_PROMPT", "0"},
		{"CI", "1"},
	} {
		n, vals := countKey(env, want.key)
		if n != 1 {
			t.Fatalf("%s 应恰好一次，实得 %d 次", want.key, n)
		}
		if vals[0] != want.val {
			t.Fatalf("%s = %q, want %q", want.key, vals[0], want.val)
		}
	}
}

// TestChildEnvKeyUniqueness 全局不变量：整份 env 里没有任何同名键。
//
// 这是文档承诺本身的可执行化——不去看具体哪一项，只看「有没有重复」。
func TestChildEnvKeyUniqueness(t *testing.T) {
	t.Setenv("CI", "from-parent")
	t.Setenv("GIT_TERMINAL_PROMPT", "from-parent")
	envs := [][]string{
		ChildEnv(nil),
		ChildEnv([]string{"CI"}),                          // 按名透传父进程的 CI
		ChildEnv([]string{"CI=0", "CI"}),                  // 字面 + 按名同键
		ChildEnv([]string{"PATH", "PATH=/custom/bin"}),    // 白名单键 + 字面同键
		ChildEnv([]string{"GIT_TERMINAL_PROMPT=1", "CI"}), // 缺省项一对，两键都被显式覆盖
	}
	for i, env := range envs {
		seen := map[string]int{}
		for _, kv := range env {
			name, _, _ := strings.Cut(kv, "=")
			seen[name]++
		}
		for name, n := range seen {
			if n > 1 {
				t.Errorf("case #%d: %q 重复出现 %d 次: %v", i, name, n, env)
			}
		}
	}
}

// TestChildEnvLiteralBeatsParent 既有语义回归：字面注入压过父进程同名值。
func TestChildEnvLiteralBeatsParent(t *testing.T) {
	t.Setenv("CHILDENV_TOKEN", "parent")
	env := ChildEnv([]string{"CHILDENV_TOKEN=child"})
	n, vals := countKey(env, "CHILDENV_TOKEN")
	if n != 1 || vals[0] != "child" {
		t.Fatalf("字面注入应唯一且胜出: n=%d vals=%v", n, vals)
	}
}
