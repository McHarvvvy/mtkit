package protocol

import "fmt"

// 错误码常量。control API 在响应里回写 code，测试侧可据此判错。
//
// 设计文档 §6.4 列出全部 gRPC 协议错误码：除通用之外有 invalid_method / invalid_resp。
const (
	ErrCodeInvalidEndpoint   = "invalid_endpoint"
	ErrCodeInvalidRouteKey   = "invalid_route_key"
	ErrCodeUnsupportedAction = "unsupported_action"
	ErrCodeInvalidActionData = "invalid_action_data"
	ErrCodeDuplicateRule     = "duplicate_rule"
	ErrCodeInvalidMethod     = "invalid_method"
	ErrCodeInvalidResp       = "invalid_resp"
)

// Error 是 actions / parser / control_http 共用的"分类错误"。
//
// Code 对应 control API 响应体里的 error 字段；Message 是详细信息。
// 通过 errors.As 在 control_http 中提取 code 与 message。
type Error struct {
	Code    string
	Message string
}

// Error 实现 error 接口。
func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

// NewError 构造分类错误；Message 用 fmt 风格格式化。
func NewError(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
