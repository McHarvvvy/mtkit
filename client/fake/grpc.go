package fake

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	grpcproto "github.com/McHarvvvy/mtkit/faked/grpc/protocol"
)

// gRPCRulesPath 是 gRPC 协议 rule 注册的 control API 路径，与服务端 control_api.go 对齐。
const gRPCRulesPath = "/v1/grpc/rules"

// grpcClient 在 control 基础之上固化 gRPC rule 注册路径。
type grpcClient struct {
	ctrl *control
}

func newGRPC(ctrl *control) *grpcClient { return &grpcClient{ctrl: ctrl} }

// On 开始声明一条 gRPC rule 的 route 坐标。
func (c *grpcClient) On(endpoint, fullMethod string) *grpcRouteBuilder {
	return &grpcRouteBuilder{client: c, endpoint: endpoint, fullMethod: fullMethod}
}

// RegisterRule 提交一条原始 gRPC rule DTO；底层 transport 直通。
func (c *grpcClient) RegisterRule(ctx context.Context, rule grpcproto.Rule) error {
	return c.ctrl.post(ctx, gRPCRulesPath, rule, nil)
}

type grpcRouteBuilder struct {
	client     *grpcClient
	endpoint   string
	fullMethod string
}

// RouteKey 填入本 rule 的 route_key，并进入 action 选择阶段。
func (b *grpcRouteBuilder) RouteKey(routeKey string) *grpcRuleBuilder {
	return &grpcRuleBuilder{
		client:     b.client,
		endpoint:   b.endpoint,
		fullMethod: b.fullMethod,
		routeKey:   routeKey,
	}
}

type grpcRuleBuilder struct {
	client     *grpcClient
	endpoint   string
	fullMethod string
	routeKey   string
}

// Static 注册 static_response：每次命中都返回同一份 protobuf response。
func (b *grpcRuleBuilder) Static(resp proto.Message) *grpcStaticBuilder {
	data, err := encodeStaticResponseData(resp)
	return &grpcStaticBuilder{
		base: b,
		data: data,
		err:  wrapErr("encode static_response", err),
	}
}

// Sequence 注册 sequence_response：每次 Add 追加一份按顺序返回的 response。
func (b *grpcRuleBuilder) Sequence() *grpcSequenceBuilder {
	return &grpcSequenceBuilder{base: b}
}

// Once 注册 once_response：首次返回 resp，第二次起按 ThenError 指定的 gRPC error 返回。
func (b *grpcRuleBuilder) Once(resp proto.Message) *grpcOnceBuilder {
	data, err := marshalAndEncode(resp)
	return &grpcOnceBuilder{
		base: b,
		resp: data,
		err:  wrapErr("encode once_response", err),
	}
}

// Error 注册 error action：命中后直接返回 gRPC status error。
func (b *grpcRuleBuilder) Error(code codes.Code, msg string) *grpcErrorBuilder {
	data, err := json.Marshal(grpcproto.ErrorData{Code: code.String(), Message: msg})
	return &grpcErrorBuilder{
		base: b,
		data: data,
		err:  wrapErr("encode error data", err),
	}
}

type grpcStaticBuilder struct {
	base *grpcRuleBuilder
	data json.RawMessage
	err  error
}

// Apply 提交 static_response rule；失败时直接 t.Fatal。
func (b *grpcStaticBuilder) Apply(t testing.TB) {
	t.Helper()
	if b.err != nil {
		t.Fatalf("faked client: %v", b.err)
	}
	rule := b.base.rule(grpcproto.ActionKindStaticResponse, b.data)
	if err := b.base.client.RegisterRule(helperCtx(), rule); err != nil {
		t.Fatalf("faked client: register static_response: %v", err)
	}
}

type grpcSequenceBuilder struct {
	base           *grpcRuleBuilder
	responses      []grpcproto.StaticResponseData
	onExhausted    string
	onExhaustedErr *grpcproto.ErrSpec
	err            error
}

// Add 追加一份 sequence_response 响应。
func (b *grpcSequenceBuilder) Add(resp proto.Message) *grpcSequenceBuilder {
	encoded, err := marshalAndEncode(resp)
	if err != nil {
		b.addErr(wrapErr("encode sequence_response", err))
		return b
	}
	b.responses = append(b.responses, grpcproto.StaticResponseData{Resp: &encoded})
	return b
}

// RepeatLastOnExhausted 设置序列耗尽后重复最后一份 response。
func (b *grpcSequenceBuilder) RepeatLastOnExhausted() *grpcSequenceBuilder {
	b.onExhausted = grpcproto.OnExhaustedRepeatLast
	return b
}

