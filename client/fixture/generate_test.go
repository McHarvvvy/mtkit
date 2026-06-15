package fixture

import (
	"strings"
	"testing"
)

// TestUniqueStringCharsetOutputs 守护 L1 格式契约（B2）：UniqueString 对每个 charset 都产出
// "恰好 n 个字符，且每个字符都属于该 charset 的字母表"。
//
// 验证手段：对全部 5 个 charset 各生成一批值，逐字符核对其落在 alphabet(cs) 内、且长度恰为 n。
// 这覆盖了原先只有 Hex 经双射用例间接验证的盲区——若编码错用了别的字母表或长度算错，立即暴露。
// 用 newScopeWith + seqSource 注入确定 token，使断言只关心"格式塑形"而与具体 token 无关。
func TestUniqueStringCharsetOutputs(t *testing.T) {
	cases := []struct {
		name string
		cs   Charset
		n    int
	}{
		{"AlphaNum", AlphaNum, 12},
		{"Lower", Lower, 14},
		{"Upper", Upper, 14},
		{"Hex", Hex, 16},
		{"Digit", Digit, 18},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			allowed := make(map[rune]bool)
			for _, r := range alphabet(c.cs) {
				allowed[r] = true
			}
			scope := newScopeWith(t, &seqSource{})
			for range 100 {
				v := scope.UniqueString(c.n, c.cs)
				if len(v) != c.n {
					t.Fatalf("%s: 长度 = %d, want %d (%q)", c.name, len(v), c.n, v)
				}
				for _, r := range v {
					if !allowed[r] {
						t.Fatalf("%s: 字符 %q 不在字母表内 (%q)", c.name, r, v)
					}
				}
			}
		})
	}
}

// TestUUIDFollowsRFC4122V4 守护 UUID/UUIDNoDash 的 RFC 4122 v4 契约（B2），并刻意做到重构韧性。
//
// 验证手段：断言 version nibble == '4'、variant nibble ∈ {8,9,a,b}，其余位为合法 hex——这是 RFC 对
// v4 的实际要求。重要：不死咬当前实现固定写入的 variant '8'；RFC 允许 variant 的低位随机（8/9/a/b 皆合规），
// 若将来改成随机 variant 位，业务契约未变，本用例不应假挂。dashed 形态额外核对短线位置。
func TestUUIDFollowsRFC4122V4(t *testing.T) {
	scope := newScopeWith(t, &seqSource{})
	isHex := func(r byte) bool {
		return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
	}
	isVariant := func(r byte) bool { return r == '8' || r == '9' || r == 'a' || r == 'b' }

	for range 256 {
		// 无短线形态：32 hex，version 在 index 12，variant 在 index 16。
		nd := scope.UUIDNoDash()
		if len(nd) != 32 {
			t.Fatalf("UUIDNoDash 长度 = %d, want 32 (%q)", len(nd), nd)
		}
		for i := range 32 {
			if i == 12 {
				if nd[i] != '4' {
					t.Fatalf("UUIDNoDash version nibble = %q, want '4' (%q)", nd[i], nd)
				}
				continue
			}
			if i == 16 {
				if !isVariant(nd[i]) {
					t.Fatalf("UUIDNoDash variant nibble = %q, want 8/9/a/b (%q)", nd[i], nd)
				}
				continue
			}
			if !isHex(nd[i]) {
				t.Fatalf("UUIDNoDash 非 hex 字符 %q at %d (%q)", nd[i], i, nd)
			}
		}

		// 带短线形态：8-4-4-4-12，version 在 index 14，variant 在 index 19。
		d := scope.UUID()
		if len(d) != 36 || d[8] != '-' || d[13] != '-' || d[18] != '-' || d[23] != '-' {
			t.Fatalf("UUID 短线布局错误 (%q)", d)
		}
		if d[14] != '4' {
			t.Fatalf("UUID version nibble = %q, want '4' (%q)", d[14], d)
		}
		if !isVariant(d[19]) {
			t.Fatalf("UUID variant nibble = %q, want 8/9/a/b (%q)", d[19], d)
		}
	}
}

// TestPhoneAllSupportedCountries 守护"全部受支持国家都能产出语法合法 E.164 号码"契约（B2）。
//
// 验证手段：遍历受支持国家集合，对每个国家断言输出形如 "+{国家码}{国内号}"、总位数 = 国家码位数
// + 国内号位数、且去掉 '+' 后全为数字。覆盖了原先只测 US 单国的盲区——任一国家配置写错（国家码/位数）
// 会被对应子用例抓住。这里只验 E.164 语法，不承诺真实号段可分配（与 spec 一致）。
func TestPhoneAllSupportedCountries(t *testing.T) {
	scope := newScopeWith(t, &seqSource{})
	for country, spec := range phoneSpecs {
		t.Run(country, func(t *testing.T) {
			p := scope.Phone(country)
			if !strings.HasPrefix(p, "+"+spec.code) {
				t.Fatalf("Phone(%q) = %q, want prefix +%s", country, p, spec.code)
			}
			digits := p[1:] // 去掉 '+'
			if len(digits) != len(spec.code)+spec.nsnDigits {
				t.Fatalf("Phone(%q) = %q, 位数 = %d, want %d", country, p, len(digits), len(spec.code)+spec.nsnDigits)
			}
			for _, r := range digits {
				if r < '0' || r > '9' {
					t.Fatalf("Phone(%q) = %q 含非数字 %q", country, p, r)
				}
			}
		})
	}
}

// TestGeneratorMisusePanics 守护"调用方误用属程序员错误、应 fail-fast panic"契约（B5）。
//
// 验证手段：对每类非法用法断言会 panic。这些是合法参数契约之外的输入（spec §1.7 明列），让误用在
// 测试期当场炸出，而非静默产出坏值。UniqueInt 越界、Email 多 domain 已在 scope_test.go 覆盖，此处补全其余。
func TestGeneratorMisusePanics(t *testing.T) {
	cases := []struct {
		name string
		call func(*CaseScope)
	}{
		{"n 非正", func(s *CaseScope) { s.UniqueString(0, Hex) }},
		{"未定义 charset", func(s *CaseScope) { s.UniqueString(8, Charset(99)) }},
		{"EmailFrom 非法 seed", func(s *CaseScope) { s.EmailFrom("not-an-email") }},
		{"Phone 不支持的国家", func(s *CaseScope) { s.Phone("ZZ") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s: 期望 panic，但没有", c.name)
				}
			}()
			c.call(newScopeWith(t, &seqSource{}))
		})
	}
}
