// Package testendpoint 是 faked acceptance test 用的测试专用 fake endpoint。
//
// 与业务侧 test/fake/{grpc,http} 平行——但只服务于 faked 本身的测试，
// 不依赖 mssiot_user 业务 protobuf 或业务 HTTP 协议，确保 faked 框架
// 移植到其他项目时测试仍可独立跑通。
//
// endpoint 通过 init() 自注册到对应协议子包的 package-level 列表：
// 调用方 blank-import 本包即触发注册。
package testendpoint

import (
	"context"

	googrpc "google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	fakedgrpc "github.com/McHarvvvy/mtkit/faked/grpc"
	grpcroutekey "github.com/McHarvvvy/mtkit/faked/grpc/routekey"
	"github.com/McHarvvvy/mtkit/faked/test/internal/testpb"
)

// EndpointEcho 是测试用 gRPC endpoint 的标识。
// 不与任何业务 endpoint 重名（业务侧用 "forum" / "device" 等），避免误注册冲突。
const EndpointEcho = "echo"

// EnvKeyEcho 是 echo endpoint 在 runtime 地址 map 中的 key（测试命名空间，与业务 MSSIOT_IT_* 隔离）。
const EnvKeyEcho = "FAKED_TEST_ECHO_ADDR"

func init() {
	fakedgrpc.RegisterEndpoint(&echoEndpoint{})
}

type echoEndpoint struct{}

func (echoEndpoint) ID() string { return EndpointEcho }

func (echoEndpoint) EnvKey() string { return EnvKeyEcho }

func (echoEndpoint) Listen() string { return "127.0.0.1:0" }

func (echoEndpoint) Register(s *googrpc.Server, deps fakedgrpc.EndpointDeps) {
	testpb.RegisterEchoServer(s, &echoService{Engine: deps.Engine})
}

func (echoEndpoint) Methods() []fakedgrpc.MethodSpec {
	return []fakedgrpc.MethodSpec{
		{
			MethodIdentity: fakedgrpc.MethodIdentity{
				Endpoint:   EndpointEcho,
				FullMethod: testpb.Echo_Ping_FullMethodName,
			},
			NewResponse: func() proto.Message { return &testpb.PingRes{} },
		},
		{
			MethodIdentity: fakedgrpc.MethodIdentity{
				Endpoint:   EndpointEcho,
				FullMethod: testpb.Echo_Whoami_FullMethodName,
			},
			NewResponse: func() proto.Message { return &testpb.WhoamiRes{} },
		},
	}
}

// echoService 是 Echo proto service 的 typed 薄壳；薄壳逻辑严守"四步法"，
// 不写业务分支——业务行为由测试用例通过 control API 注册 rule 表达。
type echoService struct {
	testpb.UnimplementedEchoServer
	Engine *fakedgrpc.ActionEngine
}

// Ping route_key 取 PingReq.key。空值留给 ActionEngine 兜底报错——
// 这是 TC-04 grpc-empty-route-key 用例显式触发的分支。
func (s *echoService) Ping(ctx context.Context, req *testpb.PingReq) (*testpb.PingRes, error) {
	out := &testpb.PingRes{}
	if err := s.Engine.HandleUnary(ctx, fakedgrpc.Call{
		Endpoint:   EndpointEcho,
		FullMethod: testpb.Echo_Ping_FullMethodName,
		RouteKey:   req.GetKey(),
		Response:   out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// Whoami 无 request 字段，route_key 用 routekey.Static() 表达"全局静态" 语义。
func (s *echoService) Whoami(ctx context.Context, _ *testpb.WhoamiReq) (*testpb.WhoamiRes, error) {
	out := &testpb.WhoamiRes{}
	if err := s.Engine.HandleUnary(ctx, fakedgrpc.Call{
		Endpoint:   EndpointEcho,
		FullMethod: testpb.Echo_Whoami_FullMethodName,
		RouteKey:   grpcroutekey.Static(),
		Response:   out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}
