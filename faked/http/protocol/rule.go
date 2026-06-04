// Package protocol 定义 faked HTTP 控制面 / 调用日志 / 测试客户端共用的 DTO。
//
// 此包只包含纯数据结构，不依赖 net/http 实现细节，不持有运行时状态。
package protocol

// Rule 是 control API 注册请求与 store 内部展示共用的 HTTP rule DTO。
type Rule struct {
	Endpoint string `json:"endpoint"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	RouteKey string `json:"route_key"`
	Action   Action `json:"action"`
}

// Key 返回 rule 唯一性 key。
func (r Rule) Key() RuleKey {
	return RuleKey{Endpoint: r.Endpoint, Method: r.Method, Path: r.Path, RouteKey: r.RouteKey}
}

// RuleKey 是 store 内部唯一性 key；不参与 JSON 序列化。
type RuleKey struct {
	Endpoint string
	Method   string
	Path     string
	RouteKey string
}

// RuleID 返回展示 ID：endpoint|method|path|route_key；字段中的 '|' 会被转义。
func (k RuleKey) RuleID() string {
	return escapeRuleField(k.Endpoint) + "|" +
		escapeRuleField(k.Method) + "|" +
		escapeRuleField(k.Path) + "|" +
		escapeRuleField(k.RouteKey)
}

func escapeRuleField(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			out = append(out, '\\', '\\')
		case '|':
			out = append(out, '\\', '|')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
