package proc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

type BuildSpec struct {
	BinaryPath   string
	SourceDir    string
	OutputName   string
	Args         []string
	Timeout      time.Duration
	SkipIfBinary bool
}

type CommandFunc func(ctx context.Context, binaryPath string) (*exec.Cmd, error)

type ReadyFunc func(ctx context.Context, waitCh <-chan error, timeout time.Duration) error

type Spec struct {
	Name         string
	Enabled      bool
	Build        BuildSpec
	Command      CommandFunc
	Ready        ReadyFunc
	ReadyTimeout time.Duration
	GracePeriod  time.Duration
	IdleTTL      time.Duration
	LogPath      string
	Env          map[string]string
	// PreStart 仅在确实要启动一个新进程前调用（复用已运行进程时不调）。
	// 用于启动前的陈旧产物清理等副作用，避免在 EnsureStarted 入口无条件执行
	// 而误删正被复用进程的运行时文件。
	PreStart func(ctx context.Context) error
}

type State struct {
	Ready        bool
	PID          int
	StartedAt    time.Time
	RestartCount int
	LastError    string
}

type Process struct {
	spec Spec
	mu   sync.Mutex

	cmd          *exec.Cmd
	waitCh       chan error
	startedAt    time.Time
	restartCount int
	lastErr      string
}

func New(spec Spec) *Process {
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = "process"
	}
	return &Process{spec: spec}
}

func (p *Process) Name() string { return p.spec.Name }

func (p *Process) IdleTTL() time.Duration { return p.spec.IdleTTL }

func (p *Process) EnsureStarted(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.spec.Enabled {
		return nil
	}
	if p.isRunningLocked() {
		return nil
	}
	if p.spec.Command == nil {
		p.lastErr = fmt.Sprintf("%s command is nil", p.spec.Name)
		return errors.New(p.lastErr)
	}

	// 关键决策：PreStart 只在锁内、确认非复用（isRunningLocked 已返回 false）后调用，
	// 保证陈旧清理不会与正在复用的进程产生竞态、也不会误删存活进程的运行时文件。
	if p.spec.PreStart != nil {
		if err := p.spec.PreStart(ctx); err != nil {
			p.lastErr = err.Error()
			return err
		}
	}

	binaryPath, err := p.ensureBinary(ctx)
	if err != nil {
		p.lastErr = err.Error()
		return err
	}
	cmd, err := p.spec.Command(ctx, binaryPath)
	if err != nil {
		p.lastErr = err.Error()
		return err
	}
	if err = validateCommand(p.spec.Name, cmd); err != nil {
		p.lastErr = err.Error()
		return err
	}
	if err = PrepareCommand(cmd); err != nil {
		p.lastErr = err.Error()
		return err
	}
	if len(cmd.Env) == 0 {
		cmd.Env = os.Environ()
	}
	if len(p.spec.Env) > 0 {
		cmd.Env = mergeEnv(cmd.Env, p.spec.Env)
	}

	logFile, err := p.openLogFile()
	if err != nil {
		p.lastErr = err.Error()
		return err
	}
	mtruntime.AttachCmdOutput(cmd, logFile)
	if err = cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		p.lastErr = err.Error()
		return err
	}
	if err = AfterStart(cmd); err != nil {
		TerminateProcess(cmd, nil, 2*time.Second)
		if logFile != nil {
			_ = logFile.Close()
		}
		p.lastErr = err.Error()
		return err
	}
	waitCh := make(chan error, 1)
	go func(localCmd *exec.Cmd, localWaitCh chan error, localLogFile *os.File) {
		if localLogFile != nil {
			defer localLogFile.Close()
		}
		defer ReleaseProcess(localCmd)
		localWaitCh <- localCmd.Wait()
		close(localWaitCh)
	}(cmd, waitCh, logFile)

	ready := p.spec.Ready
	if ready == nil {
		ready = ReadyProcessStable(400 * time.Millisecond)
	}
	if err = ready(ctx, waitCh, defaultDuration(p.spec.ReadyTimeout, 15*time.Second)); err != nil {
		p.forceStopLocked(cmd, waitCh)
		p.lastErr = err.Error()
		return err
	}

	if !p.startedAt.IsZero() {
		p.restartCount++
	}
	p.cmd = cmd
	p.waitCh = waitCh
	p.startedAt = time.Now()
	p.lastErr = ""
	return nil
}

