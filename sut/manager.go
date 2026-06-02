package sut

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

// StartSUTInput 定义被测服务启动输入。
type StartSUTInput struct {
	// Project 表示当前测试项目名。
	Project string
	// RuntimePath 表示当前 daemon runtime 文件路径。
	RuntimePath string
}

// SUTBootPlan 定义被测服务启动与探测计划。
type SUTBootPlan interface {
	// IsEnable 返回是否启用 SUT 管理流程。
	IsEnable() bool
	// GetIdleTTL 返回无租约后 SUT 允许空闲存活时长。
	GetIdleTTL() time.Duration
	// GetReadyTimeout 返回 SUT 就绪探测超时时间。
	GetReadyTimeout() time.Duration
	// GetGracePeriod 返回停止 SUT 时的优雅退出等待时长。
	GetGracePeriod() time.Duration
	// GetCommand 返回已组装好的启动命令（需包含工作目录与必要环境变量）。
	GetCommand(ctx context.Context, in StartSUTInput) (*exec.Cmd, error)
	// GetProbeAddrs 返回就绪探测地址列表（ip:port）。
	// 返回空列表表示不需要端口探测，仅检查进程短时间内未立即退出。
	GetProbeAddrs() []string
}

type State struct {
	Ready        bool
	PID          int
	StartedAt    time.Time
	RestartCount int
	LastError    string
}

type Manager struct {
	boot SUTBootPlan
	mu   sync.Mutex

	// cmd / waitCh / startedAt / restartCount / lastErr 受同一把锁保护，
	// 目的是在 Acquire、Reaper、State 查询并发时保持视图一致。
	cmd          *exec.Cmd
	waitCh       chan error
	startedAt    time.Time
	restartCount int
	lastErr      string
}

func NewManager(boot SUTBootPlan) *Manager {
	return &Manager{boot: boot}
}

func (m *Manager) EnsureStarted(ctx context.Context, in StartSUTInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.isEnabled() {
		return nil
	}
	if m.isRunningLocked() {
		return nil
	}
	if m.boot == nil {
		m.lastErr = "sut boot plan is nil"
		return errors.New(m.lastErr)
	}

	cmd, err := m.boot.GetCommand(ctx, in)
	if err != nil {
		m.lastErr = err.Error()
		return err
	}
	if err = validateCommand(cmd); err != nil {
		m.lastErr = err.Error()
		return err
	}
	if err = prepareSUTCommand(cmd); err != nil {
		m.lastErr = err.Error()
		return err
	}
	if len(cmd.Env) == 0 {
		cmd.Env = os.Environ()
	}
	// 约束：SUT 日志必须落盘到 runtime artifact，便于失败后离线排查。
	logFile, err := mtruntime.OpenAppLogFile(in.RuntimePath)
	if err != nil {
		m.lastErr = err.Error()
		return err
	}
	mtruntime.AttachCmdOutput(cmd, logFile)
	if err = cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		m.lastErr = err.Error()
		return err
	}
	if err = afterSUTStart(cmd); err != nil {
		// afterSUTStart 失败通常意味着进程分组/Job 绑定失败，
		// 这类进程若不回收，后续重试会出现端口占用或孤儿进程。
		terminateProcess(cmd, nil, 2*time.Second)
		if logFile != nil {
			_ = logFile.Close()
		}
		m.lastErr = err.Error()
		return err
	}
	waitCh := make(chan error, 1)
	go func(localCmd *exec.Cmd, localWaitCh chan error, localLogFile *os.File) {
		// wait goroutine 负责统一收尸：
		// 1) 回收平台相关进程控制资源；2) 关闭日志文件；3) 回传退出结果。
		if localLogFile != nil {
			defer localLogFile.Close()
		}
		defer releaseSUTProcess(localCmd)
		localWaitCh <- localCmd.Wait()
		close(localWaitCh)
	}(cmd, waitCh, logFile)

	// 关键决策：SUT 就绪探测统一在框架层执行，
	// 保证“所有探测地址必须可达”的一致判定语义。
	if err = waitSUTReady(ctx, waitCh, m.boot.GetProbeAddrs(), m.readyTimeout()); err != nil {
		m.forceStopLocked(cmd, waitCh)
		m.lastErr = err.Error()
		return err
	}

	if !m.startedAt.IsZero() {
		m.restartCount++
	}
	m.cmd = cmd
	m.waitCh = waitCh
	m.startedAt = time.Now()
	m.lastErr = ""
	return nil
}

func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.isEnabled() {
		m.mu.Unlock()
		return nil
	}
	if !m.isRunningLocked() {
		m.cmd = nil
		m.waitCh = nil
		m.mu.Unlock()
		return nil
	}

	cmd := m.cmd
	waitCh := m.waitCh
	m.cmd = nil
	m.waitCh = nil
	m.mu.Unlock()
	// 关键决策：等待进程退出前先释放锁，避免阻塞 state/reaper 路径。

	if cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
	}
	select {
	case <-ctx.Done():
		terminateProcess(cmd, waitCh, 5*time.Second)
		m.mu.Lock()
		m.lastErr = ctx.Err().Error()
		m.mu.Unlock()
		return ctx.Err()
	case <-time.After(m.gracePeriod()):
		terminateProcess(cmd, waitCh, 5*time.Second)
		return nil
	case <-waitCh:
		return nil
	}
}

