package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"net/mail"
	"strconv"
	"strings"
	"testing"
	"time"
)

// CaseScope 是 per-test 的 fixture 生成作用域：绑定一个 testing.TB 与进程级 token 源，只暴露生成函数。
// 唯一性来自底层全局 token 序列，CaseScope 本身无状态，并发调用经 blockSource 的锁串行化，可安全跨 goroutine 使用。
type CaseScope struct {
	t   testing.TB
	src iTokenSource
}

// next 是所有生成函数的底层出口。
// 基建故障（daemon 不可达）通过 t.Fatalf 终止用例——因此生成函数签名无 error、无 per-call t（见 §1.7）。
func (s *CaseScope) next() int64 {
	s.t.Helper()
	v, err := s.src.Next()
	if err != nil {
		s.t.Fatalf("fixture: daemon unreachable: %v", err)
	}
	return v
}

// cleanup 注册到 t.Cleanup，当前 no-op，按需填充（如未来要在用例结束时回收资源）。
func (s *CaseScope) cleanup() {}

// UniqueString 返回恰好 n 个 cs 字符、全宽随机外观的唯一串。
// 对合法参数是 total 函数：不设运行期唯一性守卫、不 fail-loud；cap=|cs|^n ≥ 2⁶³ 时严格零碰撞，
// 较窄形态按 cap 折叠后会静默别名（调用方据 §1.2 三档表自行判断形态是否够大）。
func (s *CaseScope) UniqueString(n int, cs Charset) string {
	return uniqueStringFromToken(s.next(), n, cs)
}

// UniqueHex 返回恰好 n 个小写十六进制字符。
func (s *CaseScope) UniqueHex(n int) string {
	return s.UniqueString(n, Hex)
}

// UniqueInt 返回 [0, 10^digits) 内的非负整数。
// digits ∈ [1,18]：上界受 int64 可表达范围限制；需要 19 位及以上数字载荷请用 UniqueString(n, Digit)。
func (s *CaseScope) UniqueInt(digits int) int64 {
	if digits < 1 || digits > 18 {
		panic("fixture: UniqueInt digits must be in [1,18]")
	}
	// 复用 UniqueString(Digit) 的唯一载荷再解析：digits 位十进制串（可含前导零），值域严格 < 10^18 < 2⁶³。
	v, err := strconv.ParseInt(s.UniqueString(digits, Digit), 10, 64)
	if err != nil {
		panic("fixture: UniqueInt parse failed: " + err.Error())
	}
	return v
}

// UUID 返回带短线的 RFC 4122 v4 字符串（36 字符）。
func (s *CaseScope) UUID() string {
	id := s.UUIDNoDash()
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}

// UUIDNoDash 返回无短线的 RFC 4122 v4 字符串（32 字符）。
// 内部用 UniqueHex(30) 作可变载荷，再在 index 12 / 16 插入固定的 version(4) / variant(8) 位；
// 30 hex 载荷的值空间 2¹²⁰ ≫ 2⁶³，属 ① 档无条件唯一，插入固定位不影响唯一性。
func (s *CaseScope) UUIDNoDash() string {
	payload := s.UniqueHex(30)
	return payload[:12] + "4" + payload[12:15] + "8" + payload[15:]
}

// Email 返回语法合法的 fixture 邮箱：fixture+{16位载荷}@mtkit.test。
func (s *CaseScope) Email() string {
	return "fixture+" + s.UniqueString(16, AlphaNum) + "@mtkit.test"
}

// EmailFrom 在 seed 的 local part 后插入唯一 plus tag：{local}+{16位载荷}@{domain}。
// seed 须为不含显示名的合法邮箱，否则 panic。
func (s *CaseScope) EmailFrom(seed string) string {
	local, domain := parseEmailSeed(seed)
	return local + "+" + s.UniqueString(16, AlphaNum) + "@" + domain
}

