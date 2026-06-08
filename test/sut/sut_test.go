// Package sut 验证 SUT 进程管理契约（业务事实 F1/F4/F5/F6），边界打在 sut.Manager
// （SUT 语义的稳定边界、orchestrator 实际消费的对象），用例同时兼测使用者实现的
// SUTBootPlan 被正确驱动。被测进程为真子进程（internal/suthelper 编译出的 helper 二进制）。
package sut

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	mtsut "github.com/McHarvvvy/mtkit/sut"
)

// 由 TestMain 装配：预构建的 helper 二进制 + helper 源码目录（SM-07 用框架构建）。
var (
	helperBin    string
	helperSrcDir string
)

func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	helperSrcDir = filepath.Join(wd, "internal", "suthelper")

	tmp, err := os.MkdirTemp("", "suthelper-bin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mktemp: %v\n", err)
		os.Exit(1)
	}
	helperBin = filepath.Join(tmp, "suthelper"+exeSuffix())
	build := exec.Command("go", "build", "-o", helperBin, "./internal/suthelper")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build suthelper: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// ─── 测试用 SUTBootPlan ─────────────────────────────────────────────────────

// bootPlan 把 helper 二进制包成 SUTBootPlan：knobs 是 helper 的行为旋钮，
// 经 in.MergedEnv 与 mtkit 注入 env 合并后落到子进程。
type bootPlan struct {
	binary string
	knobs  map[string]string
	probes []string
}

func (p bootPlan) Command(_ context.Context, in mtsut.StartSUTInput, binaryPath string) (*exec.Cmd, error) {
	path := binaryPath // sut.Manager 传入：BinaryPath 或框架构建产物
	if path == "" {
		path = p.binary
	}
	cmd := exec.Command(path)
	cmd.Dir = os.TempDir()
	cmd.Env = in.MergedEnv(p.knobs)
	return cmd, nil
}

func (p bootPlan) ProbeAddrs() []string { return p.probes }

// ─── 辅助 ─────────────────────────────────────────────────────────────────

func baseCfg() mtsut.Config {
	return mtsut.Config{
		Enabled:      true,
		BinaryPath:   helperBin,
		ReadyTimeout: 10 * time.Second,
		GracePeriod:  3 * time.Second,
		IdleTTL:      2 * time.Second,
	}
}

func newManager(t *testing.T, cfg mtsut.Config, boot mtsut.SUTBootPlan) *mtsut.Manager {
	t.Helper()
	dir := t.TempDir()
	input := mtsut.StartSUTInput{
		Project:     "suttest",
		RuntimePath: filepath.Join(dir, "runtime.json"),
		RuntimeDir:  dir,
	}
	mgr := mtsut.NewManager(cfg, boot, input)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Stop(ctx)
	})
	return mgr
}

// ─── SM-01 稳定就绪 ─────────────────────────────────────────────────────────

func TestSM01_StableReady(t *testing.T) {
	mgr := newManager(t, baseCfg(), bootPlan{binary: helperBin, knobs: map[string]string{"SUTHELPER_MODE": "stable"}})
	if err := mgr.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	st := mgr.Snapshot()
	if !st.Ready {
		t.Fatal("常驻进程应就绪")
	}
	if st.PID <= 0 {
		t.Fatalf("就绪后 PID 应 >0，得 %d", st.PID)
	}
}

// ─── SM-02 TCP 探活门控 ─────────────────────────────────────────────────────

func TestSM02_TCPProbeGating(t *testing.T) {
	addr := freePort(t)
	boot := bootPlan{
		binary: helperBin,
		probes: []string{addr},
		knobs:  map[string]string{"SUTHELPER_MODE": "listen", "SUTHELPER_ADDR": addr, "SUTHELPER_DELAY_MS": "300"},
	}
	mgr := newManager(t, baseCfg(), boot)
	if err := mgr.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	// 就绪返回后探测端口必可连（而非进程一起来就返回）。
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("就绪后探测端口应可连：%v", err)
	}
	_ = conn.Close()
}

// ─── SM-03 启动失败 ─────────────────────────────────────────────────────────