func (p *Process) Stop(ctx context.Context) error {
	p.mu.Lock()
	if !p.spec.Enabled {
		p.mu.Unlock()
		return nil
	}
	if !p.isRunningLocked() {
		p.cmd = nil
		p.waitCh = nil
		p.mu.Unlock()
		return nil
	}
	cmd := p.cmd
	waitCh := p.waitCh
	p.cmd = nil
	p.waitCh = nil
	p.mu.Unlock()

	if cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
	}
	select {
	case <-ctx.Done():
		TerminateProcess(cmd, waitCh, 5*time.Second)
		p.mu.Lock()
		p.lastErr = ctx.Err().Error()
		p.mu.Unlock()
		return ctx.Err()
	case <-time.After(defaultDuration(p.spec.GracePeriod, 5*time.Second)):
		TerminateProcess(cmd, waitCh, 5*time.Second)
		return nil
	case <-waitCh:
		return nil
	}
}

func (p *Process) Snapshot() State {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := State{
		Ready:        p.isRunningLocked(),
		StartedAt:    p.startedAt,
		RestartCount: p.restartCount,
		LastError:    p.lastErr,
	}
	if p.cmd != nil && p.cmd.Process != nil {
		state.PID = p.cmd.Process.Pid
	}
	return state
}

func (p *Process) ensureBinary(ctx context.Context) (string, error) {
	build := p.spec.Build
	if strings.TrimSpace(build.BinaryPath) != "" {
		return build.BinaryPath, nil
	}
	if strings.TrimSpace(build.SourceDir) == "" {
		return "", nil
	}
	if strings.TrimSpace(build.OutputName) == "" {
		return "", fmt.Errorf("%s build output name is required", p.spec.Name)
	}
	out := build.OutputName
	if !strings.ContainsAny(out, `/\`) {
		out = strings.TrimRight(build.SourceDir, `/\`) + string(os.PathSeparator) + out
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(build.SourceDir, out)
	}
	if build.SkipIfBinary {
		if st, err := os.Stat(out); err == nil && !st.IsDir() {
			return out, nil
		}
	}
	timeout := defaultDuration(build.Timeout, 60*time.Second)
	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := append([]string{"build", "-o", out}, build.Args...)
	if len(build.Args) == 0 || build.Args[len(build.Args)-1] != "." {
		args = append(args, ".")
	}
	cmd := exec.CommandContext(buildCtx, "go", args...)
	cmd.Dir = build.SourceDir
	outDir := filepath.Dir(out)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create build output dir for %s: %w", p.spec.Name, err)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("build %s failed: %w, output: %s", p.spec.Name, err, strings.TrimSpace(string(output)))
	}
	return out, nil
}

func (p *Process) openLogFile() (*os.File, error) {
	if strings.TrimSpace(p.spec.LogPath) == "" {
		return nil, nil
	}
	return mtruntime.OpenLogFile(p.spec.LogPath)
}

func (p *Process) isRunningLocked() bool {
	if p.cmd == nil || p.cmd.Process == nil || p.waitCh == nil {
		return false
	}
	select {
	case err, ok := <-p.waitCh:
		if ok && err != nil {
			p.lastErr = err.Error()
		}
		p.cmd = nil
		p.waitCh = nil
		return false
	default:
		return true
	}
}

func (p *Process) forceStopLocked(cmd *exec.Cmd, waitCh <-chan error) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	TerminateProcess(cmd, waitCh, 5*time.Second)
}

func TerminateProcess(cmd *exec.Cmd, waitCh <-chan error, waitTimeout time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := TerminateProcessTree(cmd); err != nil {
		log.Printf("mtkit terminate process failed pid=%d err=%v", cmd.Process.Pid, err)
	}
	if waitCh == nil {
		return
	}
	if waitTimeout <= 0 {
		waitTimeout = 5 * time.Second
	}
	select {
	case <-waitCh:
	case <-time.After(waitTimeout):
	}
}

func validateCommand(name string, cmd *exec.Cmd) error {
	if cmd == nil {
		return fmt.Errorf("%s command is nil", name)
	}
	if strings.TrimSpace(cmd.Path) == "" {
		return fmt.Errorf("%s command path is required", name)
	}
	if strings.TrimSpace(cmd.Dir) == "" {
		return fmt.Errorf("%s workdir is required", name)
	}
	st, err := os.Stat(cmd.Dir)
	if err != nil {
		return fmt.Errorf("%s workdir stat failed: %w", name, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("%s workdir is not directory: %s", name, cmd.Dir)
	}
	return nil
}

func defaultDuration(v, fallback time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return fallback
}

func mergeEnv(base []string, override map[string]string) []string {
	m := make(map[string]string, len(base)+len(override))
	for _, item := range base {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		m[parts[0]] = parts[1]
	}
	for k, v := range override {
		m[k] = v
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
