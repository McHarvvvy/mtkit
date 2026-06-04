package actions

import (
	"encoding/json"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/McHarvvvy/mtkit/faked/grpc/protocol"
)

// SequenceResponse 按命中次数依次返回 responses。
//
// 状态（已消费下标）按 rule 隔离：每条 rule 对应一个独立 SequenceResponse 实例。
// 序列耗尽后行为由 onExhausted 决定：repeat_last 或返回 FailedPrecondition。
type SequenceResponse struct {
	bytes       [][]byte
	onExhausted string

	// 仅 onExhausted==error 时有意义：耗尽后返回的 gRPC error。
	exhaustedCode codes.Code
	exhaustedMsg  string

	mu     sync.Mutex
	cursor int // 下一次要返回的下标
}

// Kind 返回 action kind 名称。
func (a *SequenceResponse) Kind() string { return protocol.ActionKindSequenceResponse }

// Execute 取下一个响应字节写入 resp 容器。
//
// 已消费到末尾时按 onExhausted：
//   - repeat_last：复用最后一份；
//   - error：返回 FailedPrecondition，提示序列耗尽。
func (a *SequenceResponse) Execute(resp proto.Message) error {
	if resp == nil {
		return protocol.NewError(protocol.ErrCodeInvalidResp, "nil response container")
	}
	a.mu.Lock()
	var raw []byte
	if a.cursor < len(a.bytes) {
		raw = a.bytes[a.cursor]
		a.cursor++
	} else {
		switch a.onExhausted {
		case protocol.OnExhaustedRepeatLast:
			raw = a.bytes[len(a.bytes)-1]
		case protocol.OnExhaustedError:
			a.mu.Unlock()
			return status.Error(a.exhaustedCode, a.exhaustedMsg)
		default:
			a.mu.Unlock()
			return status.Error(codes.FailedPrecondition, "sequence_response exhausted (unknown on_exhausted)")
		}
	}
	a.mu.Unlock()
	return proto.Unmarshal(raw, resp)
}

// NewSequenceResponse 解析 + 校验 sequence_response data，逐项 base64 + unmarshal 试解码。
func NewSequenceResponse(newResp func() proto.Message, data json.RawMessage) (*SequenceResponse, error) {
	var d protocol.SequenceResponseData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "unmarshal sequence_response data: %v", err)
	}
	if len(d.Responses) == 0 {
		return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "sequence_response with empty responses")
	}
	if d.OnExhausted == "" {
		return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "sequence_response missing on_exhausted")
	}
	if d.OnExhausted != protocol.OnExhaustedRepeatLast && d.OnExhausted != protocol.OnExhaustedError {
		return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "unknown on_exhausted %q", d.OnExhausted)
	}
	// 耗尽错误：默认 FailedPrecondition + 固定文案；data 指定 on_exhausted_err 时覆盖。
	// 仅 onExhausted==error 时消费该字段；repeat_last 下即便填了也无意义、不校验。
	exhaustedCode := codes.FailedPrecondition
	exhaustedMsg := "sequence_response exhausted"
	if d.OnExhausted == protocol.OnExhaustedError && d.OnExhaustedErr != nil {
		if d.OnExhaustedErr.Code == "" {
			return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "on_exhausted_err missing code")
		}
		code, perr := parseErrorCode(d.OnExhaustedErr.Code)
		if perr != nil {
			return nil, perr
		}
		exhaustedCode = code
		exhaustedMsg = d.OnExhaustedErr.Message
	}
	bytes := make([][]byte, 0, len(d.Responses))
	for i, r := range d.Responses {
		if r.Resp == "" {
			// 缺/空必填字段属 schema 不符 → invalid_action_data（与 static/once 一致）。
			return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "missing resp at index %d", i)
		}
		raw, err := decodeRespBase64(r.Resp)
		if err != nil {
			return nil, protocol.NewError(protocol.ErrCodeInvalidResp, "index %d: %v", i, err)
		}
		if err := validateResp(newResp, raw); err != nil {
			return nil, protocol.NewError(protocol.ErrCodeInvalidResp, "index %d: %v", i, err)
		}
		bytes = append(bytes, raw)
	}
	return &SequenceResponse{
		bytes:         bytes,
		onExhausted:   d.OnExhausted,
		exhaustedCode: exhaustedCode,
		exhaustedMsg:  exhaustedMsg,
	}, nil
}
