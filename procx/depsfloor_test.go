// depsfloor_test.go —— procx 依赖地板的机械化守卫（runtime#83）。
//
// procx 是 agent 无关的通用进程治理原语（最小环境 / 进程树终止 / 等待分类），
// 非 agent 下游「只为 ChildEnv 引入本 module」时，依赖面必须停在本包：
// 不 import 本仓任何其他包（否则下游吃进整条 runtime 依赖边，hotplug /
// notifyx / pacer / progress 全进构建），外部只依赖第一方基座
// oekit/concurrency。这条纪律靠约定守不住——一次顺手 import 就破——
// 这里用 `go list -deps` 对全量依赖闭包逐包断言钉死：go test 即门禁，
// 本地与 CI 同口径，无需单独脚本。
//
// 注意边界：Go 的 module 粒度是整仓，下游的 module graph 里仍会有本仓
// go.mod 的 require 边（含 oekit/pkg）；本守卫钉的是**包级 import 面**
// ——进二进制的代码只有 procx 自己 + 标准库 + oekit/concurrency，README
// 「与 oekit 的关系」一节据此向非 agent 下游表述定位。
package procx

import (
	"os/exec"
	"strings"
	"testing"
)

// depsFloor 本包允许的外部依赖前缀（第一方基座；标准库不在此列——
// 其导入路径首段不含「.」）。
// oekit/pkg 自 concurrency v0.34.0 起进入闭包（async 的 panic 收编消费
// pkg/errors，runtime#211 地板抬升实录）——同属 oekit 第一方基座，随合同放行。
var depsFloor = []string{"github.com/oekit/concurrency", "github.com/oekit/pkg"}

// TestProcxDepsFloor 断言 procx 的全量依赖闭包不越地板：
// 本仓包只准 procx 自身，外部包只准 depsFloor 白名单（oekit/concurrency
// 与 oekit/pkg 两基座），其余必须是标准库。
func TestProcxDepsFloor(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go 工具链不在 PATH，无法断言依赖闭包（CI/本地开发必有工具链）: %v", err)
	}
	// 测试进程的工作目录 = 本包源码目录（go test 语义），「.」即 procx；
	// -deps 列传递闭包，比逐文件解析 import 更硬——间接拖进来的重依赖也逃不掉。
	out, err := exec.Command(goTool, "list", "-deps", "-f", "{{.ImportPath}}", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps 失败: %v\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		if line == "github.com/oagentkit/runtime/procx" {
			continue
		}
		// 本仓其他包：显式禁止（含未来新增的 sibling）。
		if strings.HasPrefix(line, "github.com/oagentkit/runtime/") {
			t.Errorf("procx 不得 import 本仓其他包（runtime#83 依赖地板）: %s", line)
			continue
		}
		// 外部包：白名单只有第一方基座；标准库路径首段无「.」自然放行。
		if first := strings.SplitN(line, "/", 2)[0]; strings.Contains(first, ".") && !allowedDepsFloor(line) {
			t.Errorf("procx 外部依赖超出地板（只允许 %v，runtime#83）: %s", depsFloor, line)
		}
	}
}

// allowedDepsFloor 逐前缀判定（slices.ContainsForm 面太小，显式循环最直白）。
func allowedDepsFloor(line string) bool {
	for _, p := range depsFloor {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}
