package protocol

import "fmt"

// 错误码常量。设计文档 §6.4 列出全部 HTTP 协议错误码：
// 除通用之外有 invalid_route / invalid_status / invalid_body。
const (
	ErrCodeInvalidEndpoint   = "invalid_endpoint"
	ErrCodeInvalidRouteKey   = "invalid_route_key"
	ErrCodeUnsupportedAction = "unsupported_action"
	ErrCodeInvalidActionData = "invalid_action_data"
	ErrCodeDuplicateRule     = "duplicate_rule"
	ErrCodeInvalidRoute      = "invalid_route"
	ErrCodeInvalidStatus     = "invalid_status"
	ErrCodeInvalidBody       = "invalid_body"
)

// Error 是 actions / parser / control_http 共用的"分类错误"。
type Error struct {
	Code    string
	Message string
}

// Error 实现 error 接口。
func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

// NewError 构造分类错误。
func NewError(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
