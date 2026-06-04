// Package routekey 提供 HTTP 协议层构造 route_key 的常用 helper。
//
// HTTP Endpoint.Route.RouteKey 是一个 func(*http.Request) (string, error)，
// 本包函数命名遵循 Query / PathValue / Header / FormValue / JSONField 等
// 取值来源，Endpoint 作者按需直接组合或封装。
package routekey

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// ErrEmptyValue 表示提取到的 route_key 字段为空。
var ErrEmptyValue = errors.New("routekey: empty value")

// Single 与 gRPC 同名 helper 对应：把一个已知字符串包装为 route_key，空值报错。
// 通常用于 RouteKey 内部对 query / form 等已取到的值做"非空"二次校验。
func Single(value string) (string, error) {
	if value == "" {
		return "", ErrEmptyValue
	}
	return value, nil
}

// Static 返回固定 route_key（"_"）。无错。
func Static() string { return "_" }

// Composite 把字段集合稳定拼接为 "k1=v1&k2=v2"。详见 grpc/routekey 文档。
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

// Query 取 URL query 参数；空值返回错误。
func Query(r *http.Request, name string) (string, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return "", fmt.Errorf("routekey: empty query %q", name)
	}
	return v, nil
}

// PathValue 取 ServeMux 1.22+ {name} 占位符的值；空值返回错误。
func PathValue(r *http.Request, name string) (string, error) {
	v := r.PathValue(name)
	if v == "" {
		return "", fmt.Errorf("routekey: empty path value %q", name)
	}
	return v, nil
}

// Header 取请求 header；空值返回错误。
func Header(r *http.Request, name string) (string, error) {
	v := r.Header.Get(name)
	if v == "" {
		return "", fmt.Errorf("routekey: empty header %q", name)
	}
	return v, nil
}

// FormValue 解析 application/x-www-form-urlencoded body 后取字段。
//
// 内部调用 r.ParseForm()：会消费 body。若同一处理链后续仍需读 body，应
// 选 JSONField 等显式重置 body 的 helper，或在 Route binding 中自己处理。
func FormValue(r *http.Request, name string) (string, error) {
	if err := r.ParseForm(); err != nil {
		return "", fmt.Errorf("routekey: parse form: %w", err)
	}
	v := r.PostForm.Get(name)
	if v == "" {
		v = r.Form.Get(name)
	}
	if v == "" {
		return "", fmt.Errorf("routekey: empty form value %q", name)
	}
	return v, nil
}

// JSONField 读取整 body、parse JSON、取顶层字段，并重置 r.Body 让后续 handler 仍能再读一次。
// jsonPath 支持点分多级路径，例如 "user.id"；目前仅支持 object → object → ... → string|number。
func JSONField(r *http.Request, jsonPath string) (string, error) {
	if r.Body == nil {
		return "", errors.New("routekey: nil request body")
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return "", fmt.Errorf("routekey: read body: %w", err)
	}
	// 读后立即重置 body，让后续 handler / 调用日志仍能再读一次。
	r.Body = io.NopCloser(bytes.NewReader(raw))

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("routekey: parse json: %w", err)
	}
	v, err := walkJSONPath(doc, jsonPath)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", fmt.Errorf("routekey: empty json field %q", jsonPath)
	}
	return v, nil
}

// walkJSONPath 沿点分路径遍历 JSON map，叶子值要求是字符串或数字。
func walkJSONPath(doc map[string]any, jsonPath string) (string, error) {
	parts := strings.Split(jsonPath, ".")
	var cur any = doc
	for i, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", fmt.Errorf("routekey: json field %q: %s is not an object",
				jsonPath, strings.Join(parts[:i], "."))
		}
		next, present := m[p]
		if !present {
			return "", fmt.Errorf("routekey: json field %q not found", jsonPath)
		}
		cur = next
	}
	switch v := cur.(type) {
	case string:
		return v, nil
	case float64:
		// JSON number unmarshal 到 any 默认是 float64；按整数还是浮点输出由测试约定。
		// 此处用 %v 输出原始字面值近似。
		return fmt.Sprintf("%v", v), nil
	case bool:
		return fmt.Sprintf("%v", v), nil
	default:
		return "", fmt.Errorf("routekey: json field %q has unsupported type %T", jsonPath, cur)
	}
}
