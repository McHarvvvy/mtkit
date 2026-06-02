package mtkit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/McHarvvvy/mtkit/container"
	"github.com/McHarvvvy/mtkit/orchestrator"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
	"github.com/McHarvvvy/mtkit/sut"
)

// Runnable 表示可执行的测试入口。*testing.M 天然满足此接口。
type Runnable interface {
	Run() int
}

// RegisterContainersFunc 定义容器注册函数。
type RegisterContainersFunc func(ctx context.Context) ([]container.ContainerRegistration, error)

// StartSUTInput 定义被测服务启动输入。
type StartSUTInput = sut.StartSUTInput

// SUTBootPlan 定义被测服务启动计划接口。
type SUTBootPlan = sut.SUTBootPlan

// Config 定义 mtkit 的外部配置模型。
type Config struct {
	// Global 定义 daemon 与 client 的共享配置。
	Global GlobalConfig
	// Daemon 定义守护进程配置。
	Daemon DaemonConfig
	// Client 定义测试客户端配置。
	Client ClientConfig
	// SUT 定义被测服务启动计划。
	SUT SUTBootPlan
}

// GlobalConfig 定义 daemon 与 client 的共享配置。
type GlobalConfig struct {
	// Project 指定项目标识，用于 runtime 文件隔离。
	Project string
	// RuntimePath 指定共享 runtime 文件路径，空值时按 Project 自动推导。
	RuntimePath string
}

// DaemonConfig 定义守护进程配置。
type DaemonConfig struct {
	// Addr 指定 daemon 监听地址，默认建议使用 127.0.0.1:0。
	Addr string
	// Token 指定 daemon 鉴权令牌，空值时由 daemon 自动生成。
	Token string
	// IdleTTL 指定 daemon 空闲自动退出时间。
	IdleTTL time.Duration
}

// ClientConfig 定义客户端配置。
type ClientConfig struct {
	// HTTPTimeout 指定客户端请求 daemon 的 HTTP 超时时间。
	HTTPTimeout time.Duration
}

// MTKit 是 mtkit 的统一运行入口。
type MTKit struct {
	cfg                Config
	registerContainers RegisterContainersFunc
	registrations      []container.ContainerRegistration
}

// New 创建 MTKit 实例。
func New(cfg Config, registerContainers RegisterContainersFunc) (*MTKit, error) {
	normalizeGlobal(&cfg.Global)
	if strings.TrimSpace(cfg.Daemon.Addr) == "" {
		return nil, fmt.Errorf("daemon addr is required")
	}
	if cfg.Client.HTTPTimeout <= 0 {
		return nil, fmt.Errorf("client http timeout must be > 0")
	}
	if cfg.Daemon.IdleTTL <= 0 {
		return nil, fmt.Errorf("daemon idle ttl must be > 0")
	}
	if registerContainers == nil {
		return nil, fmt.Errorf("registerContainers cannot be nil")
	}

	regs, err := registerContainers(context.Background())
	if err != nil {
		return nil, err
	}
	if err := container.ValidateRegistrations(regs); err != nil {
		return nil, err
	}

	return &MTKit{
		cfg:                cfg,
		registerContainers: registerContainers,
		registrations:      regs,
	}, nil
}

func normalizeGlobal(cfg *GlobalConfig) {
	project := strings.TrimSpace(cfg.Project)
	if project == "" {
		project = "default"
	}
	runtimePath := strings.TrimSpace(cfg.RuntimePath)
	if runtimePath == "" {
		runtimePath = mtruntime.DefaultRuntimePath(project)
	}
	cfg.Project = project
	cfg.RuntimePath = runtimePath
}

// RuntimePath 返回框架内部使用的 runtime 文件路径。
func (t *MTKit) RuntimePath() string {
	return t.cfg.Global.RuntimePath
}

// Run 是 TestMain 的统一执行入口。
func (t *MTKit) Run(m Runnable) int {
	return orchestrator.Run(orchestrator.RunConfig{
		Project:             t.cfg.Global.Project,
		RuntimePath:         t.cfg.Global.RuntimePath,
		DaemonAddr:          t.cfg.Daemon.Addr,
		DaemonToken:         t.cfg.Daemon.Token,
		DaemonIdleTTL:       t.cfg.Daemon.IdleTTL,
		ClientHTTPTimeout:   t.cfg.Client.HTTPTimeout,
		SUT:                 t.cfg.SUT,
		RegisterContainers:  orchestrator.RegisterContainersFunc(t.registerContainers),
		ContainerRegistries: t.registrations,
	}, m)
}