func TestSM03_StartFailure(t *testing.T) {
	boot := bootPlan{binary: helperBin, knobs: map[string]string{"SUTHELPER_MODE": "exit", "SUTHELPER_EXIT_CODE": "1"}}
	mgr := newManager(t, baseCfg(), boot)
	err := mgr.EnsureStarted(context.Background())
	if err == nil {
		t.Fatal("进程立即退出，EnsureStarted 应报错")
	}
	st := mgr.Snapshot()
	if st.Ready {
		t.Fatal("失败后不应就绪")
	}
	if st.LastError == "" {
		t.Fatal("失败后 LastError 应非空")
	}
}

// ─── SM-04 注入 env 到达进程 ────────────────────────────────────────────────

func TestSM04_InjectedEnvReachesProcess(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "env-out.txt")
	boot := bootPlan{
		binary: helperBin,
		knobs: map[string]string{
			"SUTHELPER_MODE":     "writeenv",
			"SUTHELPER_READ_KEY": "INJECTED_KEY",
			"SUTHELPER_OUT_FILE": outFile,
		},
	}
	mgr := newManager(t, baseCfg(), boot)
	// daemon prepareSUTEnv → SetInjectEnv 的真实注入路径。
	mgr.SetInjectEnv(map[string]string{"INJECTED_KEY": "injected-value"})
	if err := mgr.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	if got := pollFile(outFile, 3*time.Second); got != "injected-value" {
		t.Fatalf("注入 env 应到达进程，helper 读回写入文件=%q", got)
	}
}

// ─── SM-05 复用幂等 ─────────────────────────────────────────────────────────

func TestSM05_ReuseIdempotent(t *testing.T) {
	mgr := newManager(t, baseCfg(), bootPlan{binary: helperBin, knobs: map[string]string{"SUTHELPER_MODE": "stable"}})
	ctx := context.Background()
	if err := mgr.EnsureStarted(ctx); err != nil {
		t.Fatalf("first EnsureStarted: %v", err)
	}
	pid1 := mgr.Snapshot().PID
	if err := mgr.EnsureStarted(ctx); err != nil {
		t.Fatalf("second EnsureStarted: %v", err)
	}
	pid2 := mgr.Snapshot().PID
	if pid1 != pid2 {
		t.Fatalf("复用应保持同一进程：pid1=%d pid2=%d", pid1, pid2)
	}
}

// ─── SM-06 优雅停止 ─────────────────────────────────────────────────────────

func TestSM06_GracefulStop(t *testing.T) {
	mgr := newManager(t, baseCfg(), bootPlan{binary: helperBin, knobs: map[string]string{"SUTHELPER_MODE": "stable"}})
	ctx := context.Background()
	if err := mgr.EnsureStarted(ctx); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	pid := mgr.Snapshot().PID
	if err := mgr.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stillAlive := waitProcessGone(pid, 5*time.Second); stillAlive {
		t.Fatalf("停止后进程 %d 仍存活", pid)
	}
}

// ─── SM-07 源码构建启动 ─────────────────────────────────────────────────────

func TestSM07_BuildFromSource(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "built-marker.txt")
	cfg := baseCfg()
	cfg.BinaryPath = ""          // 强制走源码构建
	cfg.SourceDir = helperSrcDir // 框架 go build
	cfg.BuildTimeout = 90 * time.Second
	// 框架构建产物落在 <SourceDir>/.mtkit-build，测试后清理。
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(helperSrcDir, ".mtkit-build")) })

	boot := bootPlan{
		knobs: map[string]string{
			"SUTHELPER_MODE":     "writeenv",
			"SUTHELPER_READ_KEY": "INJECTED_KEY",
			"SUTHELPER_OUT_FILE": outFile,
		},
	}
	mgr := newManager(t, cfg, boot)
	mgr.SetInjectEnv(map[string]string{"INJECTED_KEY": "built-and-ran"})
	if err := mgr.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted(源码构建): %v", err)
	}
	if got := pollFile(outFile, 3*time.Second); got != "built-and-ran" {
		t.Fatalf("框架构建的二进制应运行并写标记，得=%q", got)
	}
}

// ─── 工具 ─────────────────────────────────────────────────────────────────

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func pollFile(path string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b))
		}
		if time.Now().After(deadline) {
			return ""
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitProcessGone 在 timeout 内轮询进程是否消失；返回 true 表示超时仍存活。
func waitProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !processAlive(pid) {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
		if err != nil {
			return false
		}
		return strings.Contains(string(out), strconv.Itoa(pid))
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
