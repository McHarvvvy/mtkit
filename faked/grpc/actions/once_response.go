package actions

import (
	"encoding/json"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/McHarvvvy/mtkit/faked/grpc/protocol"
)

// OnceResponse 只允许命中一次：首次返回预存 response，第二次起按 onSecondCall 返回 gRPC error。
//
// 状态（是否已消费）按 rule 隔离，sync.Mutex 保证并发安全。
type OnceResponse struct {
	bytes        []byte
	onSecondCode codes.Code
	onSecondMsg  string

	mu       sync.Mutex
	consumed bool
}

// Kind 返回 action kind 名称。
func (a *OnceResponse) Kind() string { return protocol.ActionKindOnceResponse }

// Execute 首次写入 resp，再次返回 onSecondCall 指定的 gRPC error。
func (a *OnceResponse) Execute(resp proto.Message) error {
	if resp == nil {
		return protocol.NewError(protocol.ErrCodeInvalidResp, "nil response container")
	}
	a.mu.Lock()
	if a.consumed {
		a.mu.Unlock()
		return status.Error(a.onSecondCode, a.onSecondMsg)
	}
	a.consumed = true
	a.mu.Unlock()
	return proto.Unmarshal(a.bytes, resp)
}

// NewOnceResponse 解析 + 校验 once_response data，构造 action。
func NewOnceResponse(newResp func() proto.Message, data json.RawMessage) (*OnceResponse, error) {
	var d protocol.OnceResponseData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "unmarshal once_response data: %v", err)
	}
	if d.Resp == "" {
		// 缺/空必填字段属 schema 不符 → invalid_action_data（与 static/sequence 一致）。
		return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "once_response missing resp")
	}
	raw, err := decodeRespBase64(d.Resp)
	if err != nil {
		return nil, err
	}
	if err := validateResp(newResp, raw); err != nil {
		return nil, err
	}
	if d.OnSecondCall.Code == "" {
		return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "once_response missing on_second_call.code")
	}
	code, perr := parseErrorCode(d.OnSecondCall.Code)
	if perr != nil {
		return nil, perr
	}
	return &OnceResponse{
		bytes:        raw,
		onSecondCode: code,
		onSecondMsg:  d.OnSecondCall.Message,
	}, nil
}
