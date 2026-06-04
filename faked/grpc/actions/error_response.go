package actions

import (
	"encoding/json"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/McHarvvvy/mtkit/faked/grpc/protocol"
)

// Error 命中后直接返回一个 gRPC error；响应容器被忽略。
type Error struct {
	code codes.Code
	msg  string
}

// Kind 返回 action kind 名称。
func (a *Error) Kind() string { return protocol.ActionKindError }

// Execute 返回 status.Error(code, message)；resp 容器忽略。
func (a *Error) Execute(_ proto.Message) error {
	return status.Error(a.code, a.msg)
}

// NewError 解析 error action 的 data 字段，构造 action。
// data.Code 取 codes.Code 字符串名（"OK" / "NotFound" / "Internal" / ...）。
func NewError(data json.RawMessage) (*Error, error) {
	var d protocol.ErrorData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "unmarshal error data: %v", err)
	}
	if d.Code == "" {
		return nil, protocol.NewError(protocol.ErrCodeInvalidActionData, "empty code")
	}
	code, perr := parseErrorCode(d.Code)
	if perr != nil {
		return nil, perr
	}
	return &Error{code: code, msg: d.Message}, nil
}

// parseErrorCode 解析"用作 gRPC error"的 code 字符串，在 parseCode 基础上额外拒绝 OK。
//
// status.Error(codes.OK, ...) 返回 nil——若放行，error / once 第二次 / sequence 耗尽这些
// 本应 fail-loud 的分支会静默成功、返回零值 response，违反"不静默返回空 response"(设计 §19)。
// 因此在注册阶段就拒绝，归 invalid_action_data。
func parseErrorCode(name string) (codes.Code, *protocol.Error) {
	code, ok := parseCode(name)
	if !ok {
		return 0, protocol.NewError(protocol.ErrCodeInvalidActionData, "unknown gRPC code %q", name)
	}
	if code == codes.OK {
		return 0, protocol.NewError(protocol.ErrCodeInvalidActionData, "gRPC code OK not allowed for error (would silently succeed)")
	}
	return code, nil
}

// parseCode 把 codes.Code 字符串名解析为枚举值。
//
// 兼容两种写法：
//   - SCREAMING_SNAKE_CASE（如 "ALREADY_EXISTS"）—— codes.Code.UnmarshalJSON 原生格式
//   - PascalCase（如 "AlreadyExists"）—— codes.Code.String() 原生格式，也是设计文档 §3.5
//     与 client.GRPCClient.Error 使用的格式
//
// 双格式接受避免文档与实现漂移；调用方任选其一即可。
func parseCode(name string) (codes.Code, bool) {
	var c codes.Code
	if err := c.UnmarshalJSON([]byte(`"` + name + `"`)); err == nil {
		return c, true
	}
	// 回退尝试 PascalCase：遍历所有合法 code 比对 String()。
	// 17 个值的线性扫描，注册阶段一次性开销可忽略。
	for code := codes.Code(0); code <= codes.Unauthenticated; code++ {
		if code.String() == name {
			return code, true
		}
	}
	return 0, false
}