func (m *Manager) Snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := State{
		Ready:        m.isRunningLocked(),
		StartedAt:    m.startedAt,
		RestartCount: m.restartCount,
		LastError:    m.lastErr,
	}
	if m.cmd != nil && m.cmd.Process != nil {
		state.PID = m.cmd.Process.Pid
	}
	return state
}

func (m *Manager) isRunningLocked() bool {
	if m.cmd == nil || m.cmd.Process == nil || m.waitCh == nil {
		return false
	}
	select {
	case err, ok := <-m.waitCh:
		// 关键决策：这里消费 waitCh 后立即清空状态，
		// 避免后续调用者基于过期 cmd 继续判断为“运行中”。
		if ok && err != nil {
			m.lastErr = err.Error()
		}
		m.cmd = nil
		m.waitCh = nil
		return false
	default:
		return true
	}
}

func (m *Manager) forceStopLocked(cmd *exec.Cmd, waitCh <-chan error) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	terminateProcess(cmd, waitCh, 5*time.Second)
}

func terminateProcess(cmd *exec.Cmd, waitCh <-chan error, waitTimeout time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// 先走平台专用终止逻辑（Unix 进程组 / Windows JobObject），
	// 兜底保证子进程链一并退出。
	if err := terminateSUTProcess(cmd); err != nil {
		log.Printf("mtkit terminateProcess failed pid=%d err=%v", cmd.Process.Pid, err)
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

func validateCommand(cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("sut command is nil")
	}
	if strings.TrimSpace(cmd.Path) == "" {
		return errors.New("sut command path is required")
	}
	if strings.TrimSpace(cmd.Dir) == "" {
		return errors.New("sut workdir is required")
	}
	st, err := os.Stat(cmd.Dir)
	if err != nil {
		return fmt.Errorf("sut workdir stat failed: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("sut workdir is not directory: %s", cmd.Dir)
	}
	return nil
}

func waitSUTReady(ctx context.Context, waitCh <-chan error, probeAddrs []string, readyTimeout time.Duration) error {
	addrs := normalizeProbeAddrs(probeAddrs)
	if len(addrs) == 0 {
		// 关键决策：未配置探测地址时，不做端口探测，
		// 仅判断进程在短时间窗口内未立即退出即可视为启动成功。
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-waitCh:
			if !ok || err == nil {
				return errors.New("sut process exited before startup completed")
			}
			return fmt.Errorf("sut process exited before startup completed: %w", err)
		case <-time.After(400 * time.Millisecond):
			return nil
		}
	}

	if readyTimeout <= 0 {
		readyTimeout = 8 * time.Second
	}
	deadline := time.Now().Add(readyTimeout)
	for {
		// 关键决策：探测地址采用“全量成功”策略，任一地址失败即判定未就绪。
		failed := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
			if err != nil {
				failed = append(failed, addr)
				continue
			}
			_ = conn.Close()
		}
		if len(failed) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case werr, ok := <-waitCh:
			if !ok || werr == nil {
				return errors.New("sut process exited before ready")
			}
			return fmt.Errorf("sut process exited before ready: %w", werr)
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait sut ready timeout, failed addrs: %s", strings.Join(failed, ","))
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func normalizeProbeAddrs(addrs []string) []string {
	// 关键决策：探测地址标准化时直接丢弃非法输入，
	// 避免因为单个坏地址导致整个流程 panic。
	out := make([]string, 0, len(addrs))
	for _, raw := range addrs {
		addr := strings.TrimSpace(raw)
		if addr == "" {
			continue
		}
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		if strings.TrimSpace(host) == "" {
			host = "127.0.0.1"
		}
		if _, err = strconv.Atoi(port); err != nil {
			continue
		}
		out = append(out, net.JoinHostPort(host, port))
	}
	return out
}

func (m *Manager) IdleTTL() time.Duration {
	// 关键决策：IdleTTL 在计划未设置时给出安全默认值，
	// 防止 SUT 长时间不回收造成资源泄漏。
	if m.boot == nil {
		return 2 * time.Second
	}
	if ttl := m.boot.GetIdleTTL(); ttl > 0 {
		return ttl
	}
	return 2 * time.Second
}

func (m *Manager) gracePeriod() time.Duration {
	// 关键决策：优雅退出等待时长同样提供默认值，
	// 避免配置缺失导致立即强杀影响排障。
	if m.boot == nil {
		return 4 * time.Second
	}
	if gp := m.boot.GetGracePeriod(); gp > 0 {
		return gp
	}
	return 4 * time.Second
}

func (m *Manager) readyTimeout() time.Duration {
	if m.boot == nil {
		return 8 * time.Second
	}
	if v := m.boot.GetReadyTimeout(); v > 0 {
		return v
	}
	return 8 * time.Second
}

func (m *Manager) isEnabled() bool {
	if m.boot == nil {
		return false
	}
	return m.boot.IsEnable()
}
