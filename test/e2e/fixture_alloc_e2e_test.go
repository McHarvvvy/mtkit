//go:build e2e

package e2e

import (
	"testing"

	fixture "github.com/McHarvvvy/mtkit/client/fixture"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

// TestFixtureAllocE2E 是发号方向的**真实环境 smoke**：经真实 re-exec daemon 的 /v1/fixture/alloc 取号。
//
// 为什么单设此用例：进程内的 httptest 联测（test/fixture/alloc_http_test.go）跑的是手挂 mux + 手写鉴权 wrap，
// **绕过**了三处真实 wiring——①真实跨进程 daemon（client 与 daemon 不在同一进程）；②真实 daemon withAuth
// （用 runtime 文件里下发的真 token，而非测试自造）；③fixture 路由真实挂到 daemon 主 mux（与 lease/faked 并存）。
// 本用例正是覆盖这三处。
//
// 验证手段：用 e2e 既有执行模型 kit.Run(fakeRunnable)——闭包在持租约窗口内（等价 m.Run 位置）运行于 client
// 进程：读 runtime 文件拿真实 Addr/Token → fixture.New 建客户端 → 连取数千个 UUIDNoDash，断言两两互异、
// 长度 32、全 hex。值跨进程经真实 daemon 发号链路产生；断言用生成值自比，不写字面量。
// 用 noop 容器（不拉镜像）即可——发号只依赖 daemon，但 daemon 启动仍走真 facade 的 CleanupOrphans，故需 Docker。
func TestFixtureAllocE2E(t *testing.T) {
	requireDocker(t)
	dir := t.TempDir()
	setupEnv(t, envOpts{
		runtimeDir: dir, container: "noop", containerName: uniqueName("e2e-fxalloc"), idleTTLSec: 5,
	})

	var failMsg string
	code := mustBuildKit(t).Run(fakeRunnable{fn: func() int {
		info, err := mtruntime.Read(runtimeFile(dir))
		if err != nil {
			failMsg = "读 runtime 失败: " + err.Error()
			return 1
		}
		// 用 runtime 下发的真 Addr/Token：经真实 daemon withAuth 鉴权 + 真实 mux 路由发号。
		scope := fixture.New(info.Addr, info.Token).NewScope(t)

		const n = 5000
		seen := make(map[string]bool, n)
		for range n {
			v := scope.UUIDNoDash()
			if len(v) != 32 {
				failMsg = "UUIDNoDash 长度非 32: " + v
				return 1
			}
			for _, ch := range v {
				if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
					failMsg = "非 hex 字符: " + v
					return 1
				}
			}
			if seen[v] {
				failMsg = "出现重复值: " + v
				return 1
			}
			seen[v] = true
		}
		return 0
	}})

	if code != 0 {
		t.Fatalf("fixture alloc e2e 失败 code=%d: %s", code, failMsg)
	}
}
