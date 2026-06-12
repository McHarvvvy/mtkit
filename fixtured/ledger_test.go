package fixtured

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

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
