// Package actions 实现 gRPC 协议下的 Action 具体类型。
//
// 每个文件对应一种 action kind。本包不感知 store / engine / registry，
// 只接收已解码后的 protobuf 字节或 error spec，构造可被 ActionEngine 调用的 Action。
//
// 概念上 ActionEngine 与 store 看到的是 grpc.Action 接口；本包通过实现
// 同名签名的方法隐式满足该接口，从而无需反向 import 父包。
package actions

import (
	"encoding/base64"
	"encoding/json"

	"google.golang.org/protobuf/proto"

	"github.com/McHarvvvy/mtkit/faked/grpc/protocol"
)

// StaticResponse 每次命中返回同一份 protobuf response。
// 框架核心不解析业务字段：测试用例预先在注册时把 response 整体 marshal 成
// bytes；执行时只做 proto.Unmarshal 写回到薄壳提供的容器。
type StaticResponse struct {
	bytes []byte
}

// Kind 返回 action kind 名称。
func (a *StaticResponse) Kind() string { return protocol.ActionKindStaticResponse }

// Execute 把预存的 response bytes 写入薄壳提供的 resp 容器。
func (a *StaticResponse) Execute(resp proto.Message) error {
	if resp == nil {
		return protocol.NewError(protocol.ErrCodeInvalidResp, "nil response container")
	}
	return proto.Unmarshal(a.bytes, resp)
}

// NewStaticResponse 解析并校验 static_response 的 data 字段，构造可执行 action。
//
// newResp 来自 MethodSpec：用于"试解码"以确保注册的 bytes 能 unmarshal 到
// 目标 method 的 response 类型，提前发现协议不匹配。校验通过的 bytes 才被
// 持久化到 action，避免后续每次命中都重复试解码。
func NewStaticResponse(newResp func() proto.Message, data json.RawMessage) (*StaticResponse, error) {
	d, err := decodeStaticResponseData(data)
	if err != nil {
		return nil, err
	}
	raw, err := decodeRespBase64(d.Resp)
	if err != nil {
		return nil, err
	}
	if err := validateResp(newResp, raw); err != nil {
		return nil, err
	}
	return &StaticResponse{bytes: raw}, nil
}

func decodeStaticResponseData(data json.RawMessage) (protocol.StaticResponseData, error) {
	var d protocol.StaticResponseData
	if err := json.Unmarshal(data, &d); err != nil {
		return d, protocol.NewError(protocol.ErrCodeInvalidActionData, "unmarshal static_response data: %v", err)
	}
	if d.Resp == "" {
		// 缺/空必填字段属 schema 不符 → invalid_action_data；invalid_resp 仅留给
		// "resp 存在但 base64/unmarshal 失败"。
		return d, protocol.NewError(protocol.ErrCodeInvalidActionData, "static_response missing resp")
	}
	return d, nil
}

func decodeRespBase64(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, protocol.NewError(protocol.ErrCodeInvalidResp, "base64 decode: %v", err)
	}
	return raw, nil
}

// validateResp 验证 raw 字节可 unmarshal 到 newResp() 返回的目标 proto 类型。
// 仅做形状校验，结果丢弃；命中时仍以 raw 字节重新 unmarshal 到薄壳容器。
func validateResp(newResp func() proto.Message, raw []byte) error {
	if newResp == nil {
		return protocol.NewError(protocol.ErrCodeInvalidResp, "method spec has nil NewResponse")
	}
	probe := newResp()
	if probe == nil {
		return protocol.NewError(protocol.ErrCodeInvalidResp, "NewResponse returned nil")
	}
	if err := proto.Unmarshal(raw, probe); err != nil {
		return protocol.NewError(protocol.ErrCodeInvalidResp, "unmarshal probe: %v", err)
	}
	return nil
}
