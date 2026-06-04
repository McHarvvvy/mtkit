// Package actions 实现 HTTP 协议下的 Action 具体类型。
//
// 本包不感知 store / engine / registry，只接收已解码的 HTTPResponseSpec，
// 通过实现父包 Action 接口要求的同名方法签名隐式满足接口。
package actions

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/McHarvvvy/mtkit/faked/http/protocol"
)

// StaticResponse 每次命中按预存的 HTTPResponseSpec 写一份响应。
type StaticResponse struct {
	status  int
	headers http.Header
	body    []byte
}

// Kind 返回 action kind 名称。
func (a *StaticResponse) Kind() string { return protocol.ActionKindStaticResponse }

// Execute 把预存的 status / headers / body 写入 ResponseWriter；忽略请求体。
func (a *StaticResponse) Execute(w http.ResponseWriter, _ *http.Request) error {
	wh := w.Header()
	for k, vs := range a.headers {
		for _, v := range vs {
			wh.Add(k, v)
		}
	}
	w.WriteHeader(a.status)
	if len(a.body) == 0 {
		return nil
	}
	_, err := w.Write(a.body)
	return err
}

// NewStaticResponse 解析并校验 static_response data，构造可执行 action。
func NewStaticResponse(data json.RawMessage) (*StaticResponse, error) {
	var d protocol.StaticResponseData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "unmarshal static_response data: %v", err)
	}
	return buildStaticResponse(d)
}

// buildStaticResponse 把 HTTPResponseSpec 转成可执行的 *StaticResponse，
// 同时校验 status / body_base64。提取成函数便于 sequence / once 复用。
func buildStaticResponse(d protocol.HTTPResponseSpec) (*StaticResponse, error) {
	if d.Status < 100 || d.Status > 599 {
		return nil, protocol.NewError(protocol.ErrCodeInvalidStatus, "%d (want [100,599])", d.Status)
	}
	body, err := decodeBodyBase64(d.BodyBase64)
	if err != nil {
		return nil, err
	}
	return &StaticResponse{
		status:  d.Status,
		headers: toHTTPHeader(d.Headers),
		body:    body,
	}, nil
}

func decodeBodyBase64(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, protocol.NewError(protocol.ErrCodeInvalidBody, "base64 decode: %v", err)
	}
	return raw, nil
}

// toHTTPHeader 把 string→string map 转成 http.Header（string→[]string）。
// 设计文档明确 headers 是 string→string，不支持同名多值。
func toHTTPHeader(m map[string]string) http.Header {
	if len(m) == 0 {
		return nil
	}
	h := make(http.Header, len(m))
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}
