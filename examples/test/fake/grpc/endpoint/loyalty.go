// Package endpoint 是业务侧 fake gRPC endpoint 的自注册入口（你项目里的 test/fake/grpc/endpoint）。
//
// 每个外部 gRPC 服务一个文件，集中存放：endpoint 定义 + Methods 静态声明 + typed 薄壳。
// init() 调 fakedgrpc.RegisterEndpoint 自注册到 package-level 列表，由 bootstrap blank-import 触发。
// 本包是**服务端叶子**：只 import faked/grpc 与对应 pb 包，**禁止反向依赖 bootstrap / 用例 / harness / 业务源码**。
// 针对 endpoint 注册响应规则的客户端封装（如有复用需要）放同级 rule 包；规则不复用时直接在用例里 inline 注册。
package endpoint

import (
	"context"

	googrpc "google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	fakedgrpc "github.com/McHarvvvy/mtkit/faked/grpc"

	// 占位：你项目里 SUT 消费的那个外部服务的 generated pb 包。
	loyaltypb "shopdemo/api/loyaltypb"
)

// EndpointLoyalty 是 loyalty 外部 gRPC 服务的 endpoint 标识（control API 里的 endpoint 取值）。
const EndpointLoyalty = "loyalty"

// EnvKeyLoyalty 是 SUT 用来覆盖 loyalty 依赖地址的环境变量名。
// 它既由本 endpoint 经 EnvKey() 上报给 daemon（决定注入 SUT 的地址 key），也必须与 SUT 消费侧
// os.Getenv 读取的名字**逐字一致**——是两处项目代码的共享契约。
const EnvKeyLoyalty = "SHOP_IT_RPC_LOYALTY_ADDR"

func init() { fakedgrpc.RegisterEndpoint(&loyaltyEndpoint{}) }

type loyaltyEndpoint struct{}

func (loyaltyEndpoint) ID() string     { return EndpointLoyalty }
func (loyaltyEndpoint) EnvKey() string { return EnvKeyLoyalty }

// Listen 用 :0 让 OS 随机分配端口，避免本地并发 / 残留进程端口冲突；SUT 经 runtime 的 env_key 发现实际地址。
func (loyaltyEndpoint) Listen() string { return "127.0.0.1:0" }

// Register 把该外部进程上所有 generated gRPC service 挂到 server（多 service 共享同一 endpoint）。
func (loyaltyEndpoint) Register(s *googrpc.Server, deps fakedgrpc.EndpointDeps) {
	loyaltypb.RegisterLoyaltyServer(s, &loyaltyService{Engine: deps.Engine})
}

// Methods 声明本 endpoint 需 fake 的全部 RPC，供注册期校验 response 类型。
func (loyaltyEndpoint) Methods() []fakedgrpc.MethodSpec {
	return []fakedgrpc.MethodSpec{
		{
			MethodIdentity: fakedgrpc.MethodIdentity{
				Endpoint:   EndpointLoyalty,
				FullMethod: loyaltypb.Loyalty_GetPoints_FullMethodName,
			},
			NewResponse: func() proto.Message { return &loyaltypb.GetPointsRes{} },
		},
	}
}

// loyaltyService 是 Loyalty proto service 的 typed 薄壳。
//
// 薄壳严守“四步法”，**严禁写业务分支**——业务由测试用例经 control API 注册 rule 表达：
//  ① 接收 typed request；② 取 route_key（从真实请求字段）；③ 建 typed response 容器；④ 调通用 HandleUnary。
type loyaltyService struct {
	loyaltypb.UnimplementedLoyaltyServer
	Engine *fakedgrpc.ActionEngine
}

// GetPoints 是 Loyalty.GetPoints 的 fake 薄壳。RouteKey 取 req.GetAccountId()——它是并发隔离根基：
// 每个用例注册规则时带本用例专属 route_key，引擎按 (endpoint, method, route_key) 精确命中只本用例那条。
func (s *loyaltyService) GetPoints(ctx context.Context, req *loyaltypb.GetPointsReq) (*loyaltypb.GetPointsRes, error) {
	out := &loyaltypb.GetPointsRes{}
	if err := s.Engine.HandleUnary(ctx, fakedgrpc.Call{
		Endpoint:   EndpointLoyalty,
		FullMethod: loyaltypb.Loyalty_GetPoints_FullMethodName,
		RouteKey:   req.GetAccountId(),
		Response:   out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}
