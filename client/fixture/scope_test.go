package fixture

import (
	"regexp"
	"strconv"
	"testing"
)

// emailDefaultRe / emailDomainRe 校验 Email 形态：fixture+{16 AlphaNum}@{domain}。
var (
	emailDefaultRe = regexp.MustCompile(`^fixture\+[0-9A-Za-z]{16}@mtkit\.test$`)
	emailCustomRe  = regexp.MustCompile(`^fixture\+[0-9A-Za-z]{16}@corp\.example$`)
)

// TestUniqueIntWithinRange 守护 UniqueInt 的取值域契约（B2）：UniqueInt(d) 恒落在 [0, 10^d) 内。
// 验证逻辑：对每个合法位数 d∈[1,18] 连取多次，断言每个返回值都 ≥0 且 < 10^d（注入 seqSource，与具体 token 无关）。
func TestUniqueIntWithinRange(t *testing.T) {
	scope := newScopeWith(t, &seqSource{})
	for digits := 1; digits <= 18; digits++ {
		upper := pow10(digits)
		for range 64 {
			v := scope.UniqueInt(digits)
			if v < 0 || v >= upper {
				t.Fatalf("UniqueInt(%d) = %d, out of [0,%d)", digits, v, upper)
			}
		}
	}
}

// TestUniqueIntPanicsOnBadDigits 守护 UniqueInt 的参数契约（B5）：位数越界属程序员错误，应 fail-fast panic。
// 验证逻辑：对 0 / -1 / 19 / 20 等非法位数逐一调用，断言每个都触发 panic（而非静默返回越界/坏值）。
func TestUniqueIntPanicsOnBadDigits(t *testing.T) {
	for _, digits := range []int{0, -1, 19, 20} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("UniqueInt(%d) did not panic", digits)
				}
			}()
			newScopeWith(t, &seqSource{}).UniqueInt(digits)
		}()
	}
}

// TestUniqueIntMatchesStringPathByToken 回归等价：新实现（直接置换取整）必须与旧路径
// （置换→十进制串→ParseInt）对同一 token 逐位一致，证明去掉字符串往返是纯重构。
func TestUniqueIntMatchesStringPathByToken(t *testing.T) {
	for digits := 1; digits <= 18; digits++ {
		for token := range int64(200) {
			got := permute(token, powInt(10, digits)).Int64()

			legacyStr := uniqueStringFromToken(token, digits, Digit)
			want, err := strconv.ParseInt(legacyStr, 10, 64)
			if err != nil {
				t.Fatalf("legacy parse %q: %v", legacyStr, err)
			}
			if got != want {
				t.Fatalf("digits=%d token=%d: direct=%d legacy=%d", digits, token, got, want)
			}
		}
	}
}

// TestEmailDefaultDomain 守护 Email 的默认域行为（B2）：省略或空 domain 时回落到 mtkit.test，形态为 fixture+{16位载荷}@域。
// 验证逻辑：Email() 与 Email("") 各生成一个，用正则断言匹配 fixture+[16 AlphaNum]@mtkit.test（空串也走回落分支）。
func TestEmailDefaultDomain(t *testing.T) {
	scope := newScopeWith(t, &seqSource{})
	if e := scope.Email(); !emailDefaultRe.MatchString(e) {
		t.Fatalf("Email() = %q, want default-domain form", e)
	}
	// 空串 domain 回落默认。
	if e := scope.Email(""); !emailDefaultRe.MatchString(e) {
		t.Fatalf("Email(\"\") = %q, want default-domain form", e)
	}
}

// TestEmailCustomDomain 守护 Email 的自定义域行为（B2）：传入非空 domain 时只替换域名、本地名仍为 fixture+载荷。
// 验证逻辑：Email("corp.example") 用正则断言匹配 fixture+[16 AlphaNum]@corp.example。
func TestEmailCustomDomain(t *testing.T) {
	scope := newScopeWith(t, &seqSource{})
	if e := scope.Email("corp.example"); !emailCustomRe.MatchString(e) {
		t.Fatalf("Email(\"corp.example\") = %q, want custom-domain form", e)
	}
}

// TestEmailPanicsOnMultipleDomains 守护 Email 的参数契约（B5）：传入多个 domain 属误用，应 panic。
// 验证逻辑：Email("a.test", "b.test") 调用，断言触发 panic（变参便利入口最多接受一个 domain）。
func TestEmailPanicsOnMultipleDomains(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Email with two domains did not panic")
		}
	}()
	newScopeWith(t, &seqSource{}).Email("a.test", "b.test")
}

// TestEmailLocalPartUnique 守护 Email 的唯一性（B1）：连续生成的邮箱两两不同（载荷来自全局唯一 token）。
// 验证逻辑：连生成 128 个 Email()，用 set 检测无重复——防"塑形阶段误把载荷写成常量"之类退化。
func TestEmailLocalPartUnique(t *testing.T) {
	scope := newScopeWith(t, &seqSource{})
	seen := make(map[string]bool, 128)
	for i := range 128 {
		e := scope.Email()
		if seen[e] {
			t.Fatalf("duplicate email %q at call %d", e, i)
		}
		seen[e] = true
	}
}

// pow10 是测试内的 int64 幂助手，避免依赖 fpe.go 的 *big.Int 版本做范围断言。
func pow10(digits int) int64 {
	v := int64(1)
	for range digits {
		v *= 10
	}
	return v
}
