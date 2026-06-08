package arch

import (
	"os/exec"
	"strings"
	"testing"
)

// TestArchDependencyDirection 守护 mtkit 的分层依赖方向不变式（评审 C2）。
//
// 它用 `go list -deps` 取每个包的【传递】依赖闭包，断言其中不含被禁的内部包前缀。
// 目的：把「客户端不碰服务端实现、container/sut 不依赖 orchestrator、leaf 包保持纯净」
// 这些靠编译被动维持的约束，固化成可回归的主动守护——段2/3 一旦手滑写错 import 即报错。
func TestArchDependencyDirection(t *testing.T) {
	const mod = "github.com/McHarvvvy/mtkit"

	rules := []struct {
		name      string
		pkgs      string   // go list 模式
		forbidden []string // 依赖闭包中不允许出现的 import path（精确或其子包）
	}{
		{
			name:      "顶层 client（消费端）不得依赖任何服务端实现",
			pkgs:      mod + "/client/...",
			forbidden: []string{mod + "/orchestrator", mod + "/container", mod + "/sut"},
		},
		{
			name:      "container 不得依赖编排 / sut / 客户端",
			pkgs:      mod + "/container/...",
			forbidden: []string{mod + "/orchestrator", mod + "/sut", mod + "/client"},
		},
		{
			name:      "sut 不得依赖编排 / 容器 / 客户端",
			pkgs:      mod + "/sut/...",
			forbidden: []string{mod + "/orchestrator", mod + "/container", mod + "/client"},
		},
		{
			name:      "runtime 是 leaf",
			pkgs:      mod + "/runtime/...",
			forbidden: []string{mod + "/orchestrator", mod + "/container", mod + "/sut", mod + "/client", mod + "/fixtured", mod + "/faked"},
		},
		{
			name:      "constant 是 leaf",
			pkgs:      mod + "/constant/...",
			forbidden: []string{mod + "/orchestrator", mod + "/container", mod + "/sut", mod + "/client"},
		},
	}

	for _, r := range rules {
		for _, dep := range listDeps(t, r.pkgs) {
			for _, f := range r.forbidden {
				if dep == f || strings.HasPrefix(dep, f+"/") {
					t.Errorf("[%s] 违反依赖方向：%s 的依赖闭包包含被禁包 %s", r.name, r.pkgs, dep)
				}
			}
		}
	}
}

func listDeps(t *testing.T, pattern string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pattern).Output()
	if err != nil {
		t.Fatalf("go list -deps %s 失败: %v", pattern, err)
	}
	return strings.Fields(string(out))
}
