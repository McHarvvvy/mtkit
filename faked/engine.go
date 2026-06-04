// Package faked 在 daemon 进程内编排 gRPC 与 HTTP 两侧 fake server（内嵌模型）。
//
// 与早期「独立 cmd/faked 子进程」不同：faked 引擎由 mtkit daemon 直接构造并托管
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

// 调用日志文件名：落在 cfg.RuntimeDir 下，按约定派生、不可配。
const (
	grpcCallLogFileName = "grpc_calls.jsonl"
	httpCallLogFileName = "http_calls.jsonl"
)

// Addrs 是 Start 后 faked 数据面的对外地址，供 orchestrator 写入 runtime / 注入 SUT。
type Addrs struct {
	GRPCEndpoints map[string]string // env_key → "host:port"
	GRPCCallLog   string
	ProxyAddr     string
	HTTPCallLog   string
	CACertFile    string
}

// State 是 faked 引擎的状态快照，供 daemon control 的 state 接口暴露。
type State struct {
	Ready     bool
	StartedAt time.Time
}

// Engine 是 daemon 进程内的 faked 实例。
//
// 生命周期：New（构造 registry / store / control handler，不开 listener）
// → RegisterControl（挂 daemon 主 mux）→ Start（开数据面 listener，返回 Addrs）
// → Stop（释放 listener 与日志）。Start 幂等。
type Engine struct {
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

// New 据配置与 init() 自注册的 endpoint 构造引擎；尚未开任何 listener。
func New(cfg Config) (*Engine, error) {
	if err := NormalizeConfig(&cfg); err != nil {
		return nil, err
	}
	grpcEndpoints, err := resolveGRPCEndpoints(cfg)
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
	return &Engine{
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
func (e *Engine) RegisterControl(mux *nethttp.ServeMux) {
	e.grpcCtrl.Register(mux)
	e.httpCtrl.Register(mux)
}

// Start 开启数据面 listener：打开调用日志 → 起 gRPC server set → 备 CA → 起 HTTP 代理。
// 返回数据面地址。幂等：已启动则直接返回已记录的 Addrs。
func (e *Engine) Start(ctx context.Context) (Addrs, error) {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return e.addrs, nil
	}
	if err := os.MkdirAll(e.cfg.RuntimeDir, 0o755); err != nil {
		return Addrs{}, fmt.Errorf("ensure faked runtime dir %s: %w", e.cfg.RuntimeDir, err)
	}

	grpcLog := filepath.Join(e.cfg.RuntimeDir, grpcCallLogFileName)
	gl, err := grpc.NewFileCallLogger(grpcLog)
	if err != nil {
		return Addrs{}, fmt.Errorf("open grpc call log: %w", err)
	}
	e.grpcLogger = gl
	httpLog := filepath.Join(e.cfg.RuntimeDir, httpCallLogFileName)
	hl, err := http.NewFileCallLogger(httpLog)
	if err != nil {
		e.stopLocked()
		return Addrs{}, fmt.Errorf("open http call log: %w", err)
	}
	e.httpLogger = hl

	grpcEngine := grpc.NewActionEngine(e.grpcStore, e.grpcLogger)
	httpEngine := http.NewActionEngine(e.httpStore, e.httpLogger)

	e.grpcSet = grpc.NewServerSet(grpcEngine, e.grpcEndpoints)
	grpcAddrs, err := e.grpcSet.Start()
	if err != nil {
		e.stopLocked()
		return Addrs{}, fmt.Errorf("start grpc server set: %w", err)
	}

	bundle, certFile, err := e.loadCA()
	if err != nil {
		e.stopLocked()
		return Addrs{}, err
	}
	e.caBundle = bundle

	e.httpProxy = http.NewProxy(e.cfg.ProxyListen, httpEngine, e.httpRoutes, e.caBundle)
	proxyAddr, err := e.httpProxy.Start()
	if err != nil {
		e.stopLocked()
		return Addrs{}, fmt.Errorf("start http proxy: %w", err)
	}

	e.addrs = Addrs{
		GRPCEndpoints: grpcAddrs,
		GRPCCallLog:   grpcLog,
		ProxyAddr:     proxyAddr,
		HTTPCallLog:   httpLog,
		CACertFile:    certFile,
	}
	e.started = true
	e.startedAt = time.Now()
	return e.addrs, nil
}

// Stop 释放数据面 listener 与调用日志；多次调用幂等。
func (e *Engine) Stop(ctx context.Context) error {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopLocked()
	return nil
}

// stopLocked 释放已启动资源；Start 失败路径与 Stop 共用，调用方须持锁。
func (e *Engine) stopLocked() {
	if e.grpcSet != nil {
		e.grpcSet.Stop()
		e.grpcSet = nil
	}
	if e.httpProxy != nil {
		e.httpProxy.Stop()
		e.httpProxy = nil
	}
	if e.grpcLogger != nil {
		_ = e.grpcLogger.Close()
		e.grpcLogger = nil
	}
	if e.httpLogger != nil {
		_ = e.httpLogger.Close()
		e.httpLogger = nil
	}
	e.started = false
}

// Snapshot 返回引擎状态快照。
func (e *Engine) Snapshot() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return State{Ready: e.started, StartedAt: e.startedAt}
}

// loadCA 按动态 / 静态模式准备 HTTPS MITM 用的 CA Bundle，返回 bundle 与对外暴露的 cert 路径。
func (e *Engine) loadCA() (*ca.Bundle, string, error) {
	cert, key := e.cfg.CACertFile, e.cfg.CAKeyFile
	switch {
	case cert == "" && key == "":
		bundle, certPath, _, err := ca.Generate(e.cfg.RuntimeDir)
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

// resolveGRPCEndpoints 取所有静态注册的 endpoint（注册即启用），套用配置里的监听覆盖。
// 配置引用了未注册的 id 视为开发期错误，拒绝启动。
func resolveGRPCEndpoints(cfg Config) ([]grpc.EndpointConfig, error) {
	overrides := make(map[string]string, len(cfg.GRPCEndpoints))
	for _, ec := range cfg.GRPCEndpoints {
		if strings.TrimSpace(ec.ID) == "" {
			return nil, fmt.Errorf("faked grpc endpoint override with empty id")
		}
		overrides[ec.ID] = ec.Listen
	}
	registered := grpc.RegisteredEndpoints()
	known := make(map[string]bool, len(registered))
	out := make([]grpc.EndpointConfig, 0, len(registered))
	for _, ep := range registered {
		known[ep.ID()] = true
		listen := strings.TrimSpace(overrides[ep.ID()])
		if listen == "" {
			listen = "127.0.0.1:0"
		}
		out = append(out, grpc.EndpointConfig{Endpoint: ep, Listen: listen})
	}
	for id := range overrides {
		if !known[id] {
			return nil, fmt.Errorf("faked grpc endpoint %q in config but not statically registered", id)
		}
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
