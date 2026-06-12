package orchestrator

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	fixture "github.com/McHarvvvy/mtkit/client/fixture"
	"github.com/McHarvvvy/mtkit/constant"
	"github.com/McHarvvvy/mtkit/container"
	mtfaked "github.com/McHarvvvy/mtkit/faked"
	"github.com/McHarvvvy/mtkit/fixtured/protocol"
	"github.com/McHarvvvy/mtkit/orchestrator/client"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
	"github.com/McHarvvvy/mtkit/sut"
)

// Runnable 表示可执行的测试入口。*testing.M 天然满足此接口。
type Runnable interface {
	Run() int
}

// RunConfig 是 orchestrator 执行 daemon/client 分流所需的配置。
type RunConfig struct {
	Project           string
	RuntimePath       string
	RuntimeDir        string
	DaemonAddr        string
	DaemonToken       string
	DaemonIdleTTL     time.Duration
	ClientHTTPTimeout time.Duration
	SUTConfig         sut.Config
	SUT               sut.SUTBootPlan
	Faked             mtfaked.Config
	FakedEnabled      bool
	SchemaResources   []fixture.Resource
	// ContainerRegistries 是门面已校验（ValidateRegistrations）的容器注册项；
	// daemon 模式直接消费，空切片由 Daemon.New → NewBundle 兜底 fail-loud。
	ContainerRegistries []container.ContainerRegistration
}

// Run 按 MTKIT_RUN_MODE 分流到 daemon 模式或 client 模式。
func Run(cfg RunConfig, m Runnable) int {
	if os.Getenv(constant.EnvMTKitRunMode) == constant.MTModeDaemon {
		return runDaemonMode(cfg)
	}
	return runClientMode(cfg, m)
}

func runDaemonMode(cfg RunConfig) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	regs := cfg.ContainerRegistries

	dcfg := Config{
		Addr:         cfg.DaemonAddr,
		Token:        cfg.DaemonToken,
		Project:      cfg.Project,
		RuntimePath:  cfg.RuntimePath,
		RuntimeDir:   cfg.RuntimeDir,
		IdleTTL:      cfg.DaemonIdleTTL,
		Faked:        cfg.Faked,
		FakedEnabled: cfg.FakedEnabled,
		SUTConfig:    cfg.SUTConfig,
		SUT:          cfg.SUT,
	}

	d, err := New(dcfg, regs)
	if err != nil {
		log.Printf("mtkit daemon.New failed: %v", err)
		return 1
	}
	if err = d.Start(ctx); err != nil {
		log.Printf("mtkit daemon mode exited with error: %v", err)
		return 1
	}
	return 0
}

func runClientMode(cfg RunConfig, m Runnable) int {
	if m == nil {
		return 1
	}
	runnable := m
	if len(cfg.SchemaResources) > 0 {
		runnable = provisionRunnable{cfg: cfg, next: m}
	}
	// client 模式的完整生命周期（连接/自启 daemon → 持租约 → 心跳 → 运行 → 释放）
	// 内聚在 orchestrator/client，这里只做配置转发。
	return client.RunUnderLease(context.Background(), client.Config{
		Project:     cfg.Project,
		RuntimePath: cfg.RuntimePath,
		HTTPTimeout: cfg.ClientHTTPTimeout,
	}, runnable)
}

type provisionRunnable struct {
	cfg  RunConfig
	next Runnable
}

func (r provisionRunnable) Run() int {
	ri, err := mtruntime.Read(r.cfg.RuntimePath)
	if err != nil {
		log.Printf("read mtkit runtime for provision: %v", err)
		return 1
	}
	ledgerPath := protocol.LedgerPath(r.cfg.RuntimePath)
	reg := fixture.NewFileRegistry(ledgerPath)
	env := fixture.NewPersistEnv(ri.SUTEnv)
	if err := fixture.Provision(context.Background(), reg, env, r.cfg.SchemaResources); err != nil {
		log.Printf("provision failed: %v", err)
		return 1
	}
	return r.next.Run()
}
