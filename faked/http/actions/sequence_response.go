package actions

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/McHarvvvy/mtkit/faked/http/protocol"
)

// SequenceResponse 按命中次数依次写入 responses[i] 的 status / headers / body。
//
// 状态（cursor）按 rule 隔离；序列耗尽行为由 onExhausted 决定：
//   - repeat_last：复用最后一份；
//   - error：HTTP 协议下没有 gRPC 的 FailedPrecondition，统一回 502 + 错误消息，
//     与 ActionEngine 在路由未命中时的语义一致——测试侧据此排查。
type SequenceResponse struct {
	items       []*StaticResponse
	onExhausted string

	// 仅 onExhausted==error 时有意义：耗尽后返回的自定义响应。
	// nil 表示用默认 502（与历史行为兼容）。
	exhaustedResp *StaticResponse

	mu     sync.Mutex
	cursor int
}

// Kind 返回 action kind 名称。
func (a *SequenceResponse) Kind() string { return protocol.ActionKindSequenceResponse }

// Execute 取下一个 item 写入 ResponseWriter。
func (a *SequenceResponse) Execute(w http.ResponseWriter, r *http.Request) error {
	var item *StaticResponse
	a.mu.Lock()
	switch {
	case a.cursor < len(a.items):
		item = a.items[a.cursor]
		a.cursor++
	case a.onExhausted == protocol.OnExhaustedRepeatLast:
		item = a.items[len(a.items)-1]
	case a.exhaustedResp != nil:
		// onExhausted==error 且注册了自定义响应：返回它（status/headers/body 由调用方指定）。
		exhausted := a.exhaustedResp
		a.mu.Unlock()
		return exhausted.Execute(w, r)
	default:
		// onExhausted==error 且未自定义：回退默认 502 fail-loud（与路由未命中语义一致）。
		a.mu.Unlock()
		http.Error(w, "sequence_response exhausted", http.StatusBadGateway)
		return nil
	}
	a.mu.Unlock()
	return item.Execute(w, r)
}

// NewSequenceResponse 解析 + 校验 sequence_response data，构造 action。
func NewSequenceResponse(data json.RawMessage) (*SequenceResponse, error) {
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
	items := make([]*StaticResponse, 0, len(d.Responses))
	for i, r := range d.Responses {
		item, err := buildStaticResponse(r)
		if err != nil {
			if apiErr, ok := err.(*protocol.Error); ok {
				return nil, protocol.NewError(apiErr.Code, "index %d: %s", i, apiErr.Message)
			}
			return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "index %d: %v", i, err)
		}
		items = append(items, item)
	}

	// 自定义耗尽响应：仅 error 模式消费、校验（status/body 非法回 invalid_status/invalid_body）。
	var exhaustedResp *StaticResponse
	if d.OnExhausted == protocol.OnExhaustedError && d.OnExhaustedResp != nil {
		resp, err := buildStaticResponse(*d.OnExhaustedResp)
		if err != nil {
			if apiErr, ok := err.(*protocol.Error); ok {
				return nil, protocol.NewError(apiErr.Code, "on_exhausted_resp: %s", apiErr.Message)
			}
			return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "on_exhausted_resp: %v", err)
		}
		exhaustedResp = resp
	}

	return &SequenceResponse{items: items, onExhausted: d.OnExhausted, exhaustedResp: exhaustedResp}, nil
}
