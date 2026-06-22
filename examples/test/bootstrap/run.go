// Package bootstrap 是被测项目接入 mtkit 的**顶层启动层**（你项目里的 test/bootstrap）。
//
// 本文件演示 SUT 接入方式①：**mtkit 托管 SUT**——框架负责编译、启动、就绪探测、停止。
// 方式②（手动启动 SUT）见同包 run_manual.go。
//
// 这是一个可编译的玩具工程（module 名 `shopdemo` 即「你自己的项目」）；落到你项目时，把 test/ 这套
// 目录搬到 <yourmodule>/test/ 下、改成你的 module 名即可。
package bootstrap

import (
	"context"
	"log"
	"path/filepath"
	"testing"

	mtkit "github.com/McHarvvvy/mtkit"
	fixture "github.com/McHarvvvy/mtkit/client/fixture"
	"github.com/McHarvvvy/mtkit/container"

	"shopdemo/test/contract"

	// fake endpoint 经各包 init() 自注册进内嵌 faked 引擎；daemon == 测试二进制（re-exec），
	// 故 client 与 daemon 都要 blank-import 这些包，引擎才“看得见”这些 endpoint。
	_ "shopdemo/test/fake/grpc"
	_ "shopdemo/test/fake/http"
)

const projectName = "shopdemo"

// Run 是各 entry 测试包 TestMain 的统一入口。resources 由调用方（各包的 getResources）声明本包
// 所需的前置 provision 资源——不同测试包 provision 需求不同，故由包决定、灵活组合，bootstrap 不写死业务 schema。
func Run(m *testing.M, resources ...fixture.Resource) int {
	runtimeDir, err := contract.RuntimeDir()
	if err != nil {
		log.Printf("bootstrap: locate runtime dir: %v", err)
		return 1
	}
	root, err := contract.RepoRoot()
	if err != nil {
		log.Printf("bootstrap: locate repo root: %v", err)
		return 1
	}

	cfg := &mtkit.Config{
		// RuntimeDir 是唯一必填项；落在测试目录下（与测试代码同处、一条 gitignore 兜住所有产物）。
		RuntimeDir: runtimeDir,
		DaemonMode: mtkit.DaemonModeConfig{IdleTTLSeconds: 36},
		ClientMode: mtkit.ClientModeConfig{
			// 首跑要拉镜像 + 全量编译 SUT，会远超默认 60s；放宽到 300s 避免 client 提前超时取消就绪编排。
			HTTPTimeoutSeconds: 300,
		},
		SUT: mtkit.SUTConfig{
			ProcessConfig: mtkit.ProcessConfig{
				Enabled:   true,
				SourceDir: filepath.Join(root, "cmd", "shop"),
				// mtkit_it 标签让 SUT 编入拷贝自 mtkit/addons 的 CA 插件（仅集成测试构建装信任，生产逐字节不含）。
				BuildArgs: []string{"-tags", "mtkit_it"},
				Lifecycle: mtkit.LifecycleConfig{BuildTimeoutSeconds: 120, ReadyTimeoutSeconds: 45, GracePeriodSeconds: 5},
			},
			IdleTTLSeconds: 2,
		},
		// 启用内嵌 faked：endpoint 经上面 blank-import 自注册，监听端口随机、动态 CA。
		Faked: mtkit.FakedConfig{Enabled: true},
	}

	kit, err := mtkit.New(projectName, cfg)
	if err != nil {
		log.Printf("bootstrap: mtkit.New: %v", err)
		return 1
	}
	return kit.
		RegisterContainers(func(ctx context.Context) ([]container.ContainerRegistration, error) {
			return registerDefaultContainers(ctx)
		}).
		RegisterSUT(newSUTBootPlan(sutWorkDir(root))).
		WithSchema(resources). // 持租约后、m.Run 前跨进程幂等执行一次
		Run(m)
}
