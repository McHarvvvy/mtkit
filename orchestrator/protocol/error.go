package protocol

const (
	// ErrCodeBadRequest 表示请求非法。
	ErrCodeBadRequest = "bad_request"
	// ErrCodeUnauthorized 表示鉴权失败。
	ErrCodeUnauthorized = "unauthorized"
	// ErrCodeNotFound 表示资源不存在。
	ErrCodeNotFound = "not_found"
	// ErrCodeInternal 表示内部错误。
	ErrCodeInternal = "internal"
)

// ErrorResp 表示协议错误返回。
type ErrorResp struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
