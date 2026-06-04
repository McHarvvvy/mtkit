package protocol

import "encoding/json"

// Action kind 常量。HTTP 协议下没有 error kind——所有错误响应都是带特定 status 的 static_response。
const (
	ActionKindStaticResponse   = "static_response"
	ActionKindSequenceResponse = "sequence_response"
	ActionKindOnceResponse     = "once_response"
)

// OnExhausted 取值：sequence_response 序列耗尽后行为。
const (
	OnExhaustedRepeatLast = "repeat_last"
	OnExhaustedError      = "error"
)

// Action 是注册请求中 action 字段的通用形状：kind 决定 data 的结构。
type Action struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// HTTPResponseSpec 是 HTTP 响应字面值，被多个 action 复用。
// BodyBase64 是 body 字节的 base64 编码：避免 JSON 中混入二进制和编码不一致。
type HTTPResponseSpec struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	BodyBase64 string            `json:"body_base64"`
}

// StaticResponseData 是 static_response 的 data 形状。
type StaticResponseData = HTTPResponseSpec

// SequenceResponseData 是 sequence_response 的 data 形状。
// Responses 按顺序消费，OnExhausted 取 OnExhaustedRepeatLast / OnExhaustedError。
//
// OnExhaustedResp 仅在 OnExhausted==OnExhaustedError 时有意义：指定耗尽后返回的
// 自定义响应（status + headers + body）。为空时回退到默认 502，保持与历史行为兼容。
// repeat_last 路径忽略该字段。与 gRPC 侧的 SequenceResponseData.OnExhaustedErr 对称。
type SequenceResponseData struct {
	Responses       []HTTPResponseSpec `json:"responses"`
	OnExhausted     string             `json:"on_exhausted"`
	OnExhaustedResp *HTTPResponseSpec  `json:"on_exhausted_resp,omitempty"`
}

// OnceResponseData 是 once_response 的 data 形状。
// 首次命中返回 Response；第二次起返回 OnSecondCall 指定的响应。
type OnceResponseData struct {
	Response     HTTPResponseSpec `json:"response"`
	OnSecondCall HTTPResponseSpec `json:"on_second_call"`
}
