// Package unique 是「唯一性引擎」算法的 spike 验证（非正式实现）。
//
// 它把 fixture-design §3.1 的算法**自包含**地实现一遍并做压力测试，用于在动手写
// client/fixture 之前先证明算法可靠：
//   - L0：daemon 串行发块（allocator）+ 进程内本地区块缓存（blockSource）。
//   - 单射：token →(仿射保形置换)→ base 编码，恰好 n 字符、全宽外观、注入。
// 三层可靠性各有专测：进程内跨 goroutine、跨进程全局、置换本身的双射性。
//
// 全部逻辑在本测试文件内，不污染正式包；正式实现落地后本目录可删。
package unique

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"sync"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// L0 服务端：daemon 串行发块（每 kind 一个计数器）。用 mutex 守 map，求「显然正确」。
// ─────────────────────────────────────────────────────────────────────────────

type allocator struct {
	mu       sync.Mutex
	counters map[string]int64
}

func newAllocator() *allocator { return &allocator{counters: map[string]int64{}} }

// allocIDBlock 串行返回 [start, start+size) 半开区间，跨调用者天然不相交。
func (a *allocator) allocIDBlock(kind string, size int64) (start, end int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	start = a.counters[kind]
	a.counters[kind] = start + size
	return start, start + size
}

// ─────────────────────────────────────────────────────────────────────────────
// L0 进程侧：本地区块缓存。一个进程一个（按 kind），所有 goroutine 共享、mu 保护。
// 锁只在「区块耗尽补块」时跨网络持有；区块内是纯本地自增（这里 alloc 是内存调用）。
// ─────────────────────────────────────────────────────────────────────────────

type blockSource struct {
	mu        sync.Mutex
	kind      string
	size      int64
	next, end int64
	alloc     *allocator
}

func (b *blockSource) Next() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.next >= b.end { // 冷路径：补块
		b.next, b.end = b.alloc.allocIDBlock(b.kind, b.size)
	}
	v := b.next
	b.next++
	return v
}

// ─────────────────────────────────────────────────────────────────────────────
// L1 单射：token →(Feistel 保形置换)→ base 编码，恰好 n 字符。
//
//	cap = |cs|^n。permute 是 [0,cap) 上的双射（Feistel 网络 + cycle-walking，FPE 标准构造）：
//	  - split = ⌈√cap⌉，在 [0, split²) 上跑平衡 Feistel（轮函数 = keyed SHA-256 mod split），
//	    这是 [0, split²) 上的置换；若像 ≥ cap 则反复施加直到落回 [0,cap)（cycle-walking）。
//	对任意基数/长度都成立，且充分扩散——避免「单次乘法对 2 的幂模数退化」的坑（见提交说明）。
//	注意：raw token 仍不出现在任何公共签名，这里 int64 token 只是 spike 内部用。
//
// ─────────────────────────────────────────────────────────────────────────────

