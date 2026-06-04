package actions

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/McHarvvvy/mtkit/faked/http/protocol"
)

// OnceResponse 只允许命中一次：首次写入 response，第二次起写入 onSecondCall。
//
// 状态（是否已消费）按 rule 隔离，sync.Mutex 保证并发安全。
type OnceResponse struct {
	first  *StaticResponse
	second *StaticResponse

	mu       sync.Mutex
	consumed bool
}

// Kind 返回 action kind 名称。
func (a *OnceResponse) Kind() string { return protocol.ActionKindOnceResponse }

// Execute 首次写入 first，再次写入 second；都靠 StaticResponse.Execute 完成实际写。
func (a *OnceResponse) Execute(w http.ResponseWriter, r *http.Request) error {
	a.mu.Lock()
	use := a.first
	if a.consumed {
		use = a.second
	}
	a.consumed = true
	a.mu.Unlock()
	return use.Execute(w, r)
}

// NewOnceResponse 解析 + 校验 once_response data，构造 action。
func NewOnceResponse(data json.RawMessage) (*OnceResponse, error) {
	var d protocol.OnceResponseData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "unmarshal once_response data: %v", err)
	}
	first, err := buildStaticResponse(d.Response)
	if err != nil {
		return nil, wrapIndexed("response", err)
	}
	second, err := buildStaticResponse(d.OnSecondCall)
	if err != nil {
		return nil, wrapIndexed("on_second_call", err)
	}
	return &OnceResponse{first: first, second: second}, nil
}

// wrapIndexed 给字段定位前缀，便于注册阶段定位是哪一份 response 不合法。
func wrapIndexed(label string, err error) error {
	if apiErr, ok := err.(*protocol.Error); ok {
		return protocol.NewError(apiErr.Code, "%s: %s", label, apiErr.Message)
	}
	return protocol.NewError(protocol.ErrCodeInvalidActionData, "%s: %v", label, err)
}
