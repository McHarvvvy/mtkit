package fixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

// defaultRegistryLockTimeout 是未设 ctx 截止时间时的等锁兜底超时。
// provision 在启动阶段，进程卡死的危害远大于超时报错，故宁可超时终止也不无限等锁。
const defaultRegistryLockTimeout = 30 * time.Second

// FileRegistry 是 Registry 的 v1 文本账本实现。
//
// 选文本账本而非 MySQL：Registry 必须在任何资源 provision 之前可用，若用 MySQL 后端就要求先有 MySQL 连接，
// 形成“用 MySQL 来 provision MySQL”的循环依赖。文本账本零基建依赖，任何资源类型都能 provision（详见 ADR-0001）。
//
// 账本生命周期与 daemon/容器严格绑定：daemon 启动时写空（见 fixtured.InitFileLedger），
// 重启即重置，账本与数据同步清零，不会出现“账本说已 applied 但表不存在”。
type FileRegistry struct {
	ledgerPath string
}

// NewFileRegistry 创建文本账本 Registry。ledgerPath 由调用方经 protocol.LedgerPath 从 runtimePath 推算。
func NewFileRegistry(ledgerPath string) *FileRegistry {
	return &FileRegistry{ledgerPath: ledgerPath}
}

// EnsureOnce 在整文件一把锁内执行完整的“读 → 检查 → 执行 → 写”。
//
// 注意：持锁贯穿 fn（即 Resource.Apply），临界区时长 = 最慢一次 Apply（建表/灌种子，可能含网络 DDL），
// 且整文件一把锁会让所有进程、所有 key 串行化。这是为保证“检查+执行+标记”原子性而有意为之
// （否则两进程会同时建表）；provision 低频、DDL 用 IF NOT EXISTS，串行成本可接受——不要理解为“窗口极短”。
func (r *FileRegistry) EnsureOnce(ctx context.Context, key string, fn func() error) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return false, errors.New("provision key is empty")
	}
	if fn == nil {
		return false, errors.New("provision fn is nil")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRegistryLockTimeout)
		defer cancel()
	}
	if err := os.MkdirAll(filepath.Dir(r.ledgerPath), 0o755); err != nil {
		return false, fmt.Errorf("create ledger dir: %w", err)
	}

	f, err := os.OpenFile(r.ledgerPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return false, fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()

	unlock, err := lockFile(ctx, f)
	if err != nil {
		return false, fmt.Errorf("lock ledger: %w", err)
	}
	// defer LIFO：unlock 先于 f.Close 执行，确保先释放文件锁再关闭 fd。
	defer unlock()

	ledger, err := readLedger(f)
	if err != nil {
		return false, err
	}
	for _, applied := range ledger.Applied {
		if applied == key {
			return true, nil
		}
	}
	if err := fn(); err != nil {
		return false, err
	}
	ledger.Applied = append(ledger.Applied, key)
	if err := writeLedger(f, ledger); err != nil {
		return false, err
	}
	return false, nil
}

func readLedger(f *os.File) (protocol.Ledger, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return protocol.Ledger{}, fmt.Errorf("seek ledger: %w", err)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return protocol.Ledger{}, fmt.Errorf("read ledger: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return protocol.Ledger{Applied: []string{}}, nil
	}
	var ledger protocol.Ledger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return protocol.Ledger{}, fmt.Errorf("decode ledger: %w", err)
	}
	if ledger.Applied == nil {
		ledger.Applied = []string{}
	}
	return ledger, nil
}

func writeLedger(f *os.File, ledger protocol.Ledger) error {
	// 先 Seek(0) 再 Truncate(0)：从头覆盖且清掉旧内容，避免残留尾字节污染 JSON。
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek ledger: %w", err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate ledger: %w", err)
	}
	if err := json.NewEncoder(f).Encode(ledger); err != nil {
		return fmt.Errorf("write ledger: %w", err)
	}
	return nil
}
