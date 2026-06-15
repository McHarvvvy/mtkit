//go:build fixture_deep

package fixture

import (
	"math/big"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

// 本文件是唯一性引擎的"深档"验证，仅在显式 -tags fixture_deep 时编译运行（默认 go test 与 CI 不碰），
// 供需要时手动触发：go test -tags fixture_deep -race -run TestFPEDeep ./client/fixture/
//
// 它把 fpe_test.go 的三个角度推到穷举不可行/采样易漏的量级：亿级穷举、千万逆向、千万级生产管线
// 并发唯一性、全宽扩散。复用 fpe_test.go 的 bitset / permuteInverse / decodeBase / oneDeckCaps
// （同包、tag 开时一并编译）。

// deepExhaustiveCaps 把穷举推到亿级。distinct==cap 仍是确定性双射证明，只是值空间更大、更接近实战。
// 最大的一档（~9e8）较慢，置于 !Short 下，方便 -short 做一次较快的深档冒烟。
var deepExhaustiveCaps = []int64{
	16777216,  // Hex^6 (16^6)
	100000000, // Digit^8 (1e8)
	268435456, // Hex^7 (16^7) ≈ 2.68e8
}

// TestFPEDeepPermuteIsBijectionExhaustive 在亿级值空间确定性地证明 permute 双射（角度 1 深档）。
//
// 验证手段：同 fast 档的穷举法（distinct==cap ⟺ 双射），cap 提到 1.6e7~9e8。bitset 最大约 112MB。
func TestFPEDeepPermuteIsBijectionExhaustive(t *testing.T) {
	caps := deepExhaustiveCaps
	if !testing.Short() {
		caps = append(caps, 916132832) // AlphaNum^5 (62^5) ≈ 9.16e8，最重一档
	}
	for _, capacity := range caps {
		capBig := big.NewInt(capacity)
		seen := newBitset(capacity)
		for token := int64(0); token < capacity; token++ {
			v := permute(token, capBig).Int64()
			if v < 0 || v >= capacity {
				t.Fatalf("cap=%d token=%d: permute 越界 v=%d", capacity, token, v)
			}
			if seen.testAndSet(v) {
				t.Fatalf("cap=%d token=%d: permute 重复落点 %d（非单射）", capacity, token, v)
			}
		}
		t.Logf("cap=%d 穷举双射通过（%d 个落点恰为全值域）", capacity, capacity)
	}
}

// TestFPEDeepPermuteRoundTripLargeCaps 把 ①档逆向往返推到千万级（角度 2 深档）。
//
// 验证手段：同 fast 档的 unpermute(permute(x))==x，每个 ①档 cap 取 1000 万随机 token + 边界值。
func TestFPEDeepPermuteRoundTripLargeCaps(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const perCap = 10_000_000
	for _, capBig := range oneDeckCaps() {
		for i := range perCap {
			var token int64
			switch i {
			case 0:
				token = 0
			case 1:
				token = 1<<63 - 1 // MaxInt64
			default:
				token = rng.Int63()
			}
			y := permute(token, capBig)
			if permuteInverse(y, capBig).Int64() != token {
				t.Fatalf("cap=%s token=%d: round-trip 失配", capBig, token)
			}
		}
	}
}

// TestFPEDeepPipelineUniquenessConcurrent 是"并发取号 + 完整生产管线"的大规模唯一性证明（角度补强）。
//
// 验证手段：用**生产** blockSource（HiLo 锁）+ 一个原子计数器模拟 daemon allocator，N 个 goroutine 并发
// 抽 token，跑**生产** uniqueStringFromToken(9, Digit)，把每个串 decode 回整数后用原子位图检重。
// 它同时覆盖三处真实退化面：①blockSource 并发补块/游标是否漏发或重发 token；②permute 在大量真实
// token 上是否单射；③encode 是否单射。配 -race 跑。cap=1e9 远大于生成量（2e7），属无折叠区。
// 这取代了旧 spike 的端到端测，但绑定的是生产代码。
func TestFPEDeepPipelineUniquenessConcurrent(t *testing.T) {
	const (
		shapeN     = 9          // Digit^9，cap=1e9，整数可索引
		capacity   = 1000000000 // 1e9
		goroutines = 16
		total      = 20_000_000 // 2e7；可按机器内存上调
	)
	perG := total / goroutines

	var counter int64 // 模拟 daemon 串行发号
	src := &blockSource{
		size: 4096,
		kind: protocol.KindToken,
		fetch: func(kind string, size int64) (int64, int64, error) {
			end := atomic.AddInt64(&counter, size)
			return end - size, end, nil
		},
	}

	seen := newBitset(capacity)
	alpha := alphabet(Digit)
	var dup int64
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perG {
				tok, err := src.Next()
				if err != nil {
					t.Errorf("blockSource.Next: %v", err)
					return
				}
				s := uniqueStringFromToken(tok, shapeN, Digit)
				v := decodeBase(s, alpha).Int64()
				if seen.atomicTestAndSet(v) {
					atomic.AddInt64(&dup, 1)
				}
			}
		}()
	}
	wg.Wait()
	if dup != 0 {
		t.Fatalf("生产管线在 %d 次生成中出现 %d 次碰撞", perG*goroutines, dup)
	}
	t.Logf("生产管线 %d 次并发生成零碰撞", perG*goroutines)
}

// TestFPEDeepFullWidthSpread 守护生产输出"全宽扩散"——每个字符位都铺开，无固定前缀/可预测尾。
//
// 验证手段：取大量 UniqueHex(16) 生产输出，统计每个位置出现的不同符号数，断言每位都覆盖 ≥ 半数
// （8/16）符号。这直接证伪 spec §1.4 记载的"单次仿射模乘退化出 0000… 长前缀"那类失败——若 Feistel
// 扩散不足，某些位会塌缩到少数符号，本用例当场抓住。
func TestFPEDeepFullWidthSpread(t *testing.T) {
	const n, base, samples = 16, 16, 200_000
	scope := newScopeWith(t, &seqSource{})
	cover := make([]map[byte]bool, n)
	for i := range cover {
		cover[i] = make(map[byte]bool)
	}
	for range samples {
		s := scope.UniqueHex(n)
		for pos := 0; pos < n; pos++ {
			cover[pos][s[pos]] = true
		}
	}
	for pos := 0; pos < n; pos++ {
		if len(cover[pos]) < base/2 {
			t.Fatalf("位置 %d 只覆盖 %d/%d 个符号，扩散不足", pos, len(cover[pos]), base)
		}
	}
}

// atomicTestAndSet 是 bitset 的无锁置位（深档并发用例专用）：CAS 设位，返回此前是否已置。
func (b *bitset) atomicTestAndSet(i int64) bool {
	word := &b.bits[i/64]
	mask := uint64(1) << (uint(i) % 64)
	for {
		old := atomic.LoadUint64(word)
		if old&mask != 0 {
			return true
		}
		if atomic.CompareAndSwapUint64(word, old, old|mask) {
			return false
		}
	}
}
