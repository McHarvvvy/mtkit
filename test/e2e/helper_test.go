//go:build e2e

// Package e2e 是 mtkit 的真实环境 smoke：真 Docker 容器 + re-exec daemon + 注入真 SUT。
//
// 统一执行模型：每个用例自驱 kit.Run(fakeRunnable)——闭包体在持租约窗口内观察活环境，
// 闭包返回后检查返回码与事后清理。daemon 与 client 每次合测（client 经 MTKIT_RUN_MODE=daemon
// re-exec 出同一 test 二进制做 daemon）。各用例 config 经 E2E_* 环境变量编码，re-exec 继承
// os.Environ() 使 client 与 daemon 子进程读到同一份（旧 MT_SCENARIO 的简化正确版）。
//
// 需 build tag e2e + 本地 Docker。blank-import echoendpoint 让 client 与 daemon 都注册 echo
// fake endpoint。
package e2e

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

	dockercontainer "github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	mtkit "github.com/McHarvvvy/mtkit"
	"github.com/McHarvvvy/mtkit/container"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"

	_ "github.com/McHarvvvy/mtkit/test/e2e/internal/echoendpoint" // 注册 echo fake endpoint（client + daemon）
)

// sutSrcDir 是 e2e SUT 源码目录的绝对路径（框架据此 go build）。由 TestMain 计算、经 env 传给 daemon。
var sutSrcDir string

func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e getwd: %v\n", err)
		os.Exit(1)
	}
	sutSrcDir = filepath.Join(wd, "internal", "sut")

	// re-exec daemon 子进程：构造与 client 完全一致的 kit（同一份 E2E_* env），进入 daemon 模式。
	if os.Getenv(runModeEnv) == "daemon" {
		kit, err := buildKitFromEnv()
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e daemon buildKit: %v\n", err)
			os.Exit(1)
		}
		os.Exit(kit.Run(noopRunnable{}))
	}

	os.Exit(m.Run())
}

// runModeEnv 与 mtkit/constant.EnvMTKitRunMode 对齐（脱离 import，硬编码字面量）。
const runModeEnv = "MTKIT_RUN_MODE"

// ─── 配置：client 与 daemon 共用的单一构造入口 ──────────────────────────────

func buildKitFromEnv() (*mtkit.MTKit, error) {
	idleSec, _ := strconv.Atoi(os.Getenv("E2E_IDLE_TTL_SEC"))
	if idleSec <= 0 {
		idleSec = 36
	}
	cfg := &mtkit.Config{
		RuntimeDir: os.Getenv("E2E_RUNTIME_DIR"),
		DaemonMode: mtkit.DaemonModeConfig{Addr: "127.0.0.1:0", IdleTTLSeconds: idleSec},
		// acquire 期间 daemon 可能现场构建 SUT，故 client HTTP 超时放宽，避免 acquire 提前超时。
		ClientMode: mtkit.ClientModeConfig{HTTPTimeoutSeconds: 180},
	}
	if os.Getenv("E2E_FAKED") == "1" {
		cfg.Faked.Enabled = true // 动态 CA、默认代理监听
	}
	sutEnabled := os.Getenv("E2E_SUT") == "1"
	if sutEnabled {
		cfg.SUT.Enabled = true
		cfg.SUT.SourceDir = os.Getenv("E2E_SUT_SRC")
		cfg.SUT.BuildArgs = []string{"-tags", "mtkit_it"} // 编入 CA addon 模板
		cfg.SUT.Lifecycle.BuildTimeoutSeconds = 180
		cfg.SUT.Lifecycle.ReadyTimeoutSeconds = 30
		cfg.SUT.Lifecycle.GracePeriodSeconds = 5
	}

	kit, err := mtkit.New("mtkit-e2e", cfg)
	if err != nil {
		return nil, err
	}
	kind := os.Getenv("E2E_CONTAINER")
	name := os.Getenv("E2E_CONTAINER_NAME")
	kit.RegisterContainers(func(context.Context) ([]container.ContainerRegistration, error) {
		return registerContainers(kind, name), nil
	})
	if sutEnabled {
		kit.RegisterSUT(e2eBootPlan{controlAddr: os.Getenv("E2E_SUT_CONTROL_ADDR")})
	}
	return kit, nil
}

// registerContainers 据 kind 返回容器注册：redis=真 Docker redis（带 REDIS_ADDR 注入）；其余=noop。
func registerContainers(kind, name string) []container.ContainerRegistration {
	if kind != "redis" {
		return []container.ContainerRegistration{
			{Name: name, Start: func(context.Context) (testcontainers.Container, error) { return nil, nil }},
		}
	}
	return []container.ContainerRegistration{
		{
			Name:  name,
			Start: startRedis(name),
			SUTEnv: func(ctx context.Context, c testcontainers.Container) (map[string]string, error) {
				host, err := c.Host(ctx)
				if err != nil {
					return nil, err
				}
				port, err := c.MappedPort(ctx, "6379/tcp")
				if err != nil {
					return nil, err
				}
				return map[string]string{"REDIS_ADDR": net.JoinHostPort(host, port.Port())}, nil
			},
		},
	}
}