// ErrorOnExhausted 设置序列耗尽后返回指定的 gRPC error（code + message）。
// 与 Once().ThenError 对称——耗尽错误不再固定为 FailedPrecondition。
func (b *grpcSequenceBuilder) ErrorOnExhausted(code codes.Code, msg string) *grpcSequenceBuilder {
	b.onExhausted = grpcproto.OnExhaustedError
	b.onExhaustedErr = &grpcproto.ErrSpec{Code: code.String(), Message: msg}
	return b
}

// Apply 提交 sequence_response rule；失败时直接 t.Fatal。
func (b *grpcSequenceBuilder) Apply(t testing.TB) {
	t.Helper()
	if b.err != nil {
		t.Fatalf("faked client: %v", b.err)
	}
	if len(b.responses) == 0 {
		t.Fatalf("faked client: sequence_response needs at least 1 response")
	}
	if b.onExhausted == "" {
		t.Fatalf("faked client: sequence_response needs on_exhausted; call RepeatLastOnExhausted or ErrorOnExhausted")
	}
	data, err := json.Marshal(grpcproto.SequenceResponseData{
		Responses:      b.responses,
		OnExhausted:    b.onExhausted,
		OnExhaustedErr: b.onExhaustedErr,
	})
	if err != nil {
		t.Fatalf("faked client: encode sequence_response: %v", err)
	}
	rule := b.base.rule(grpcproto.ActionKindSequenceResponse, data)
	if err := b.base.client.RegisterRule(helperCtx(), rule); err != nil {
		t.Fatalf("faked client: register sequence_response: %v", err)
	}
}

func (b *grpcSequenceBuilder) addErr(err error) {
	if err != nil && b.err == nil {
		b.err = err
	}
}

type grpcOnceBuilder struct {
	base             *grpcRuleBuilder
	resp             string
	onSecondCall     grpcproto.ErrSpec
	onSecondCallDone bool
	err              error
}

// ThenError 设置 once_response 在第二次及后续调用时返回的 gRPC error。
func (b *grpcOnceBuilder) ThenError(code codes.Code, msg string) *grpcOnceBuilder {
	b.onSecondCall = grpcproto.ErrSpec{Code: code.String(), Message: msg}
	b.onSecondCallDone = true
	return b
}

// Apply 提交 once_response rule；失败时直接 t.Fatal。
func (b *grpcOnceBuilder) Apply(t testing.TB) {
	t.Helper()
	if b.err != nil {
		t.Fatalf("faked client: %v", b.err)
	}
	if !b.onSecondCallDone {
		t.Fatalf("faked client: once_response needs second-call behavior; call ThenError")
	}
	data, err := json.Marshal(grpcproto.OnceResponseData{
		Resp:         &b.resp,
		OnSecondCall: b.onSecondCall,
	})
	if err != nil {
		t.Fatalf("faked client: encode once_response: %v", err)
	}
	rule := b.base.rule(grpcproto.ActionKindOnceResponse, data)
	if err := b.base.client.RegisterRule(helperCtx(), rule); err != nil {
		t.Fatalf("faked client: register once_response: %v", err)
	}
}

type grpcErrorBuilder struct {
	base *grpcRuleBuilder
	data json.RawMessage
	err  error
}

// Apply 提交 error rule；失败时直接 t.Fatal。
func (b *grpcErrorBuilder) Apply(t testing.TB) {
	t.Helper()
	if b.err != nil {
		t.Fatalf("faked client: %v", b.err)
	}
	rule := b.base.rule(grpcproto.ActionKindError, b.data)
	if err := b.base.client.RegisterRule(helperCtx(), rule); err != nil {
		t.Fatalf("faked client: register error: %v", err)
	}
}

func (b *grpcRuleBuilder) rule(kind string, data json.RawMessage) grpcproto.Rule {
	return grpcproto.Rule{
		Endpoint:   b.endpoint,
		FullMethod: b.fullMethod,
		RouteKey:   b.routeKey,
		Action: grpcproto.Action{
			Kind: kind,
			Data: data,
		},
	}
}

// encodeStaticResponseData 把 proto.Message marshal + base64 后封到 protocol.StaticResponseData。
func encodeStaticResponseData(resp proto.Message) (json.RawMessage, error) {
	encoded, err := marshalAndEncode(resp)
	if err != nil {
		return nil, err
	}
	return json.Marshal(grpcproto.StaticResponseData{Resp: &encoded})
}

// marshalAndEncode 是 proto.Marshal + base64.Std 的合并：测试侧统一编码入口，
// 避免每个 helper 各自实现一遍。
//
// nil response 是客户端构造期错误，返回普通 error；不复用 *APIError——后者
// 专门表达 control API 返回 ok=false 的结构化错误，借用会让 errors.As 出来的
// APIError.HTTPStatus=0 误导调用方。
func marshalAndEncode(resp proto.Message) (string, error) {
	if resp == nil {
		return "", errors.New("nil response")
	}
	raw, err := proto.Marshal(resp)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func wrapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}
