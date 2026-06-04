package mtkit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/McHarvvvy/mtkit/container"
	mtfaked "github.com/McHarvvvy/mtkit/faked"
	"github.com/McHarvvvy/mtkit/orchestrator"
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
	// Project 是应用身份标识，由 New(project, cfg) 传入，不写配置文件
	// （默认配置/runtime 路径含 <project> 段，写文件里会有读文件前无法推导路径的鸡生蛋问题）。
	Project string `json:"-"`
	// RuntimeDir 是 mtkit 产物目录（文件夹）：runtime 文件 + daemon/SUT 日志 + 构建产物 + faked 产物。
	RuntimeDir string `json:"runtime_dir"`
	// RuntimeFile 是 runtime 文件，缺省 <RuntimeDir>/mtkit.runtime.json；
	// 可配为纯文件名（落在 RuntimeDir 下）或绝对路径。normalize 后为解析好的最终路径。
	RuntimeFile string `json:"runtime_file"`
	// DaemonMode 定义 daemon 模式（re-exec 守护进程）配置。
	DaemonMode DaemonModeConfig `json:"daemon_mode"`
	// ClientMode 定义 client 模式（持租约的测试进程）配置。
	ClientMode ClientModeConfig `json:"client_mode"`
	// SUT 定义被测服务进程配置。
	SUT SUTConfig `json:"sut"`
	// Faked 定义内嵌 faked 引擎配置。
	Faked FakedConfig `json:"faked"`
}

// DaemonModeConfig 定义 daemon 模式配置。
type DaemonModeConfig struct {
	// Addr 指定 daemon 监听地址，默认建议使用 127.0.0.1:0。
	Addr string `json:"addr"`
	// Token 指定 daemon 鉴权令牌，空值时由 daemon 自动生成。
	Token string `json:"-"`
	// IdleTTL 指定 daemon 空闲自动退出时间。
	IdleTTL time.Duration `json:"-"`
	// IdleTTLSeconds 是 JSON 配置里的空闲退出秒数。
	IdleTTLSeconds int `json:"idle_ttl_seconds"`
}

// ClientModeConfig 定义 client 模式配置。
type ClientModeConfig struct {
	// HTTPTimeout 指定客户端请求 daemon 的 HTTP 超时时间。
	HTTPTimeout time.Duration `json:"-"`
	// HTTPTimeoutSeconds 是 JSON 配置里的 HTTP 超时秒数。
	HTTPTimeoutSeconds int `json:"http_timeout_seconds"`
}

// MTKit 是 mtkit 的统一运行入口。
type MTKit struct {
	cfg                Config
	registerContainers RegisterContainersFunc
	registrations      []container.ContainerRegistration
	sut                SUTBootPlan
}

// New 创建 MTKit 实例。
func New(project string, cfg *Config) (*MTKit, error) {
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("project is required")
	}
	if cfg == nil {
		loaded, err := LoadConfig(project)
		if err != nil {
			return nil, err
		}
		cfg = loaded
	} else {
		if err := normalizeConfig(project, cfg); err != nil {
			return nil, err
		}
	}

	return &MTKit{cfg: *cfg}, nil
}

// RuntimePath 返回框架内部使用的 runtime 文件路径。
func (t *MTKit) RuntimePath() string {
	return t.cfg.RuntimeFile
}

// RegisterContainers 注册容器定义。
func (t *MTKit) RegisterContainers(fn RegisterContainersFunc) *MTKit {
	t.registerContainers = fn
	return t
}

// RegisterSUT 注册被测服务启动计划。
func (t *MTKit) RegisterSUT(plan SUTBootPlan) *MTKit {
	t.sut = plan
	return t
}

// Run 是 TestMain 的统一执行入口。
func (t *MTKit) Run(m Runnable) int {
	if t.registerContainers == nil {
		fmt.Fprintln(os.Stderr, "mtkit registerContainers is nil")
		return 1
	}
	regs, err := t.registerContainers(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "mtkit register containers failed: %v\n", err)
		return 1
	}
	if err := container.ValidateRegistrations(regs); err != nil {
		fmt.Fprintf(os.Stderr, "mtkit container registrations invalid: %v\n", err)
		return 1
	}
	if t.cfg.SUT.Enabled && t.sut == nil {
		fmt.Fprintln(os.Stderr, "mtkit sut enabled but RegisterSUT was not called")
		return 1
	}
	t.registrations = regs
	return orchestrator.Run(orchestrator.RunConfig{
		Project:             t.cfg.Project,
		RuntimePath:         t.cfg.RuntimeFile,
		RuntimeDir:          t.cfg.RuntimeDir,
		DaemonAddr:          t.cfg.DaemonMode.Addr,
		DaemonToken:         t.cfg.DaemonMode.Token,
		DaemonIdleTTL:       t.cfg.DaemonMode.IdleTTL,
		ClientHTTPTimeout:   t.cfg.ClientMode.HTTPTimeout,
		SUTConfig:           toSUTRuntimeConfig(t.cfg.SUT),
		SUT:                 t.sut,
		Faked:               toFakedEngineConfig(t.cfg.Faked, t.cfg.RuntimeDir),
		FakedEnabled:        t.cfg.Faked.Enabled,
		RegisterContainers:  orchestrator.RegisterContainersFunc(t.registerContainers),
		ContainerRegistries: t.registrations,
	}, m)
}

func toSUTRuntimeConfig(cfg SUTConfig) sut.Config {
	return sut.Config{
		Enabled:      cfg.Enabled,
		BinaryPath:   cfg.BinaryPath,
		SourceDir:    cfg.SourceDir,
		BuildArgs:    cfg.BuildArgs,
		BuildTimeout: cfg.Lifecycle.BuildTimeout,
		ReadyTimeout: cfg.Lifecycle.ReadyTimeout,
		GracePeriod:  cfg.Lifecycle.GracePeriod,
		IdleTTL:      cfg.IdleTTL,
	}
}

// toFakedEngineConfig 把 mtkit 配置的 faked 块转成内嵌引擎配置。
// faked 引擎产物（调用日志 / 动态 CA）落在 <mtkit.runtime_dir>/faked 下。
func toFakedEngineConfig(cfg FakedConfig, runtimeDir string) mtfaked.Config {
	eps := make([]mtfaked.GRPCEndpointConfig, 0, len(cfg.GRPC.Endpoints))
	for _, e := range cfg.GRPC.Endpoints {
		eps = append(eps, mtfaked.GRPCEndpointConfig{ID: e.ID, Listen: e.Listen})
	}
	return mtfaked.Config{
		GRPCEndpoints: eps,
		ProxyListen:   cfg.HTTP.ProxyListen,
		CACertFile:    cfg.HTTP.CACertFile,
		CAKeyFile:     cfg.HTTP.CAKeyFile,
		RuntimeDir:    filepath.Join(runtimeDir, "faked"),
	}
}
