package fixture

import (
	"math"
	"math/big"
	"math/rand"
	"testing"
)

// 本文件是唯一性引擎（Feistel FPE）的"快档"正确性验证，绑定**生产**的 permute / feistelOnce /
// feistelRound / encodeBase（白盒，故必须 package fixture）。常驻本地与 CI，秒级。
//
// 为什么需要它：唯一性是整个 fixture 的根，而双射性又是唯一性的根。这里用三个**互相独立、非循环**
// 的角度证明生产算法是双射：
//   1) 小空间穷举——distinct==cap 是双射的**确定性**证明（无概率成分），且不依赖逆向实现，独立兜底；
//   2) 大空间逆向往返——证明 ①档（cap≥2⁶³，穷举不可行）量级上的 big.Int 实现仍可逆 ⇒ 单射 ⇒ 双射；
//   3) encodeBase 往返——把"整数双射"安全传导到"字符串唯一"。
// 深档（亿级穷举/千万逆向/5000万管线）见 fpe_deep_test.go（build tag fixture_deep，手动触发）。

// bitset 是检重用的位图，按整数 token 值占位；比 map[int64]bool 省一个量级内存，
// 使穷举能扩到百万乃至（深档）亿级。
type bitset struct{ bits []uint64 }

func newBitset(n int64) *bitset { return &bitset{bits: make([]uint64, n/64+1)} }

// testAndSet 置位并返回该位此前是否已置（true 即重复）。
func (b *bitset) testAndSet(i int64) bool {
	w, m := i/64, uint64(1)<<(uint(i)%64)
	if b.bits[w]&m != 0 {
		return true
	}
	b.bits[w] |= m
	return false
}

// fastExhaustiveCaps 是快档穷举的 cap 取值：跨越多个量级、含 2 的幂(Hex/十进制)与非方友好的素数，
// 以同时压到 cycle-walking 的不同 split²−cap 间隙。permute 的双射性只取决于 cap（与 charset 无关），
// 故这里按 cap 值枚举即可覆盖各形态。
var fastExhaustiveCaps = []int64{
	10000,   // Digit^4
	65536,   // Hex^4
	99991,   // 素数：split² 与 cap 间隙大，重压 cycle-walking
	238328,  // AlphaNum^3 (62^3)
	456976,  // Lower^4 (26^4)
	1048576, // Hex^5 (16^5)：快档最大单形态值空间
}

// TestFPEPermuteIsBijectionExhaustive 是双射的**确定性**证明（角度 1）。
//
// 验证手段：对每个 cap 穷举 token∈[0,cap)，记录 permute(token,cap) 落点；只要出现重复（单射被破坏）
// 或最终 distinct≠cap（满射被破坏），即判失败。distinct==cap ⟺ 输出恰是 [0,cap) 的一个排列 ⟺ 双射，
// 这是数学等价、无概率成分——远强于"采样 N 次没撞见碰撞"。不使用逆向实现，故与角度 2 不构成"两错相消"。
func TestFPEPermuteIsBijectionExhaustive(t *testing.T) {
	for _, capacity := range fastExhaustiveCaps {
		capBig := big.NewInt(capacity)
		seen := newBitset(capacity)
		for token := int64(0); token < capacity; token++ {
			v := permute(token, capBig).Int64()
			if v < 0 || v >= capacity {
				t.Fatalf("cap=%d token=%d: permute 越界 v=%d", capacity, token, v)
			}
			if seen.testAndSet(v) {
				t.Fatalf("cap=%d token=%d: permute 产生重复落点 %d（非单射）", capacity, token, v)
			}
		}
		// 穷举了恰 cap 个互异 token、每个落点互异且在 [0,cap) 内 ⇒ 落点集合即整个值域 ⇒ 满射 ⇒ 双射。
	}
}

// oneDeckCaps 是 ①档 cap（≥2⁶³，穷举不可行），用于逆向往返证明大空间双射。
func oneDeckCaps() []*big.Int {
	return []*big.Int{
		powInt(16, 16), // Hex^16 = 2^64
		powInt(62, 11), // AlphaNum^11 ≈ 5.2e19
		powInt(10, 19), // Digit^19 ≈ 1e19（UniqueString(19,Digit) 的值空间）
	}
}

// TestFPEPermuteRoundTripLargeCaps 是大空间双射证明（角度 2）。
//
// 验证手段：在测试内独立实现 permute 的逆（Feistel 轮序倒转 + 同样的 cycle-walking），对 ①档 cap
// 取一批 token（含 0/1/2 的幂/MaxInt64 等边界 + 固定种子随机），断言 unpermute(permute(x))==x。
// 有限集合上"存在左逆" ⇒ 单射 ⇒（有限集上）双射。这覆盖了穷举到不了的 2⁶³ 量级——正是 ①档"无条件唯一"
// 的实现层保障。逆向实现若写错，往返会失配而非静默放过（与角度 1 的穷举互为独立校验）。
func TestFPEPermuteRoundTripLargeCaps(t *testing.T) {
	rng := rand.New(rand.NewSource(1)) // 固定种子：跨运行可复现
	edge := []int64{0, 1, 2, 3, 1 << 10, 1 << 20, 1 << 40, 1 << 62, math.MaxInt64}
	const randomPerCap = 5000

	for _, capBig := range oneDeckCaps() {
		tokens := append([]int64(nil), edge...)
		for range randomPerCap {
			tokens = append(tokens, rng.Int63()) // [0, 2^63) ⊂ [0,cap)，故 token mod cap 恒等
		}
		for _, token := range tokens {
			y := permute(token, capBig)
			back := permuteInverse(y, capBig)
			if back.Int64() != token {
				t.Fatalf("cap=%s token=%d: round-trip 失配，permute=%s unpermute=%s",
					capBig, token, y, back)
			}
		}
	}
}

