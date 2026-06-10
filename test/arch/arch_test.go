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
			// faked 是被 orchestrator 托管的服务端，不得反向依赖编排 / 容器 / sut / 客户端。
			name:      "faked 不得反向依赖编排 / 容器 / sut / 客户端",
			pkgs:      mod + "/faked/...",
			forbidden: []string{mod + "/orchestrator", mod + "/container", mod + "/sut", mod + "/client"},
		},
		{
			// client/fake 只能经 protocol 子包与 faked 对话，不得拽服务端实现（actions / routekey / ca）
			// 进测试 binary。注意：不能直接禁 faked/grpc、faked/http 或 faked——前缀匹配会误伤合法的
			// faked/grpc/protocol、faked/http/protocol；改禁服务端叶子实现包，误 import 服务端入口会
			// 传递拉进它们，照样被逮到。
			name: "client/fake 只依赖 protocol，不碰 faked 服务端实现",
			pkgs: mod + "/client/fake/...",
			forbidden: []string{
				mod + "/orchestrator", mod + "/container", mod + "/sut",
				mod + "/faked/grpc/actions", mod + "/faked/grpc/routekey",
				mod + "/faked/http/actions", mod + "/faked/http/routekey", mod + "/faked/http/ca",
			},
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
