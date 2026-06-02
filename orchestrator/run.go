package orchestrator

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/McHarvvvy/mtkit/constant"
	"github.com/McHarvvvy/mtkit/container"
	"github.com/McHarvvvy/mtkit/orchestrator/client"
	"github.com/McHarvvvy/mtkit/sut"
)

// Runnable 表示可执行的测试入口。*testing.M 天然满足此接口。
type Runnable interface {
	Run() int
}

// RegisterContainersFunc 定义容器注册函数。
type RegisterContainersFunc func(ctx context.Context) ([]container.ContainerRegistration, error)

// RunConfig 是 orchestrator 执行 daemon/client 分流所需的配置。
type RunConfig struct {
	Project             string
	RuntimePath         string
	DaemonAddr          string
	DaemonToken         string
	DaemonIdleTTL       time.Duration
	ClientHTTPTimeout   time.Duration
	SUT                 sut.SUTBootPlan
	RegisterContainers  RegisterContainersFunc
	ContainerRegistries []container.ContainerRegistration
}

// Run 按 MT_MODE 分流到 daemon 模式或 client 模式。
func Run(cfg RunConfig, m Runnable) int {
	if os.Getenv(constant.EnvMTMode) == constant.MTModeDaemon {
		return runDaemonMode(cfg)
	}
	return runClientMode(cfg, m)
}

func runDaemonMode(cfg RunConfig) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if cfg.RegisterContainers == nil {
		log.Printf("mtkit registerContainers is nil")
		return 1
	}
	regs := cfg.ContainerRegistries
	if len(regs) == 0 {
		var loadErr error
		regs, loadErr = cfg.RegisterContainers(ctx)
		if loadErr != nil {
			log.Printf("mtkit register containers failed: %v", loadErr)
			return 1
		}
	}

	dcfg := Config{
		Addr:        cfg.DaemonAddr,
		Token:       cfg.DaemonToken,
		Project:     cfg.Project,
		RuntimePath: cfg.RuntimePath,
		IdleTTL:     cfg.DaemonIdleTTL,
		SUT:         cfg.SUT,
	}
	if v := strings.TrimSpace(os.Getenv(constant.EnvMTRuntime)); v != "" {
		dcfg.RuntimePath = v
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
	// client 模式的完整生命周期（连接/自启 daemon → 持租约 → 心跳 → 运行 → 释放）
	// 内聚在 orchestrator/client，这里只做配置转发。
	return client.RunUnderLease(context.Background(), client.Config{
		Project:     cfg.Project,
		RuntimePath: cfg.RuntimePath,
		HTTPTimeout: cfg.ClientHTTPTimeout,
	}, m)
}