const (
	symDigit = "0123456789"
	symHex   = "0123456789abcdef"
	symLower = "abcdefghijklmnopqrstuvwxyz"
	symAlnum = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

const feistelRounds = 4 // ≥3 即双射；4 轮换取充分扩散

var feistelKey = []byte("mtkit-fixture-fpe-v1") // 固定 key ⇒ 映射确定、可复现

func capOf(n, base int) *big.Int {
	return new(big.Int).Exp(big.NewInt(int64(base)), big.NewInt(int64(n)), nil)
}

func ceilSqrt(n *big.Int) *big.Int {
	s := new(big.Int).Sqrt(n)
	if new(big.Int).Mul(s, s).Cmp(n) < 0 {
		s.Add(s, big.NewInt(1))
	}
	return s
}

// roundF 是 Feistel 轮函数：keyed SHA-256(round‖R) mod split。
func roundF(round int, r, mod *big.Int) *big.Int {
	h := sha256.New()
	h.Write(feistelKey)
	var rb [8]byte
	binary.BigEndian.PutUint64(rb[:], uint64(round))
	h.Write(rb[:])
	h.Write(r.Bytes())
	d := new(big.Int).SetBytes(h.Sum(nil))
	return d.Mod(d, mod)
}

// feistelOnce 是 [0, split²) 上的一次置换。
func feistelOnce(v, split *big.Int) *big.Int {
	l := new(big.Int).Div(v, split)
	r := new(big.Int).Mod(v, split)
	for i := 0; i < feistelRounds; i++ {
		f := roundF(i, r, split)
		nr := new(big.Int).Add(l, f)
		nr.Mod(nr, split)
		l, r = r, nr
	}
	return l.Mul(l, split).Add(l, r)
}

// permute 把 [0, cap) 双射到自身，且充分扩散。要求 x ∈ [0, cap)。
func permute(x, capacity *big.Int) *big.Int {
	split := ceilSqrt(capacity)
	y := new(big.Int).Set(x)
	for { // cycle-walking：期望迭代 < 2 次（split² 仅略大于 cap）
		y = feistelOnce(y, split)
		if y.Cmp(capacity) < 0 {
			return y
		}
	}
}

func encode(y *big.Int, n int, sym string) string {
	base := big.NewInt(int64(len(sym)))
	q := new(big.Int).Set(y)
	r := new(big.Int)
	buf := make([]byte, n)
	for i := n - 1; i >= 0; i-- { // 低位在右，最高位在左
		q.DivMod(q, base, r)
		buf[i] = sym[r.Int64()]
	}
	return string(buf)
}

// uniqueStringChecked 返回 (值, token 是否在预算内)。token≥cap ⇒ false（紧格式耗尽）。
func uniqueStringChecked(token int64, n int, sym string) (string, bool) {
	capacity := capOf(n, len(sym))
	if big.NewInt(token).Cmp(capacity) >= 0 {
		return "", false
	}
	return encode(permute(big.NewInt(token), capacity), n, sym), true
}

func uniqueString(t testing.TB, token int64, n int, sym string) string {
	t.Helper()
	v, ok := uniqueStringChecked(token, n, sym)
	if !ok {
		t.Fatalf("budget exceeded: token=%d n=%d base=%d", token, n, len(sym))
	}
	return v
}

func uniqueHex(t testing.TB, token int64, n int) string { return uniqueString(t, token, n, symHex) }

func dashUUID(h string) string { // h 长度 32：8-4-4-4-12
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ─────────────────────────────────────────────────────────────────────────────
// 测试工具：用 bitset 在 token 空间检重（避免巨型 map）。
// ─────────────────────────────────────────────────────────────────────────────

type bitset struct{ bits []uint64 }

func newBitset(n int64) *bitset { return &bitset{bits: make([]uint64, n/64+1)} }

// testAndSet 置位；若该位已置返回 true（即重复）。
func (b *bitset) testAndSet(i int64) bool {
	w, m := i/64, uint64(1)<<(uint(i)%64)
	if b.bits[w]&m != 0 {
		return true
	}
	b.bits[w] |= m
	return false
}

// assertTokensUnique 合并所有 goroutine 收集的 token，断言总数与零重复。
func assertTokensUnique(t *testing.T, groups [][]int64, want int) {
	t.Helper()
	var maxTok, total int64
	for _, g := range groups {
		total += int64(len(g))
		for _, v := range g {
			if v > maxTok {
				maxTok = v
			}
		}
	}
	if int(total) != want {
		t.Fatalf("token 总数 = %d，期望 %d", total, want)
	}
	bs := newBitset(maxTok + 1)
	for _, g := range groups {
		for _, v := range g {
			if bs.testAndSet(v) {
				t.Fatalf("发现重复 token: %d", v)
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 测 1：进程内跨 goroutine（同一 blockSource 被并发抽取）+ 区块边界正确性。
// 小 block size 故意制造大量补块，压测边界竞态。需在 -race 下通过。
// ─────────────────────────────────────────────────────────────────────────────

func TestToken_Concurrent_SingleProcess(t *testing.T) {
	const goroutines, perG = 64, 20_000
	bs := &blockSource{kind: "k", size: 64, alloc: newAllocator()} // 小块 → 频繁补块

	groups := make([][]int64, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			local := make([]int64, perG)
			for i := range local {
				local[i] = bs.Next()
			}
			groups[g] = local
		}(g)
	}
	wg.Wait()
	assertTokensUnique(t, groups, goroutines*perG)
}

// ─────────────────────────────────────────────────────────────────────────────
// 测 2：跨进程全局唯一。一个共享 allocator（daemon）+ N 个独立 blockSource（进程），
// 每进程内再开多 goroutine。断言所有进程产出的 token 全局零重复。
// ─────────────────────────────────────────────────────────────────────────────

func TestToken_MultiProcess_GlobalUnique(t *testing.T) {
	const procs, goroutines, perG = 8, 16, 10_000
	alloc := newAllocator() // 唯一串行权威

	all := make([][]int64, 0, procs*goroutines)
	var allMu sync.Mutex
	var procWg sync.WaitGroup
	for p := 0; p < procs; p++ {
		procWg.Add(1)
		go func() {
			defer procWg.Done()
			bs := &blockSource{kind: "k", size: 1024, alloc: alloc} // 每「进程」一个本地缓存
			groups := make([][]int64, goroutines)
			var wg sync.WaitGroup
			for g := 0; g < goroutines; g++ {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					local := make([]int64, perG)
					for i := range local {
						local[i] = bs.Next()
					}
					groups[g] = local
				}(g)
			}
			wg.Wait()
			allMu.Lock()
			all = append(all, groups...)
			allMu.Unlock()
		}()
	}
	procWg.Wait()
	assertTokensUnique(t, all, procs*goroutines*perG)
}

// ─────────────────────────────────────────────────────────────────────────────
// 测 3：置换本身是双射（穷举）。对小 cap 穷举全部 token，断言输出恰好是整个值空间的
// 一个排列（distinct 数 == cap）⇒ 注入且满射 ⇒ 双射。这是「字符串唯一」的数学根基。
// ─────────────────────────────────────────────────────────────────────────────

func TestScramble_IsBijection_Exhaustive(t *testing.T) {
	cases := []struct {
		n, base int
		sym     string
	}{
		{4, 16, symHex},   // cap = 65536
		{5, 10, symDigit}, // cap = 100000
		{3, 62, symAlnum}, // cap = 238328
	}
	for _, c := range cases {
		capacity := capOf(c.n, c.base).Int64()
		seen := make(map[string]struct{}, capacity)
		for tok := int64(0); tok < capacity; tok++ {
			s := uniqueString(t, tok, c.n, c.sym)
			if len(s) != c.n {
				t.Fatalf("n=%d base=%d token=%d 长度=%d", c.n, c.base, tok, len(s))
			}
			if _, dup := seen[s]; dup {
				t.Fatalf("n=%d base=%d token=%d 产生重复串 %q", c.n, c.base, tok, s)
			}
			seen[s] = struct{}{}
		}
		if int64(len(seen)) != capacity {
			t.Fatalf("n=%d base=%d distinct=%d，期望满排列 %d", c.n, c.base, len(seen), capacity)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 测 4：端到端字符串唯一。共享 allocator + 多进程 + 多 goroutine 跑完整管线
// （token→scramble→encode），断言字符串零重复、长度恰好、字符集合法。
// ─────────────────────────────────────────────────────────────────────────────

func TestUniqueString_EndToEnd_NoCollision(t *testing.T) {
	n := 12
	total := 1_000_000
	if testing.Short() {
		total = 100_000
	}
	const procs, goroutines = 4, 8
	perG := total / (procs * goroutines)
	total = perG * procs * goroutines

	alloc := newAllocator()
	parts := make([][]string, 0, procs*goroutines)
	var mu sync.Mutex
	var pwg sync.WaitGroup
	for p := 0; p < procs; p++ {
		pwg.Add(1)
		go func() {
			defer pwg.Done()
			bs := &blockSource{kind: "str/12/lower", size: 4096, alloc: alloc}
			local := make([][]string, goroutines)
			var wg sync.WaitGroup
			for g := 0; g < goroutines; g++ {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					out := make([]string, perG)
					for i := range out {
						out[i] = uniqueString(t, bs.Next(), n, symLower)
					}
					local[g] = out
				}(g)
			}
			wg.Wait()
			mu.Lock()
			parts = append(parts, local...)
			mu.Unlock()
		}()
	}
	pwg.Wait()

	seen := make(map[string]struct{}, total)
	for _, part := range parts {
		for _, s := range part {
			if len(s) != n {
				t.Fatalf("长度 %d != %d: %q", len(s), n, s)
			}
			for _, ch := range s {
				if ch < 'a' || ch > 'z' {
					t.Fatalf("非法字符 %q in %q", ch, s)
				}
			}
			if _, dup := seen[s]; dup {
				t.Fatalf("重复字符串: %q", s)
			}
			seen[s] = struct{}{}
		}
	}
	if len(seen) != total {
		t.Fatalf("distinct=%d，期望 %d", len(seen), total)
	}
	t.Logf("端到端 %d 个 UniqueString(12,Lower) 零碰撞；样例: %q %q %q",
		total, mustGen(t, alloc, n, symLower), mustGen(t, alloc, n, symLower), mustGen(t, alloc, n, symLower))
}

func mustGen(t *testing.T, a *allocator, n int, sym string) string {
	bs := &blockSource{kind: "sample", size: 8, alloc: a}
	return uniqueString(t, bs.Next(), n, sym)
}

// ─────────────────────────────────────────────────────────────────────────────
// 测 5：UUID = 32 hex（无 v4 语义）。断言唯一、长度 32、hex 合法；dashed 同样唯一且格式正确。
// ─────────────────────────────────────────────────────────────────────────────

func TestUniqueHex32_UUID_NoCollision(t *testing.T) {
	total := 500_000
	if testing.Short() {
		total = 50_000
	}
	bs := &blockSource{kind: "uuid", size: 4096, alloc: newAllocator()}
	seen := make(map[string]struct{}, total)
	for i := 0; i < total; i++ {
		h := uniqueHex(t, bs.Next(), 32)
		if len(h) != 32 {
			t.Fatalf("hex 长度 %d", len(h))
		}
		for _, ch := range h {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
				t.Fatalf("非 hex 字符 %q in %q", ch, h)
			}
		}
		d := dashUUID(h)
		if len(d) != 36 || d[8] != '-' || d[13] != '-' || d[18] != '-' || d[23] != '-' {
			t.Fatalf("dashed 格式错误: %q", d)
		}
		if _, dup := seen[h]; dup {
			t.Fatalf("重复 UUID: %q", h)
		}
		seen[h] = struct{}{}
	}
	t.Logf("%d 个 32-hex UUID 零碰撞；样例: %s  dashed=%s",
		total, uniqueHex(t, bs.Next(), 32), dashUUID(uniqueHex(t, bs.Next(), 32)))
}

// ─────────────────────────────────────────────────────────────────────────────
// 测 6：全宽外观——首字符在字符集上铺开，证明 scramble 摊开了 token、无固定前缀
// （对比「左填充」方案：首字符会恒为 cs[0]）。
// ─────────────────────────────────────────────────────────────────────────────

func TestUniqueString_FirstCharSpread(t *testing.T) {
	bs := &blockSource{kind: "spread", size: 4096, alloc: newAllocator()}
	const n, base, samples = 8, 16, 20_000
	first := make(map[byte]int)
	for i := 0; i < samples; i++ {
		s := uniqueHex(t, bs.Next(), n)
		first[s[0]]++
	}
	if len(first) < base/2 {
		t.Fatalf("首字符只覆盖 %d/%d 个符号，疑似未摊开", len(first), base)
	}
	t.Logf("首字符覆盖 %d/%d 个 hex 符号（全宽外观成立）", len(first), base)
}

// ─────────────────────────────────────────────────────────────────────────────
// 测 7：紧格式预算。4 位十进制 cap=10000：token 0..9999 全部唯一且铺满；第 10001 个
// （token=10000）超预算被检出 ⇒ 生产里 t.Fatalf，不静默回绕。
// ─────────────────────────────────────────────────────────────────────────────

func TestBudget_TightFormat_Exhaustion(t *testing.T) {
	const n, base = 4, 10
	capacity := capOf(n, base).Int64() // 10000
	seen := make(map[string]struct{}, capacity)
	for tok := int64(0); tok < capacity; tok++ {
		s, ok := uniqueStringChecked(tok, n, symDigit)
		if !ok {
			t.Fatalf("token=%d 不应超预算（cap=%d）", tok, capacity)
		}
		if _, dup := seen[s]; dup {
			t.Fatalf("token=%d 产生重复 %q", tok, s)
		}
		seen[s] = struct{}{}
	}
	if int64(len(seen)) != capacity {
		t.Fatalf("distinct=%d，期望铺满 %d", len(seen), capacity)
	}
	if _, ok := uniqueStringChecked(capacity, n, symDigit); ok {
		t.Fatalf("token=%d 超预算却未被检出（会静默回绕碰撞）", capacity)
	}
	t.Logf("4 位十进制铺满 %d 个唯一值，第 %d 个正确报预算耗尽", capacity, capacity+1)
}

// ─────────────────────────────────────────────────────────────────────────────
// 测 8：每个字符位都充分扩散——不止首字符，所有 n 个位置都覆盖字符集 ≥ 半数符号。
// 这是比「测 6 首字符」更强的全宽扩散证明，直接证伪「固定前缀/可预测尾」退化。
// ─────────────────────────────────────────────────────────────────────────────

func TestUniqueHex_AllPositionsSpread(t *testing.T) {
	bs := &blockSource{kind: "spread-all", size: 4096, alloc: newAllocator()}
	const n, base, samples = 16, 16, 50_000
	cover := make([]map[byte]int, n)
	for i := range cover {
		cover[i] = make(map[byte]int)
	}
	for range samples {
		s := uniqueHex(t, bs.Next(), n)
		for pos := 0; pos < n; pos++ {
			cover[pos][s[pos]]++
		}
	}
	for pos := 0; pos < n; pos++ {
		if len(cover[pos]) < base/2 {
			t.Fatalf("位置 %d 只覆盖 %d/%d 个符号，扩散不足", pos, len(cover[pos]), base)
		}
	}
	t.Logf("16 个 hex 位全部覆盖 ≥%d/%d 符号，全宽扩散成立", base/2, base)
}
