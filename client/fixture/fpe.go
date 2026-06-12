package fixture

import (
	"crypto/sha256"
	"math/big"
)

// feistelKey 是轮函数的固定 key：固定 key 使 permute 成为确定性可复现映射，
// permute(token, cap) 在任何机器、任何次运行产出同一串（token 分配本身才是非确定性的，见 §1.8）。
var (
	feistelKey = []byte("mtkit-fixture-fpe-v1")
	bigOne     = big.NewInt(1)
)

// uniqueStringFromToken 把全局唯一 token 单射成“恰好 n 个 cs 字符”的串。
// 管线：token →(permute 双射)→ [0,cap) 内的值 →(base 编码)→ n 字符。唯一性只靠 permute 的双射性，与编码无关。
func uniqueStringFromToken(token int64, n int, cs Charset) string {
	if n <= 0 {
		panic("fixture: n must be positive")
	}
	alpha := alphabet(cs)
	capacity := powInt(len(alpha), n)
	value := permute(token, capacity)
	return encodeBase(value, n, alpha)
}

// permute 是 [0, cap) 上的确定性双射（保形加密 FPE：平衡 Feistel + cycle-walking）。
//
// 不同输入必映射到不同输出，故碰撞行为是二值阈值而非概率：token < cap 时残差两两不同、碰撞恰为 0；
// 这正是选 Feistel 而非“hash 然后 mod”的原因（后者是概率性碰撞）。
func permute(token int64, capacity *big.Int) *big.Int {
	// cap ≥ 2⁶³ 时 token 恒 < cap，此 Mod 即恒等；cap 较窄时按 cap 折叠（③ 档静默别名，不在唯一性保证内）。
	y := new(big.Int).Mod(big.NewInt(token), capacity)
	split := ceilSqrt(capacity)
	for {
		y = feistelOnce(y, split)
		// cycle-walking：feistelOnce 作用在 [0, split²) ⊇ [0, cap) 上，落到 [cap, split²) 的值重映射，
		// 直到回到 [0, cap)。split² 仅略大于 cap，期望迭代 < 2 次。
		if y.Cmp(capacity) < 0 {
			return y
		}
	}
}

// feistelOnce 是 [0, split²) 上的一次置换：把 v 拆成 (L,R)=(v/split, v%split)，做 4 轮平衡 Feistel。
//
// 平衡 Feistel 对任意轮函数都可逆，故 feistelOnce 恒为双射——双射性与轮数无关；
// 取 4 轮只为足够扩散（规避仿射退化那类长前缀），不是双射的前提。
func feistelOnce(v, split *big.Int) *big.Int {
	l := new(big.Int).Div(v, split)
	r := new(big.Int).Mod(v, split)
	for round := byte(0); round < 4; round++ {
		f := feistelRound(round, r, split)
		nextL := new(big.Int).Set(r)
		nextR := new(big.Int).Add(l, f)
		nextR.Mod(nextR, split)
		l, r = nextL, nextR
	}
	out := new(big.Int).Mul(l, split)
	out.Add(out, r)
	return out
}

// feistelRound 计算轮函数 F = SHA256(key ‖ round ‖ R) mod split。
// 轮函数无需单射（Feistel 的双射性不依赖它），用 keyed SHA-256 是为确定性 + 足够扩散。
func feistelRound(round byte, r, split *big.Int) *big.Int {
	h := sha256.New()
	_, _ = h.Write(feistelKey)
	_, _ = h.Write([]byte{round})
	_, _ = h.Write(r.Bytes())
	sum := h.Sum(nil)
	return new(big.Int).Mod(new(big.Int).SetBytes(sum), split)
}

// ceilSqrt 返回 ⌈√v⌉，保证 split² ≥ cap，使 [0,cap) 嵌入 feistelOnce 的定义域 [0,split²)。
func ceilSqrt(v *big.Int) *big.Int {
	root := new(big.Int).Sqrt(v)
	square := new(big.Int).Mul(root, root)
	if square.Cmp(v) < 0 {
		root.Add(root, bigOne)
	}
	return root
}

// powInt 返回 radix^n（big.Int），即某形态的值空间 cap。
func powInt(radix, n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(int64(radix)), big.NewInt(int64(n)), nil)
}

// encodeBase 把 v（< radix^n）编码成恰好 n 个 alpha 字符，低位在右、高位补零。
func encodeBase(v *big.Int, n int, alpha string) string {
	radix := big.NewInt(int64(len(alpha)))
	x := new(big.Int).Set(v)
	rem := new(big.Int)
	out := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		x.QuoRem(x, radix, rem)
		out[i] = alpha[rem.Int64()]
	}
	return string(out)
}
