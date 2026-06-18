package mtkit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	fixture "github.com/McHarvvvy/mtkit/client/fixture"
	"github.com/McHarvvvy/mtkit/container"
	mtfaked "github.com/McHarvvvy/mtkit/faked"
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
	// IdleTTLSeconds 是 daemon 空闲自动退出秒数；运行期 Duration 由 Run 派生。
	IdleTTLSeconds int `json:"idle_ttl_seconds"`
}

// ClientModeConfig 定义 client 模式配置。
type ClientModeConfig struct {
	// HTTPTimeoutSeconds 是客户端请求 daemon 的 HTTP 超时秒数；运行期 Duration 由 Run 派生。
	HTTPTimeoutSeconds int `json:"http_timeout_seconds"`
}

// MTKit 是 mtkit 的统一运行入口。
type MTKit struct {
	cfg                Config
	registerContainers RegisterContainersFunc
	registrations      []container.ContainerRegistration
	sut                SUTBootPlan
	schemaResources    []fixture.Resource
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

// WithSchema 注册需 provision 的资源列表，多次调用追加。
// 资源在 client 模式 Acquire 成功后、m.Run() 前按 daemon 生命周期幂等执行一次（见 orchestrator.provisionRunnable）。
func (t *MTKit) WithSchema(resources []fixture.Resource) *MTKit {
	t.schemaResources = append(t.schemaResources, resources...)
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
		DaemonIdleTTL:       secondsOrDefault(t.cfg.DaemonMode.IdleTTLSeconds, 36*time.Second),
		ClientHTTPTimeout:   secondsOrDefault(t.cfg.ClientMode.HTTPTimeoutSeconds, 60*time.Second),
		SUTConfig:           toSUTRuntimeConfig(t.cfg.SUT),
		SUT:                 t.sut,
		Faked:               toFakedEngineConfig(t.cfg.Faked, t.cfg.RuntimeDir, t.cfg.RuntimeFile),
		FakedEnabled:        t.cfg.Faked.Enabled,
		SchemaResources:     t.schemaResources,
		ContainerRegistries: t.registrations,
	}, m)
}

// toSUTRuntimeConfig 把配置层的 SUTConfig（只含秒数）转成 SUT 运行期配置（含 time.Duration）。
// 超时 / TTL 的默认值在此一次性派生——配置层不持有任何 Duration。
func toSUTRuntimeConfig(cfg SUTConfig) sut.Config {
	return sut.Config{
		Enabled:      cfg.Enabled,
		BinaryPath:   cfg.BinaryPath,
		SourceDir:    cfg.SourceDir,
		BuildArgs:    cfg.BuildArgs,
		BuildTimeout: secondsOrDefault(cfg.Lifecycle.BuildTimeoutSeconds, 120*time.Second),
		ReadyTimeout: secondsOrDefault(cfg.Lifecycle.ReadyTimeoutSeconds, 45*time.Second),
		GracePeriod:  secondsOrDefault(cfg.Lifecycle.GracePeriodSeconds, 5*time.Second),
		IdleTTL:      secondsOrDefault(cfg.IdleTTLSeconds, 2*time.Second),
	}
}

// toFakedEngineConfig 把 mtkit 配置的 faked 块转成内嵌 server 配置。
// gRPC endpoint 监听地址不在此——由 Endpoint.Listen() 在代码中声明。
// 动态 CA 等私有产物落在 <mtkit.runtime_dir>/faked 下；调用日志与 daemon.log / app.log
// 同属诊断流，统一落在 <mtkit.runtime_dir>/logs 下（见 runtime.LogDir）。
func toFakedEngineConfig(cfg FakedConfig, runtimeDir, runtimePath string) mtfaked.Config {
	return mtfaked.Config{
		ProxyListen: cfg.HTTP.ProxyListen,
		CACertFile:  cfg.HTTP.CACertFile,
		CAKeyFile:   cfg.HTTP.CAKeyFile,
		RuntimeDir:  filepath.Join(runtimeDir, "faked"),
		CallLogDir:  mtruntime.LogDir(runtimePath),
	}
}
