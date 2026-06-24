// Package faked 在 daemon 进程内编排 gRPC 与 HTTP 两侧 fake server（内嵌模型）。
//
// 与早期「独立 cmd/faked 子进程」不同：faked server 由 mtkit daemon 直接构造并托管
// （见 faked-integration-plan §0.4）。control 面挂 daemon 主 mux（RegisterControl），
// 数据面（每 fake gRPC 一个 listener + 一个 HTTP MITM 代理 listener）由 Start 开启
// 并把地址经 Addrs 返回给 orchestrator，由后者写入 mtkit.runtime.json——本包不写
// 任何独立 runtime 文件、不开独立 control listener。
package faked

import (
	"context"
	"fmt"
	nethttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/McHarvvvy/mtkit/faked/grpc"
	"github.com/McHarvvvy/mtkit/faked/http"
	"github.com/McHarvvvy/mtkit/faked/http/ca"
)

// 调用日志文件名：落在 cfg.CallLogDir 下，按约定派生、不可配。
// faked_ 前缀标明这是 faked 引擎产生的日志，与同目录的 daemon.log / app.log 区分来源。
const (
	grpcCallLogFileName = "faked_grpc_calls.jsonl"
	httpCallLogFileName = "faked_http_calls.jsonl"
)

// Addrs 是 Start 后 faked 数据面的对外地址，供 orchestrator 写入 runtime / 注入 SUT。
type Addrs struct {
	GRPCEndpoints map[string]string // env_key → "host:port"
	GRPCCallLog   string
	ProxyAddr     string
	HTTPCallLog   string
	CACertFile    string
}

// State 是 faked server 的状态快照，供 daemon control 的 state 接口暴露。
type State struct {
	Ready     bool
	StartedAt time.Time
}

// Server 是 daemon 进程内的 faked 实例。
//
// 生命周期：New（构造 registry / store / control handler，不开 listener）
// → RegisterControl（挂 daemon 主 mux）→ Start（开数据面 listener，返回 Addrs）
// → Stop（释放 listener 与日志）。Start 幂等。
type Server struct {
	cfg           Config
	grpcStore     *grpc.RuleStore
	httpStore     *http.RuleStore
	grpcMethods   *grpc.MethodRegistry
	httpRoutes    *http.RouteRegistry
	grpcEndpoints []grpc.EndpointConfig
	grpcCtrl      *grpc.ControlHandler
	httpCtrl      *http.ControlHandler

	mu         sync.Mutex
	started    bool
	startedAt  time.Time
	grpcLogger *grpc.FileCallLogger
	httpLogger *http.FileCallLogger
	grpcSet    *grpc.ServerSet
	httpProxy  *http.Proxy
	caBundle   *ca.Bundle
	addrs      Addrs
}

// New 据配置与 init() 自注册的 endpoint 构造 server；尚未开任何 listener。
func New(cfg Config) (*Server, error) {
	if err := NormalizeConfig(&cfg); err != nil {
		return nil, err
	}
	grpcEndpoints, err := resolveGRPCEndpoints()
	if err != nil {
		return nil, err
	}
	httpEndpoints := http.RegisteredEndpoints() // 注册即启用，不再经配置过滤

	grpcMethods, err := grpc.BuildMethodRegistry(grpcEndpointsOf(grpcEndpoints))
	if err != nil {
		return nil, fmt.Errorf("build grpc method registry: %w", err)
	}
	httpRoutes, err := http.BuildRouteRegistry(httpEndpoints)
	if err != nil {
		return nil, fmt.Errorf("build http route registry: %w", err)
	}

	grpcStore := grpc.NewRuleStore()
	httpStore := http.NewRuleStore()
	return &Server{
		cfg:           cfg,
		grpcStore:     grpcStore,
		httpStore:     httpStore,
		grpcMethods:   grpcMethods,
		httpRoutes:    httpRoutes,
		grpcEndpoints: grpcEndpoints,
		grpcCtrl:      grpc.NewControlHandler(grpcStore, grpcMethods),
		httpCtrl:      http.NewControlHandler(httpStore, httpRoutes),
	}, nil
}

// RegisterControl 把两侧 rule 注册 handler 挂到 daemon 主 mux（共用 daemon 主端口）。
// 必须在 daemon mux 开始 Serve 之前调用。
//
// wrap 是 control handler 的中间件：daemon 传 withAuth 让 faked control 与 lease 共用 token
// 鉴权（faked 自身不感知 token，鉴权策略由宿主 daemon 决定）；独立进程内测试传 nil 表示不鉴权。
func (s *Server) RegisterControl(mux *nethttp.ServeMux, wrap func(nethttp.HandlerFunc) nethttp.HandlerFunc) {
	s.grpcCtrl.Register(mux, wrap)
	s.httpCtrl.Register(mux, wrap)
}