func startRedis(name string) func(context.Context) (testcontainers.Container, error) {
	return func(ctx context.Context) (testcontainers.Container, error) {
		return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Name:         name,
				Image:        "redis:7-alpine",
				ExposedPorts: []string{"6379/tcp"},
				WaitingFor:   wait.ForListeningPort("6379/tcp"),
			},
			Started: true,
		})
	}
}

// ─── Runnable / BootPlan ────────────────────────────────────────────────────

type noopRunnable struct{}

func (noopRunnable) Run() int { return 0 }

type fakeRunnable struct{ fn func() int }

func (f fakeRunnable) Run() int { return f.fn() }

type e2eBootPlan struct{ controlAddr string }

func (p e2eBootPlan) Command(_ context.Context, in mtkit.StartSUTInput, binaryPath string) (*exec.Cmd, error) {
	cmd := exec.Command(binaryPath)
	cmd.Dir = os.TempDir()
	cmd.Env = in.MergedEnv(map[string]string{"SUT_CONTROL_ADDR": p.controlAddr})
	return cmd, nil
}

func (p e2eBootPlan) ProbeAddrs() []string { return []string{p.controlAddr} }

// ─── 环境装配 ───────────────────────────────────────────────────────────────

type envOpts struct {
	runtimeDir     string
	container      string // "redis" | "noop"
	containerName  string
	sut            bool
	faked          bool
	sutControlAddr string
	idleTTLSec     int
}

func setupEnv(t *testing.T, o envOpts) {
	t.Helper()
	set := func(k, v string) {
		_ = os.Setenv(k, v)
		t.Cleanup(func() { _ = os.Unsetenv(k) })
	}
	set("E2E_RUNTIME_DIR", o.runtimeDir)
	set("E2E_CONTAINER", o.container)
	set("E2E_CONTAINER_NAME", o.containerName)
	set("E2E_IDLE_TTL_SEC", strconv.Itoa(o.idleTTLSec))
	set("E2E_SUT_SRC", sutSrcDir)
	if o.sut {
		set("E2E_SUT", "1")
		set("E2E_SUT_CONTROL_ADDR", o.sutControlAddr)
	}
	if o.faked {
		set("E2E_FAKED", "1")
	}
	// 兜底：测试结束确保该 runtime 目录的 daemon 被终止，避免 lingering daemon。
	t.Cleanup(func() { killDaemon(o.runtimeDir) })
}

func mustBuildKit(t *testing.T) *mtkit.MTKit {
	t.Helper()
	kit, err := buildKitFromEnv()
	if err != nil {
		t.Fatalf("buildKitFromEnv: %v", err)
	}
	return kit
}

// ─── 运行时 / 进程 / Docker 工具 ────────────────────────────────────────────

func runtimeFile(dir string) string { return filepath.Join(dir, mtruntime.DefaultRuntimeFileName) }

func daemonPID(t *testing.T, dir string) int {
	t.Helper()
	info, err := mtruntime.Read(runtimeFile(dir))
	if err != nil {
		t.Fatalf("read runtime %s: %v", dir, err)
	}
	return info.PID
}

// requireDocker 在 Docker 不可达时 fail（非 skip）。e2e 经 -tags e2e 显式 opt-in，
// 运行即明确要求真 Docker；缺失是环境故障，应红而非 skip——skip 会假绿、致 e2e 静默腐烂。
func requireDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("e2e requires Docker (client): %v", err)
	}
	defer cli.Close()
	if _, err = cli.Ping(ctx); err != nil {
		t.Fatalf("e2e requires Docker (daemon): %v", err)
	}
}

func dockerInspectID(name string) (string, bool) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", false
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	insp, err := cli.ContainerInspect(ctx, name)
	if err != nil {
		return "", false
	}
	return insp.ID, true
}

func containerIDByName(t *testing.T, name string) string {
	t.Helper()
	id, ok := dockerInspectID(name)
	if !ok {
		t.Fatalf("container %s not found", name)
	}
	return id
}

func containerExists(name string) bool {
	_, ok := dockerInspectID(name)
	return ok
}

func removeContainer(name string) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = cli.ContainerRemove(ctx, name, dockercontainer.RemoveOptions{Force: true, RemoveVolumes: true})
}

// cleanup 终止 daemon、移除容器与 runtime 产物（兜底，避免跨用例残留）。
func cleanup(name, dir string) {
	killDaemon(dir)
	removeContainer(name)
	_ = os.Remove(runtimeFile(dir))
	_ = os.RemoveAll(mtruntime.RunnerDir(runtimeFile(dir)))
}

func killDaemon(dir string) {
	info, err := mtruntime.Read(runtimeFile(dir))
	if err != nil || info.PID <= 0 {
		return
	}
	killProcess(info.PID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(info.PID) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func killProcess(pid int) {
	if pid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(syscall.SIGTERM)
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

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
