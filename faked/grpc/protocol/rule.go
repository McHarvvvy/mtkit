// Package protocol 定义 faked gRPC 控制面 / 调用日志 / 测试客户端共用的 DTO。
//
// 此包只包含纯数据结构，不依赖任何 pb 包，不持有运行时状态。
// 服务端 store / engine / actions 与测试侧 client 均通过本包对齐 wire 协议。
package protocol

// Rule 是 control API 注册请求与 store 内部展示共用的 gRPC rule DTO。
type Rule struct {
	Endpoint   string `json:"endpoint"`
	FullMethod string `json:"full_method"`
	RouteKey   string `json:"route_key"`
	Action     Action `json:"action"`
}

// Key 返回 rule 唯一性 key。
func (r Rule) Key() RuleKey {
	return RuleKey{Endpoint: r.Endpoint, FullMethod: r.FullMethod, RouteKey: r.RouteKey}
}

// RuleKey 是 store 内部的唯一性 key；不参与 JSON 序列化。
// store 用结构化 key 而非字符串拼接做唯一性判断，避免分隔符冲突。
type RuleKey struct {
	Endpoint   string
	FullMethod string
	RouteKey   string
}

// RuleID 返回展示 ID，用于日志与错误信息：endpoint|full_method|route_key。
// 字段中的 '|' 会被转义为 '\|'，避免歧义；内部唯一性仍以结构化 key 为准。
func (k RuleKey) RuleID() string {
	return escapeRuleField(k.Endpoint) + "|" +
		escapeRuleField(k.FullMethod) + "|" +
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
