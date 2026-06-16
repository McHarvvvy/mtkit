package fixtured

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

// TestInitFileLedgerResetsAppliedKeys 守护账本重置契约（C5）：daemon 启动调 InitFileLedger 会清空上一代残留的 applied 列表。
// 验证逻辑：预置一个非空账本（模拟上一代 daemon 残留），调 InitFileLedger 后读回，断言 applied 为空——
// 保证新容器（空数据）配空账本，client 不会误判某 key 已 applied 而跳过建表（正是账本机制要消灭的状态）。
func TestInitFileLedgerResetsAppliedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), protocol.LedgerFileName)
	// 预置一个非空账本，模拟上一代 daemon 的残留。
	seed, err := json.Marshal(protocol.Ledger{Applied: []string{"mysql.test.users"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, seed, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InitFileLedger(path); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ledger protocol.Ledger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	if len(ledger.Applied) != 0 {
		t.Fatalf("applied = %v, want empty after reset", ledger.Applied)
	}
}
