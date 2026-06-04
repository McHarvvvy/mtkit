package grpc

import (
	"fmt"
	"net"
	"sync"

	googrpc "google.golang.org/grpc"
)

// EndpointConfig 是 daemon 解析配置后传给 ServerSet 的单个 endpoint 启动信息。
//
// Endpoint 字段由 daemon 通过 ID 在 RegisteredEndpoints() 里取出；Listen 来自
// mtkit 配置的 faked.grpc.endpoints[].listen 覆盖（缺省随机端口）。env_key 不在此——
// 由 Endpoint.EnvKey() 提供（见 faked-integration-plan §9.2）。运行期不变。
type EndpointConfig struct {
	Endpoint Endpoint
	Listen   string // "host:port"，端口可为 0 以让 OS 分配。
}

// ServerSet 管理一组 enabled gRPC endpoint 的独立 *googrpc.Server。
// 每个 endpoint 对应一个 listener + 一个 server；endpoint 之间相互独立。
type ServerSet struct {
	engine    *ActionEngine
	configs   []EndpointConfig
	mu        sync.Mutex
	instances []*serverInstance
	started   bool
}

type serverInstance struct {
	config   EndpointConfig
	listener net.Listener
	server   *googrpc.Server
}

// NewServerSet 构造 ServerSet。configs 顺序决定启动顺序，但 stop 时反向不影响正确性。
func NewServerSet(engine *ActionEngine, configs []EndpointConfig) *ServerSet {
	return &ServerSet{engine: engine, configs: configs}
}

// Start 为每个 EndpointConfig 监听端口、创建 grpc.Server、注册 service、并发 Serve。
// 返回 EnvKey → "host:port" 映射，供 daemon 写入 runtime.json。
//
// 任一 endpoint 启动失败：Start 会 Stop 已就绪的实例，返回原始错误。
func (s *ServerSet) Start() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil, fmt.Errorf("grpc server set already started")
	}
	deps := EndpointDeps{Engine: s.engine}
	addrs := make(map[string]string, len(s.configs))
	for _, cfg := range s.configs {
		lis, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			s.stopUnlocked()
			return nil, fmt.Errorf("listen %s: %w", cfg.Endpoint.ID(), err)
		}
		srv := googrpc.NewServer()
		cfg.Endpoint.Register(srv, deps)

		inst := &serverInstance{config: cfg, listener: lis, server: srv}
		s.instances = append(s.instances, inst)

		// Serve 阻塞；用 goroutine 异步运行。退出由 GracefulStop / Stop 触发。
		go func(in *serverInstance) {
			_ = in.server.Serve(in.listener)
		}(inst)

		addrs[cfg.Endpoint.EnvKey()] = lis.Addr().String()
	}
	s.started = true
	return addrs, nil
}

// Stop GracefulStop 所有已启动的 *grpc.Server。已 Stop 多次幂等。
func (s *ServerSet) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopUnlocked()
}

// stopUnlocked 由 Start 失败和外部 Stop 共用；调用方必须已持锁。
func (s *ServerSet) stopUnlocked() {
	for _, inst := range s.instances {
		if inst.server != nil {
			inst.server.GracefulStop()
		}
	}
	s.instances = nil
	s.started = false
}
