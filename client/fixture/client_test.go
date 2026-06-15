package fixture

import (
	"errors"
	"sync"
	"testing"

	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

// seqSource 是包内测试共用的确定性 token 源：从 0 单调自增、并发安全。
// 注入它可绕过 HTTP 层，让生成/算法用例对"喂进去的 token 序列"完全可控（见 newScopeWith）。
type seqSource struct {
	mu sync.Mutex
	n  int64
}

func (s *seqSource) Next() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.n
	s.n++
	return v, nil
}

// TestBlockSourceFetchesOnlyWhenExhausted 守护 HiLo 缓存契约：区块内纯本地自增，仅在耗尽时才远调补块。
//
// 验证手段：用块大小 2、两段预置区块的 fake fetch，连取 4 个 token，断言取值恰为两段区块内容
// 且 fetch 只发生在区块边界（耗尽时）。这验证的是"取号语义正确"，补块次数是该语义的直接可观察结果
// （区块边界），非内部机制。
func TestBlockSourceFetchesOnlyWhenExhausted(t *testing.T) {
	blocks := [][2]int64{{0, 2}, {10, 12}}
	var calls int
	src := &blockSource{
		size: 2,
		kind: protocol.KindToken,
		fetch: func(kind string, size int64) (int64, int64, error) {
			if kind != protocol.KindToken || size != 2 {
				t.Fatalf("fetch args = %q/%d", kind, size)
			}
			b := blocks[calls]
			calls++
			return b[0], b[1], nil
		},
	}
	for i, want := range []int64{0, 1, 10, 11} {
		got, err := src.Next()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("Next[%d] = %d, want %d", i, got, want)
		}
	}
	if calls != 2 {
		t.Fatalf("fetch calls = %d, want 2", calls)
	}
}

// TestBlockSourceSurfacesFetchFailure 守护"基建故障不静默产坏数据"契约（B4）的可测观察面：
// daemon 不可达时取号必须以 error 上抛（上层 CaseScope.next 据此 t.Fatalf 终止用例）。
//
// 验证手段：注入一个永远失败的 fetch，断言 Next 返回非 nil error——若实现吞掉了 fetch 错误并
// 返回一个伪造 token，本用例会因 err==nil 而失败，从而抓住"静默坏数据"这一最危险的退化。
// （为什么不在 CaseScope 层验 t.Fatalf：testing.TB 是密封接口、包外无法伪造；t.Fatalf 黏合本身
// 平凡，错误上抛由此用例护住即可。）
func TestBlockSourceSurfacesFetchFailure(t *testing.T) {
	src := &blockSource{
		size: 2,
		kind: protocol.KindToken,
		fetch: func(string, int64) (int64, int64, error) {
			return 0, 0, errors.New("daemon unreachable")
		},
	}
	if _, err := src.Next(); err == nil {
		t.Fatal("expected error when fetch fails, got nil (silent bad token)")
	}
}

// TestBlockSourceRejectsInvalidBlock 守护"拿到非法区块时拒绝而非吐坏号"：
// daemon 回了 end≤start 的空/反向区块时，取号必须报错，而不是返回会造成重复/越界的 token。
//
// 验证手段：注入一个返回 [5,5)（空区块）的 fetch，断言 Next 返回 error。
func TestBlockSourceRejectsInvalidBlock(t *testing.T) {
	src := &blockSource{
		size: 2,
		kind: protocol.KindToken,
		fetch: func(string, int64) (int64, int64, error) {
			return 5, 5, nil
		},
	}
	if _, err := src.Next(); err == nil {
		t.Fatal("expected error on empty token block, got nil")
	}
}
