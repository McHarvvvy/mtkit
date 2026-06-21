package bootstrap

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"

	mtkit "github.com/McHarvvvy/mtkit"
	"github.com/McHarvvvy/mtkit/container"
)

// RunBaseEnvOnly 演示 SUT 接入方式②：**手动启动 SUT**。
//
// 适用场景：你想用自己的方式启动 / 调试 SUT（断点、热重载、改配置反复重启），只让 mtkit 负责
// 把基础环境（daemon + 容器 + 容器 Init）拉起并常驻。与方式①的差别只有两点：
//   - **不调 RegisterSUT、SUT.Enabled 关**：mtkit 不托管 SUT 进程。
//   - **daemon IdleTTL 设很长**：基础环境在你手动起 SUT、跑完 go test 期间不被空闲回收。
//
// 工作流：
//  1. 先把基础环境拉起来（可由一个“只起环境”的 TestMain 调本函数，或专门跑一次 acquire）；
//     daemon 冷启动 → 容器就绪 → 容器 Init（建库 / namespace）执行完。
//  2. 拿到各容器地址，喂给 SUT 配置：两种取法——
//     a) 从 runtime 文件读 SUTEnv（mtkit 注入用的连接串全集，见 RuntimePath / mtruntime.Read）；
//     b) 或在 containers.go 注册时把容器端口**写死**（见 fixedHostPort 注释），SUT 配置直接照抄固定端口。
//  3. 用上述地址手动启动 SUT（务必让 SUT 像生产一样**完整初始化全部依赖**，只是连向容器 / fake）。
//  4. 跑 `go test ./test/entry/...` 完成测试；用例侧与方式①完全一致。
//
// 注意：手动模式下 SUT 工作目录、CA 插件等前提仍要满足（见 sut.go 与 README“接入前提”）。
func RunBaseEnvOnly(m *testing.M) int {
	root, err := repoRoot()
	if err != nil {
		log.Printf("bootstrap: locate repo root: %v", err)
		return 1
	}

	cfg := &mtkit.Config{
		RuntimeDir: filepath.Join(root, filepath.FromSlash(defaultRuntimeSubdir)),
		// 关键差异：长 IdleTTL，让基础环境在你手动起 SUT、跑测试期间常驻不被回收。
		DaemonMode: mtkit.DaemonModeConfig{IdleTTLSeconds: 3600},
		ClientMode: mtkit.ClientModeConfig{HTTPTimeoutSeconds: 300},
		// 关键差异：不启用 SUT，mtkit 只负责容器 + faked。
		Faked: mtkit.FakedConfig{Enabled: true},
	}

	kit, err := mtkit.New(projectName, cfg)
	if err != nil {
		log.Printf("bootstrap: mtkit.New: %v", err)
		return 1
	}
	// 注意：不调 RegisterSUT。容器 provision 仍可经 WithSchema 注入（按需）。
	return kit.
		RegisterContainers(func(ctx context.Context) ([]container.ContainerRegistration, error) {
			return registerDefaultContainers(ctx)
		}).
		Run(m)
}

var _ = os.Getenv // 占位：示意骨架不强求所有 import 都被业务代码用到
