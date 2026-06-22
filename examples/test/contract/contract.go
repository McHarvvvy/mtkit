// Package contract 是测试侧最底层的「共享契约」叶子包：它只放被多个测试包共用的固定值——
// 环境变量 key、SUT 服务地址、运行时产物路径——本身不依赖任何其它测试包。
//
// 它的存在是为了**打破反向依赖**：这些值原本散在 bootstrap（顶层启动包）里，被 schema / seeders /
// request / harness 反向 import 去取，导致叶子包依赖顶层包。抽到本包后，bootstrap 与各叶子包都只
// 依赖 contract，依赖方向自上而下、无环。
package contract

import (
	"os"
	"path/filepath"
)

// —— 环境变量 key：bootstrap 的容器 SUTEnv 回调按这些 key 吐连接串、注入 SUT；
//    fixture/schema、seeders 按同一 key 经 PersistEnv 取连接。两侧逐字一致才连得上。
const (
	EnvKeyMySQLDSN  = "SHOP_IT_MYSQL_DSN"
	EnvKeyRedisAddr = "SHOP_IT_REDIS_ADDR"
)

// —— SUT 服务地址：项目侧静态约定，mtkit 的 runtime 不暴露它。boot plan 的 ProbeAddrs（就绪探测）
//    与 request helper 的基址都复用这里，单一来源。
const (
	SUTHTTPAddr = "127.0.0.1:18080"
	SUTGRPCAddr = "127.0.0.1:18081"

	// SUTHTTPBaseURL 供 request helper 拼接 endpoint 路径。
	SUTHTTPBaseURL = "http://" + SUTHTTPAddr
)

// —— 运行时产物路径。
const (
	// RuntimeSubdir 是 RuntimeDir 相对仓库根的位置；落在测试目录下便于排查、便于一条 gitignore 兜住。
	RuntimeSubdir = "test/.runtime/mtkit"
	// runtimeFileName 与 mtkit 默认 RuntimeFile 同名；用例侧据 RuntimePath() 复算同一路径懒读 runtime。
	runtimeFileName = "mtkit.runtime.json"
)

// RepoRoot 从当前工作目录向上找含 go.mod 的目录作为工程根（集成测试恒从源码树运行）。
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// RuntimeDir 返回 mtkit 产物目录（工程根 + RuntimeSubdir），供 bootstrap 填进 mtkit.Config.RuntimeDir。
func RuntimeDir() (string, error) {
	root, err := RepoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(RuntimeSubdir)), nil
}

// RuntimePath 返回 mtkit daemon 落盘的 runtime 文件路径，供 fixture/harness 在 m.Run 执行期懒读。
func RuntimePath() (string, error) {
	dir, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, runtimeFileName), nil
}
