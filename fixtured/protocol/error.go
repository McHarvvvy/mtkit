package protocol

const (
	// ErrCodeInvalidKind 表示请求的发号 kind 不受支持。
	ErrCodeInvalidKind = "invalid_kind"
	// ErrCodeInvalidSize 表示请求的区块大小非法（非正或超出上界）。
	ErrCodeInvalidSize = "invalid_size"
	// ErrCodeBadRequest 表示 HTTP 请求格式错误。
	ErrCodeBadRequest = "bad_request"
	// ErrCodeInternal 表示服务端内部错误。
	ErrCodeInternal = "internal"
)

// ErrorResp 是 fixture 控制面错误返回。
// 字段刻意与 daemon lease 错误返回（orchestrator/protocol.ErrorResp 的 code/message）保持一致：
// 客户端因此能用同一结构解析 fixture handler 与 daemon withAuth（鉴权失败）两类错误，不丢结构化 code。
type ErrorResp struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
