// Package echoendpoint 为 e2e 自带一份 echo fake endpoint（gRPC + HTTP），经 init()
// 自注册到 faked 引擎。e2e 测试二进制 blank-import 本包即触发注册；与 faked acceptance
// 的 testendpoint 平行但独立（各自独立 test 二进制，互不共链）。
package echoendpoint

import (
	"context"

	googrpc "google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	fakedgrpc "github.com/McHarvvvy/mtkit/faked/grpc"
	"github.com/McHarvvvy/mtkit/test/e2e/internal/echopb"
)

// EndpointEcho 是 e2e gRPC endpoint 标识。
const EndpointEcho = "echo"

func init() {
	fakedgrpc.RegisterEndpoint(&echoEndpoint{})
}

type echoEndpoint struct{}

func (echoEndpoint) ID() string     { return EndpointEcho }
func (echoEndpoint) EnvKey() string { return echopb.EnvKeyEchoGRPC }
func (echoEndpoint) Listen() string { return "127.0.0.1:0" }

func (echoEndpoint) Register(s *googrpc.Server, deps fakedgrpc.EndpointDeps) {
	echopb.RegisterEchoServer(s, &echoService{Engine: deps.Engine})
}

func (echoEndpoint) Methods() []fakedgrpc.MethodSpec {
	return []fakedgrpc.MethodSpec{
		{
			MethodIdentity: fakedgrpc.MethodIdentity{
				Endpoint:   EndpointEcho,
				FullMethod: echopb.Echo_Ping_FullMethodName,
			},
			NewResponse: func() proto.Message { return &echopb.PingRes{} },
		},
	}
}

// echoService 是 Echo 的 typed 薄壳：route_key 取 PingReq.key，业务行为由 rule 表达。
type echoService struct {
	echopb.UnimplementedEchoServer
	Engine *fakedgrpc.ActionEngine
}

func (s *echoService) Ping(ctx context.Context, req *echopb.PingReq) (*echopb.PingRes, error) {
	out := &echopb.PingRes{}
	if err := s.Engine.HandleUnary(ctx, fakedgrpc.Call{
		Endpoint:   EndpointEcho,
		FullMethod: echopb.Echo_Ping_FullMethodName,
		RouteKey:   req.GetKey(),
		Response:   out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}
