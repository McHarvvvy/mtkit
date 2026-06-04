package protocol

import "encoding/json"

// Action kind 常量。control API 校验 kind 是否被支持，未支持返回 400 unsupported_action。
const (
	ActionKindStaticResponse   = "static_response"
	ActionKindSequenceResponse = "sequence_response"
	ActionKindOnceResponse     = "once_response"
	ActionKindError            = "error"
)

// OnExhausted 取值：sequence_response 序列耗尽后行为。
const (
	OnExhaustedRepeatLast = "repeat_last"
	OnExhaustedError      = "error"
)

// Action 是注册请求中 action 字段的通用形状：kind 决定 data 的结构。
// data 保留为 RawMessage，由 actions 包按 kind 解析为具体类型，
// 避免框架核心层引入 union 类型造成的耦合。
type Action struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// StaticResponseData 是 static_response 的 data 形状。
// Resp 是 protobuf marshal 后再 base64 的字节串，由测试侧 client 编码、
// 由 control API 校验解码 + 反序列化到目标 method 的 response 类型。
type StaticResponseData struct {
	Resp string `json:"resp"`
}

// SequenceResponseData 是 sequence_response 的 data 形状。
// Responses 按顺序消费，OnExhausted 取 OnExhaustedRepeatLast / OnExhaustedError。
//
// OnExhaustedErr 仅在 OnExhausted==OnExhaustedError 时有意义：指定耗尽后返回的
// gRPC error（code + message）。为空时回退到默认 FailedPrecondition + 固定文案，
// 保持与历史行为兼容。repeat_last 路径忽略该字段。
type SequenceResponseData struct {
	Responses      []StaticResponseData `json:"responses"`
	OnExhausted    string               `json:"on_exhausted"`
	OnExhaustedErr *ErrSpec             `json:"on_exhausted_err,omitempty"`
}

// OnceResponseData 是 once_response 的 data 形状。
// 首次命中返回 Resp；第二次起返回 OnSecondCall 指定的 gRPC error。
type OnceResponseData struct {
	Resp         string  `json:"resp"`
	OnSecondCall ErrSpec `json:"on_second_call"`
}

// ErrorData 是 error 的 data 形状：直接返回 gRPC error。
type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrSpec 是单条 gRPC error 的描述，被多个 action 复用。
// Code 取 codes.Code 字符串名（"OK" / "NotFound" / ...）。
type ErrSpec struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
