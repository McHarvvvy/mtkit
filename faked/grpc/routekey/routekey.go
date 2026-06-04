// Package routekey 提供 gRPC 协议层构造 route_key 的常用 helper。
//
// gRPC typed 薄壳在收到 typed request 后调用本包函数得到 route_key 字符串，
// 再交给 ActionEngine 查 rule。空值检查由本包做"严"，engine 再做兜底。
package routekey

import (
	"errors"
	"net/url"
	"sort"
	"strings"
)

// ErrEmptyValue 表示 Single 收到空字符串。route_key 必须有内容，否则 rule 必然不命中。
var ErrEmptyValue = errors.New("routekey: empty value")

// Single 直接以单一字段值作为 route_key。空字符串返回错误。
func Single(value string) (string, error) {
	if value == "" {
		return "", ErrEmptyValue
	}
	return value, nil
}

// Static 返回固定 route_key（"_"），用于 method 全局静态数据场景。
// 不返回错误：内容由本包决定，永远非空。
func Static() string { return "_" }

// Composite 把多个字段按字段名字母序拼接为 "k1=v1&k2=v2" 形式。
// 字段名与值都做 query escaping；空值字段也参与拼接，便于稳定生成可比对 key。
func Composite(fields map[string]string) string {
	if len(fields) == 0 {
		return ""
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(fields[k]))
	}
	return b.String()
}