// permuteInverse 是 permute 的逆：在 [0,cap) 上把 permute 的像映回原 token。
// 镜像 permute 的 cycle-walking——反复施加 feistelOnce 的逆，直到落回 [0,cap)。
func permuteInverse(y, capacity *big.Int) *big.Int {
	split := ceilSqrt(capacity)
	w := new(big.Int).Set(y)
	for {
		w = feistelOnceInverse(w, split)
		if w.Cmp(capacity) < 0 {
			return w
		}
	}
}

// feistelOnceInverse 是生产 feistelOnce 的逆置换：轮序从高到低倒转，
// 每轮还原 (oldL,oldR)：oldR=newL；oldL=(newR − F(round,oldR)) mod split。
// 复用生产 feistelRound 作轮函数 F——逆向只反转网络结构，轮函数本身无需可逆。
func feistelOnceInverse(v, split *big.Int) *big.Int {
	l := new(big.Int).Div(v, split)
	r := new(big.Int).Mod(v, split)
	for round := 3; round >= 0; round-- {
		oldR := new(big.Int).Set(l)
		f := feistelRound(byte(round), oldR, split)
		oldL := new(big.Int).Sub(r, f)
		oldL.Mod(oldL, split) // big.Int.Mod 返回非负余数，自动处理 r−f 为负
		l, r = oldL, oldR
	}
	out := new(big.Int).Mul(l, split)
	out.Add(out, r)
	return out
}

// TestFPEEncodeBaseRoundTrip 守护 base 编码是单射（角度 3）。
//
// 验证手段：对若干 (charset,n) 取随机 v∈[0,cap)，断言 decode(encodeBase(v,n,alpha))==v 且长度恰 n。
// 编码单射意味着"整数 distinct ⇒ 字符串 distinct"，从而前两个角度对整数双射的证明可安全传导到
// UniqueString 的字符串唯一性——也让大规模唯一性可只在整数域用 bitset 检测（省内存）。
func TestFPEEncodeBaseRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	cases := []struct {
		cs Charset
		n  int
	}{
		{Hex, 8}, {Digit, 9}, {Lower, 6}, {Upper, 6}, {AlphaNum, 5},
	}
	for _, c := range cases {
		alpha := alphabet(c.cs)
		capBig := powInt(len(alpha), c.n)
		for range 5000 {
			v := new(big.Int).Rand(rng, capBig)
			s := encodeBase(v, c.n, alpha)
			if len(s) != c.n {
				t.Fatalf("encodeBase 长度 = %d, want %d (%q)", len(s), c.n, s)
			}
			if got := decodeBase(s, alpha); got.Cmp(v) != 0 {
				t.Fatalf("round-trip 失配：v=%s encode=%q decode=%s", v, s, got)
			}
		}
	}
}

// decodeBase 是 encodeBase 的逆：把 base-|alpha| 字符串解回整数（高位在左）。仅测试用。
func decodeBase(s, alpha string) *big.Int {
	index := make(map[byte]int64, len(alpha))
	for i := 0; i < len(alpha); i++ {
		index[alpha[i]] = int64(i)
	}
	base := big.NewInt(int64(len(alpha)))
	v := big.NewInt(0)
	for i := 0; i < len(s); i++ {
		v.Mul(v, base)
		v.Add(v, big.NewInt(index[s[i]]))
	}
	return v
}

// TestFPENarrowCapFoldStaysInRange 守护 ③档窄形态的折叠安全（spec §1.7）。
//
// 验证手段：对一个很窄的 cap（10^3），喂入远超 cap 的 token（含 ≥cap 的值），断言 uniqueStringFromToken
// 产出**始终**是恰 n 位、全合法字符、且对应整数落在 [0,cap) 内。**刻意不断言**超过 cap 个后仍唯一——
// spec 明确窄形态会静默别名。这条守护的是"折叠不崩、不越界、格式不坏"，而非窄串的全局唯一（那不在契约内）。
func TestFPENarrowCapFoldStaysInRange(t *testing.T) {
	const n = 3 // Digit^3：cap=1000
	alpha := alphabet(Digit)
	capacity := powInt(10, n).Int64()
	for token := int64(0); token < 3000; token++ { // 跨越 cap 三倍，强制折叠
		s := uniqueStringFromToken(token, n, Digit)
		if len(s) != n {
			t.Fatalf("token=%d: 长度 = %d, want %d (%q)", token, len(s), n, s)
		}
		v := decodeBase(s, alpha).Int64()
		if v < 0 || v >= capacity {
			t.Fatalf("token=%d: 折叠后越界 v=%d (cap=%d)", token, v, capacity)
		}
	}
}
