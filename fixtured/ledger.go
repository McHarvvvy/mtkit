package fixtured

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

// InitFileLedger 把 provision 账本写空 {"applied":[]}，供 daemon 启动时重置。
//
// 重要（时序约束）：账本截断必须 happens-before daemon 对外可发现（绑定端口 / 写 runtime）。
// 否则新 daemon（新容器=空数据）若先让 client 拿到租约、再截断账本，client 可能 EnsureOnce 读到
// 上一代残留账本，误判已 applied 而跳过建表，导致新容器里表不存在——正是本机制要消灭的状态。
// 故调用方（orchestrator daemon Start）须在 net.Listen / 写 runtime 之前同步完成本调用。
//
// 本函数只依赖账本路径 + protocol.Ledger 空值，归属 daemon 包，刻意不 import client/fixture：
// daemon 逻辑包与测试侧逻辑包互不 import，唯一共享物是 fixtured/protocol leaf。
func InitFileLedger(ledgerPath string) error {
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(protocol.Ledger{Applied: []string{}})
	if err != nil {
		return err
	}
	return os.WriteFile(ledgerPath, b, 0o644)
}