// Start 开启数据面 listener：打开调用日志 → 起 gRPC server set → 备 CA → 起 HTTP 代理。
// 返回数据面地址。幂等：已启动则直接返回已记录的 Addrs。
func (s *Server) Start(ctx context.Context) (Addrs, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return s.addrs, nil
	}
	if err := os.MkdirAll(s.cfg.RuntimeDir, 0o755); err != nil {
		return Addrs{}, fmt.Errorf("ensure faked runtime dir %s: %w", s.cfg.RuntimeDir, err)
	}

	grpcLog := filepath.Join(s.cfg.CallLogDir, grpcCallLogFileName)
	gl, err := grpc.NewFileCallLogger(grpcLog)
	if err != nil {
		return Addrs{}, fmt.Errorf("open grpc call log: %w", err)
	}
	s.grpcLogger = gl
	httpLog := filepath.Join(s.cfg.CallLogDir, httpCallLogFileName)
	hl, err := http.NewFileCallLogger(httpLog)
	if err != nil {
		s.stopLocked()
		return Addrs{}, fmt.Errorf("open http call log: %w", err)
	}
	s.httpLogger = hl

	grpcEngine := grpc.NewActionEngine(s.grpcStore, s.grpcLogger)
	httpEngine := http.NewActionEngine(s.httpStore, s.httpLogger)

	s.grpcSet = grpc.NewServerSet(grpcEngine, s.grpcEndpoints)
	grpcAddrs, err := s.grpcSet.Start()
	if err != nil {
		s.stopLocked()
		return Addrs{}, fmt.Errorf("start grpc server set: %w", err)
	}

	bundle, certFile, err := s.loadCA()
	if err != nil {
		s.stopLocked()
		return Addrs{}, err
	}
	s.caBundle = bundle

	s.httpProxy = http.NewProxy(s.cfg.ProxyListen, httpEngine, s.httpRoutes, s.caBundle)
	proxyAddr, err := s.httpProxy.Start()
	if err != nil {
		s.stopLocked()
		return Addrs{}, fmt.Errorf("start http proxy: %w", err)
	}

	s.addrs = Addrs{
		GRPCEndpoints: grpcAddrs,
		GRPCCallLog:   grpcLog,
		ProxyAddr:     proxyAddr,
		HTTPCallLog:   httpLog,
		CACertFile:    certFile,
	}
	s.started = true
	s.startedAt = time.Now()
	return s.addrs, nil
}

// Stop 释放数据面 listener 与调用日志；多次调用幂等。
func (s *Server) Stop(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	return nil
}

// stopLocked 释放已启动资源；Start 失败路径与 Stop 共用，调用方须持锁。
func (s *Server) stopLocked() {
	if s.grpcSet != nil {
		s.grpcSet.Stop()
		s.grpcSet = nil
	}
	if s.httpProxy != nil {
		s.httpProxy.Stop()
		s.httpProxy = nil
	}
	if s.grpcLogger != nil {
		_ = s.grpcLogger.Close()
		s.grpcLogger = nil
	}
	if s.httpLogger != nil {
		_ = s.httpLogger.Close()
		s.httpLogger = nil
	}
	s.started = false
}

// Snapshot 返回 server 状态快照。
func (s *Server) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{Ready: s.started, StartedAt: s.startedAt}
}

// loadCA 按动态 / 静态模式准备 HTTPS MITM 用的 CA Bundle，返回 bundle 与对外暴露的 cert 路径。
func (s *Server) loadCA() (*ca.Bundle, string, error) {
	cert, key := s.cfg.CACertFile, s.cfg.CAKeyFile
	switch {
	case cert == "" && key == "":
		bundle, certPath, _, err := ca.Generate(s.cfg.RuntimeDir)
		if err != nil {
			return nil, "", fmt.Errorf("generate ca bundle: %w", err)
		}
		return bundle, certPath, nil
	case cert != "" && key != "":
		bundle, err := ca.LoadFromFiles(cert, key)
		if err != nil {
			return nil, "", fmt.Errorf("load ca bundle: %w", err)
		}
		return bundle, cert, nil
	default:
		return nil, "", fmt.Errorf("http ca_cert_file and ca_key_file must be both set or both empty")
	}
}

// resolveGRPCEndpoints 取所有静态注册的 endpoint（注册即启用），监听地址由 Endpoint.Listen() 提供。
// 监听地址（含端口是否固定）是项目方的选择，框架不干涉；Listen() 为空视为开发期错误
// （endpoint 未声明监听地址），拒绝启动。
func resolveGRPCEndpoints() ([]grpc.EndpointConfig, error) {
	registered := grpc.RegisteredEndpoints()
	out := make([]grpc.EndpointConfig, 0, len(registered))
	for _, ep := range registered {
		if strings.TrimSpace(ep.ID()) == "" {
			return nil, fmt.Errorf("grpc endpoint with empty ID")
		}
		listen := strings.TrimSpace(ep.Listen())
		if listen == "" {
			return nil, fmt.Errorf("faked grpc endpoint %q has empty Listen()", ep.ID())
		}
		out = append(out, grpc.EndpointConfig{Endpoint: ep, Listen: listen})
	}
	return out, nil
}

// grpcEndpointsOf 抽出 EndpointConfig 里的 Endpoint，喂给 BuildMethodRegistry。
func grpcEndpointsOf(cfgs []grpc.EndpointConfig) []grpc.Endpoint {
	out := make([]grpc.Endpoint, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, c.Endpoint)
	}
	return out
}
