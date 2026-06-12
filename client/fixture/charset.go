package fixture

// Charset 标识 UniqueString 使用的字母表。
// size（字母表长度）充当编码基数，符号集决定输出字符；两个同 size 的字母表（如大小写 hex）是不同 charset。
type Charset int

const (
	AlphaNum Charset = iota // 62：数字 + 大小写字母
	Lower                   // 26：小写字母
	Upper                   // 26：大写字母
	Hex                     // 16：小写十六进制
	Digit                   // 10：十进制数字
)

// alphabet 返回 cs 对应的符号集；未定义的 charset 是程序员错误，直接 panic。
func alphabet(cs Charset) string {
	switch cs {
	case AlphaNum:
		return "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	case Lower:
		return "abcdefghijklmnopqrstuvwxyz"
	case Upper:
		return "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	case Hex:
		return "0123456789abcdef"
	case Digit:
		return "0123456789"
	default:
		panic("fixture: invalid charset")
	}
}
