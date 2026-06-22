package bootstrap

import (
	"context"
	"os/exec"
	"path/filepath"

	mtkit "github.com/McHarvvvy/mtkit"

	"shopdemo/test/contract"
)

// sutBootPlan 控制 SUT 进程的启动命令（仅托管模式 run.go 用）。
//
// 容器与 faked 的连接 env 由 mtkit 统一组装、经 in.Env 注入；本 plan 只叠加 SUT 特有 env 与启动命令。
type sutBootPlan struct {
	// workDir 是 SUT 进程工作目录。SUT 二进制建在 RuntimeDir 下、不在源码目录，故工作目录**不能由二进制
	// 路径推导**，必须显式钉死为源码目录，使 SUT 的相对路径加载（配置 / assets）与直接 go run 一致。
	workDir string
}

func newSUTBootPlan(workDir string) mtkit.SUTBootPlan {
	return &sutBootPlan{workDir: workDir}
}

// ProbeAddrs 返回就绪探测地址：mtkit 探到这些端口可连才判 SUT 就绪、放发租约。
// 地址取自 contract（与 request helper 的基址同源）——SUT 端口是项目侧静态约定，mtkit runtime 不暴露它。
func (p *sutBootPlan) ProbeAddrs() []string {
	return []string{contract.SUTHTTPAddr, contract.SUTGRPCAddr}
}

// Command 组装启动 SUT 的 *exec.Cmd。
func (p *sutBootPlan) Command(_ context.Context, in mtkit.StartSUTInput, binaryPath string) (*exec.Cmd, error) {
	// 不绑入参 ctx：SUT 生命周期由 daemon 管控，防止过早 cancel。
	cmd := exec.CommandContext(context.Background(), binaryPath)
	cmd.Dir = p.workDir // 显式钉死工作目录
	// in.Env = mtkit 注入的容器 + faked 连接信息；MergedEnv 把 SUT 特有 env 叠加在其上。
	// 这几个开关是**项目自己的 IT/prod 区分约定，mtkit 完全无感**——由 SUT 自己据此切到 IT 配置覆盖。
	cmd.Env = in.MergedEnv(map[string]string{
		"SHOP_IT_ENABLED": "true",
		"SHOP_ENV":        "local",
	})
	return cmd, nil
}

// sutWorkDir 返回 SUT 源码目录作为进程工作目录。
func sutWorkDir(repoRoot string) string {
	return filepath.Join(repoRoot, "cmd", "shop")
}
