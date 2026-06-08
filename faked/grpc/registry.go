package grpc

import (
	"fmt"
	"sync"

	googrpc "google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// MethodIdentity 是一个 gRPC RPC method 的唯一身份：(endpoint, full_method)。
//
// 同时承担两个角色——
//   - 嵌入 MethodSpec：作为作者侧 spec 的"身份"部分；
//   - 作为 MethodRegistry 内部 map 的 key。
//
// 这样 spec 的身份字段与索引 key 共用一份定义，避免"影子类型"概念漂移。
type MethodIdentity struct {
	Endpoint   string
	FullMethod string
}

// MethodSpec 静态描述一个 gRPC RPC method 的 fake 元数据。
//
// 嵌入 MethodIdentity：作者写 spec 时用 MethodIdentity 字面值显式表达"这是身份字段"。
// NewResponse 给 control API 注册阶段用于校验 base64 protobuf bytes 能反序列化
// 为目标 method 的 response 类型——它是 spec 的"非身份载荷"，所以不属于 MethodIdentity。
type MethodSpec struct {
	MethodIdentity
	NewResponse func() proto.Message
}

// EndpointDeps 由 daemon 在启动 gRPC server 时传给 Endpoint.Register；
// 通过依赖注入让 typed 薄壳获取 ActionEngine 引用。
type EndpointDeps struct {
	Engine *ActionEngine
}

// Endpoint 表示一个外部 gRPC 服务的 fake 边界，对应一个独立监听地址。
//
// 一个 Endpoint 上可注册多个 proto service；每个 enabled Endpoint 拥有
// 自己的 *googrpc.Server 实例。
type Endpoint interface {
	// ID 返回 endpoint 标识，例 "forum"；与配置中 endpoints[].id 对齐。
	ID() string
	// EnvKey 返回 SUT 期望的环境变量名（如 "MSSIOT_IT_RPC_FORUM_ADDR"）。
	// 由 endpoint 自身声明（项目领域知识）：daemon 用它作 runtime 地址 map 的 key
	// 并注入 SUT，不再经配置文件传入（见 faked-integration-plan §9.2）。
	EnvKey() string
	// Listen 返回本 endpoint fake server 的监听地址（"host:port"）。
	// 由 endpoint 自身声明（项目领域知识，与 EnvKey 同源），不再经配置文件传入：
	// 用固定端口还是 "127.0.0.1:0" 随机端口是项目方的选择，框架不干涉、不强制。
	Listen() string
	// Register 把所有 typed 薄壳注册到 grpc server；deps 提供 ActionEngine。
	Register(s *googrpc.Server, deps EndpointDeps)
	// Methods 返回本 endpoint 静态声明的所有 RPC method 元数据。
	Methods() []MethodSpec
}

// endpointRegistry 是 init() 自注册的 package-level Endpoint 列表。
// 互斥锁保护 append；读多写少，启动后只读。
var endpointRegistry struct {
	mu        sync.Mutex
	endpoints []Endpoint
}

// RegisterEndpoint 在 init() 阶段把 Endpoint 自注册到 package-level 列表。
// daemon 启动时遍历 RegisteredEndpoints() 装配静态 Endpoint。
func RegisterEndpoint(e Endpoint) {
	endpointRegistry.mu.Lock()
	defer endpointRegistry.mu.Unlock()
	endpointRegistry.endpoints = append(endpointRegistry.endpoints, e)
}

// RegisteredEndpoints 返回当前已 init() 自注册的 Endpoint 副本。
func RegisteredEndpoints() []Endpoint {
	endpointRegistry.mu.Lock()
	defer endpointRegistry.mu.Unlock()
	out := make([]Endpoint, len(endpointRegistry.endpoints))
	copy(out, endpointRegistry.endpoints)
	return out
}

// MethodRegistry 持有 daemon 启动时从 enabled endpoint 收集的所有 MethodSpec。
// control API 用它在注册阶段校验：
//   - HasEndpoint：endpoint 是否在本 daemon 启用范围内
//   - Lookup：(endpoint, full_method) 是否静态注册，并取出 NewResponse 校验 response bytes
//
// map key 直接复用 MethodIdentity——不再单独定义影子 key 类型。
type MethodRegistry struct {
	endpoints map[string]bool
	specs     map[MethodIdentity]MethodSpec
}

// BuildMethodRegistry 从一组 enabled Endpoint 收集 MethodSpec，做去重与一致性校验。
// 校验失败说明开发期配置错误（重复声明 / endpoint 不一致），daemon 应拒绝启动。
func BuildMethodRegistry(endpoints []Endpoint) (*MethodRegistry, error) {
	r := &MethodRegistry{
		endpoints: make(map[string]bool, len(endpoints)),
		specs:     make(map[MethodIdentity]MethodSpec),
	}
	for _, e := range endpoints {
		id := e.ID()
		if id == "" {
			return nil, fmt.Errorf("grpc endpoint with empty ID")
		}
		if r.endpoints[id] {
			return nil, fmt.Errorf("duplicate grpc endpoint id %q", id)
		}
		r.endpoints[id] = true
		for _, m := range e.Methods() {
			if m.Endpoint != id {
				return nil, fmt.Errorf("method %s declares endpoint %q but registered under endpoint %q",
					m.FullMethod, m.Endpoint, id)
			}
			if m.FullMethod == "" {
				return nil, fmt.Errorf("endpoint %q has method with empty FullMethod", id)
			}
			if m.NewResponse == nil {
				return nil, fmt.Errorf("endpoint %q method %s has nil NewResponse", id, m.FullMethod)
			}
			if _, exists := r.specs[m.MethodIdentity]; exists {
				return nil, fmt.Errorf("duplicate method %s in endpoint %s", m.FullMethod, m.Endpoint)
			}
			r.specs[m.MethodIdentity] = m
		}
	}
	return r, nil
}

// HasEndpoint 判断 endpoint 是否被注册到本 registry。
func (r *MethodRegistry) HasEndpoint(id string) bool {
	return r.endpoints[id]
}

// Lookup 按 (endpoint, full_method) 查询 MethodSpec。
func (r *MethodRegistry) Lookup(endpoint, fullMethod string) (MethodSpec, bool) {
	spec, ok := r.specs[MethodIdentity{Endpoint: endpoint, FullMethod: fullMethod}]
	return spec, ok
}
