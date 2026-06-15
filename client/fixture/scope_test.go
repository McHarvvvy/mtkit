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

func TestEmailCustomDomain(t *testing.T) {
	scope := newScopeWith(t, &seqSource{})
	if e := scope.Email("corp.example"); !emailCustomRe.MatchString(e) {
		t.Fatalf("Email(\"corp.example\") = %q, want custom-domain form", e)
	}
}

func TestEmailPanicsOnMultipleDomains(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Email with two domains did not panic")
		}
	}()
	newScopeWith(t, &seqSource{}).Email("a.test", "b.test")
}

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
