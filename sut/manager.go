package sut

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/McHarvvvy/mtkit/proc"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

type StartSUTInput struct {
	Project     string
	RuntimePath string
	RuntimeDir  string
	// Env 是 mtkit 已组装好、要注入 SUT 的环境变量：容器连接信息 + faked 连接信息。
	// 由 daemon 在容器 / faked 就绪后、SUT 启动前经 SetInjectEnv 填入。boot plan 只需把
	// 自己特有的 env 叠加在其上：cmd.Env = in.MergedEnv(extra)。
	Env map[string]string
}

// MergedEnv 返回 os.Environ() + in.Env + extra 合并后的 env 切片（后者覆盖前者）。
// boot plan 用它一行拿到"基础环境 + mtkit 注入 + SUT 特有"的完整 env，无需自己遍历。
func (in StartSUTInput) MergedEnv(extra map[string]string) []string {
	m := make(map[string]string, len(in.Env)+len(extra)+32)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	for k, v := range in.Env {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

type SUTBootPlan interface {
	Command(ctx context.Context, in StartSUTInput, binaryPath string) (*exec.Cmd, error)
	ProbeAddrs() []string
}

type Config struct {
	Enabled      bool
	BinaryPath   string
	SourceDir    string
	BuildArgs    []string
	BuildTimeout time.Duration
	ReadyTimeout time.Duration
	GracePeriod  time.Duration
	IdleTTL      time.Duration
}

type State = proc.State

type Manager struct {
	boot  SUTBootPlan
	cfg   Config
	input StartSUTInput
	p     *proc.Process
}

func NewManager(cfg Config, boot SUTBootPlan, input StartSUTInput) *Manager {
	m := &Manager{boot: boot, cfg: cfg, input: input}
	m.p = proc.New(proc.Spec{
		Name:    "sut",
		Enabled: cfg.Enabled,
		Build: proc.BuildSpec{
			BinaryPath: cfg.BinaryPath,
			SourceDir:  cfg.SourceDir,
			OutputName: defaultOutputName(input.Project),
			Args:       cfg.BuildArgs,
			Timeout:    cfg.BuildTimeout,
		},
		// 读 m.input（而非闭包捕获的 input 副本）：注入 env 由 daemon 在启动 SUT 前
		// 经 SetInjectEnv 填入，必须在 Command 执行时取最新值。
		Command: func(ctx context.Context, binaryPath string) (*exec.Cmd, error) {
			if m.boot == nil {
				return nil, errNilBootPlan
			}
			return m.boot.Command(ctx, m.input, binaryPath)
		},
		Ready:        proc.ReadyTCP(probeAddrs(boot)),
		ReadyTimeout: cfg.ReadyTimeout,
		GracePeriod:  cfg.GracePeriod,
		IdleTTL:      cfg.IdleTTL,
		LogPath:      mtruntime.AppLogPath(input.RuntimePath),
	})
	return m
}

// SetInjectEnv 在 SUT 启动前由 daemon 填入要注入的环境变量（容器 + faked 连接信息）。
// 调用时机在 acquire 串行流程内（容器 / faked 就绪之后、EnsureStarted 之前），无并发。
func (m *Manager) SetInjectEnv(env map[string]string) {
	m.input.Env = env
}

func (m *Manager) EnsureStarted(ctx context.Context) error {
	return m.p.EnsureStarted(ctx)
}

func (m *Manager) Stop(ctx context.Context) error {
	return m.p.Stop(ctx)
}

func (m *Manager) Snapshot() State {
	return m.p.Snapshot()
}

func (m *Manager) IdleTTL() time.Duration {
	if m == nil || m.p == nil {
		return 2 * time.Second
	}
	if ttl := m.p.IdleTTL(); ttl > 0 {
		return ttl
	}
	return 2 * time.Second
}

func probeAddrs(boot SUTBootPlan) []string {
	if boot == nil {
		return nil
	}
	return boot.ProbeAddrs()
}

func defaultOutputName(project string) string {
	name := strings.TrimSpace(project)
	if name == "" {
		name = "sut"
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(".mtkit-build", name)
}

type nilBootPlanError struct{}

func (nilBootPlanError) Error() string { return "sut boot plan is nil" }

var errNilBootPlan error = nilBootPlanError{}
