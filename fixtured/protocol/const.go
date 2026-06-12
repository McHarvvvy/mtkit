package protocol

import (
	"path/filepath"

	mtconstant "github.com/McHarvvvy/mtkit/constant"
)

// HeaderToken 是 daemon 控制面鉴权头，与 lease、faked 控制面共用同一 token，
// 让 fixture 协调路由能挂在 daemon 主端口上复用同一把鉴权。
const HeaderToken = mtconstant.HeaderToken

// KindToken 是 v1 唯一受支持的发号 kind。
// kind 字段在 wire 层预留：未来若按形态分配独立序列，可在不破坏协议的前提下放开取值。
// 提取到 protocol 是为了 daemon（校验）与 client（请求）共用同一字面量，避免两侧各写一份漂移。
const KindToken = "token"

// PathAlloc 是 fixture token 区块发号端点。
// 前缀 /v1/fixture/ 与 lease（/v1/）、faked（/v1/grpc/、/v1/http/）不冲突。
const PathAlloc = "/v1/fixture/alloc"

// LedgerFileName 是 provision 账本文件名。
// daemon 启动时重置、client 持锁读写，两侧靠此常量落到同一文件，不再各自硬编码。
const LedgerFileName = "mtkit.provision.json"

// LedgerPath 由 runtime 文件路径推算账本路径。
// 账本与 runtime 同目录，故无需在 RuntimeInfo 里新增字段；daemon 与 client 共用此函数保证路径一致。
func LedgerPath(runtimePath string) string {
	return filepath.Join(filepath.Dir(runtimePath), LedgerFileName)
}