// Phone 返回受支持国家的 E.164 形态号码。
// 第一版只保证 E.164 语法与国家码正确，不承诺号码符合各国真实可分配号段规则；country 不支持则 panic。
func (s *CaseScope) Phone(country string) string {
	spec, ok := phoneSpecs[strings.ToUpper(strings.TrimSpace(country))]
	if !ok {
		panic("fixture: unsupported phone country")
	}
	return "+" + spec.code + s.UniqueString(spec.nsnDigits, Digit)
}

// phoneSpec 描述一个国家的 E.164 拼装参数：国家码 + 国内号码位数。
type phoneSpec struct {
	code      string
	nsnDigits int
}

var phoneSpecs = map[string]phoneSpec{
	"CN": {code: "86", nsnDigits: 11},
	"US": {code: "1", nsnDigits: 10},
	"GB": {code: "44", nsnDigits: 10},
	"DE": {code: "49", nsnDigits: 10},
	"FR": {code: "33", nsnDigits: 9},
	"JP": {code: "81", nsnDigits: 10},
	"IT": {code: "39", nsnDigits: 10},
	"CA": {code: "1", nsnDigits: 10},
}

// parseEmailSeed 解析并校验 seed，返回 local 与 domain。
// 用 mail.ParseAddress + 三重校验拒绝带显示名 / 非裸地址的输入，确保塑形结果仍是合法邮箱。
func parseEmailSeed(seed string) (local, domain string) {
	trimmed := strings.TrimSpace(seed)
	addr, err := mail.ParseAddress(trimmed)
	if err != nil || addr.Name != "" || addr.Address != trimmed {
		panic("fixture: seed must be a plain valid email address")
	}
	at := strings.LastIndex(addr.Address, "@")
	if at <= 0 || at == len(addr.Address)-1 {
		panic("fixture: seed must be a plain valid email address")
	}
	return addr.Address[:at], addr.Address[at+1:]
}

// Sha256Hex 返回 s 的小写 SHA-256 十六进制摘要。确定性变换，不消耗 token。
func Sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// EpochSeconds 返回当前 Unix 秒。
func EpochSeconds() int64 {
	return time.Now().Unix()
}

// EpochMillis 返回当前 Unix 毫秒。
func EpochMillis() int64 {
	return time.Now().UnixMilli()
}

// 以下标准枚举样例用于“挑选非生成”：可重复、值可跨用例相同，不消耗 token。
var ISOCountryCodes = []string{"CN", "US", "GB", "DE", "FR", "JP", "IT", "CA", "AU", "BR", "IN", "KR", "SG", "NL", "SE", "ES"}
var ISOCurrencies = []string{"CNY", "USD", "GBP", "EUR", "JPY", "CAD", "AUD", "BRL", "INR", "KRW", "SGD", "CHF", "SEK", "NOK", "MXN"}
var IANATimeZones = []string{"Asia/Shanghai", "America/New_York", "Europe/London", "Europe/Berlin", "Europe/Paris", "Asia/Tokyo", "America/Los_Angeles", "UTC"}
var BCP47Languages = []string{"zh-CN", "en-US", "en-GB", "de-DE", "fr-FR", "ja-JP", "it-IT", "es-ES"}

// SampleCountry 随机返回一个 ISO 3166-1 alpha-2 国家码（可重复）。
func SampleCountry() string { return sample(ISOCountryCodes) }

// SampleCurrency 随机返回一个 ISO 4217 货币码（可重复）。
func SampleCurrency() string { return sample(ISOCurrencies) }

// SampleTimeZone 随机返回一个 IANA 时区名（可重复）。
func SampleTimeZone() string { return sample(IANATimeZones) }

// SampleLanguage 随机返回一个 BCP 47 语言标签（可重复）。
func SampleLanguage() string { return sample(BCP47Languages) }

// sample 从非空集合随机取一个元素；用全局 math/rand（Go 1.20+ 自动播种、并发安全），样例无需可复现。
func sample(values []string) string {
	if len(values) == 0 {
		panic("fixture: empty sample set")
	}
	return values[rand.Intn(len(values))]
}
