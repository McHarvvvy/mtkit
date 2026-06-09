// Package orchestrator 验证 daemon 的 lease 控制面契约（业务事实 F1/F3/F5/F6）：
// acquire 串联就绪、token 鉴权、容器回滚、SUT env 装配落盘、idle 自动回收。
//
// 形态：**进程内构造真实 daemon**——`orchestrator.New` + goroutine 跑 `Start`，经真实
// 端口打 lease HTTP API。容器用 fake 替身（见 fakeContainer），全程不创建真实容器。
//
// 经 mtorch.WithOrphanCleaner 注入 no-op 孤儿清理器：daemon 启动不再触发任何 Docker 调用，
// 本层完全 Docker 无关、确定性强。真实 orphan cleanup 由 e2e 层（真 facade）覆盖。
package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/McHarvvvy/mtkit/container"
	mtorch "github.com/McHarvvvy/mtkit/orchestrator"
	"github.com/McHarvvvy/mtkit/orchestrator/protocol"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
	"github.com/testcontainers/testcontainers-go"
)

const testToken = "orch-test-token"

// noopCleaner 是注入给 daemon 的 no-op 孤儿清理器。orchestrator 的编排逻辑与 Docker 无关，
// 注入它使本层进程内测试完全不触发任何 Docker 调用——这是去 Docker 依赖的关键。
type noopCleaner struct{}

func (noopCleaner) CleanupOrphans(context.Context, []container.ContainerRegistration) error {
	return nil
}

// fakeContainer 是不碰 Docker 的容器替身：只记录是否被 Terminate——回滚契约的观察面
// （"已起容器在回滚后被终止" = 这个 fake 的终态，而非调用次数）。
type fakeContainer struct {
	testcontainers.Container // 嵌入接口；除 Terminate 外的方法本层用例都不会触达
	mu         sync.Mutex
	terminated bool
}

func (c *fakeContainer) Terminate(_ context.Context, _ ...testcontainers.TerminateOption) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terminated = true
	return nil
}

func (c *fakeContainer) wasTerminated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminated
}

// startFake 返回一个 Start：成功返回给定 fakeContainer。
func startFake(c *fakeContainer) func(context.Context) (testcontainers.Container, error) {
	return func(context.Context) (testcontainers.Container, error) { return c, nil }
}

// startErrAfter 返回一个 Start：延迟 delay 后返回错误（延迟用于让并发启动的"好容器"先就位）。
func startErrAfter(delay time.Duration, err error) func(context.Context) (testcontainers.Container, error) {
	return func(context.Context) (testcontainers.Container, error) {
		time.Sleep(delay)
		return nil, err
	}
}

// harness 持有一个进程内运行的 daemon 及其对外句柄。
type harness struct {
	t           *testing.T
	addr        string
	token       string
	runtimePath string
	hc          *http.Client
	cancel      context.CancelFunc
	done        chan struct{}
}

// newHarness 起一个进程内 daemon。mutate 可改默认 Config（如缩短 IdleTTL）。
func newHarness(t *testing.T, regs []container.ContainerRegistration, mutate func(*mtorch.Config)) *harness {
	t.Helper()
	dir := t.TempDir()
	cfg := mtorch.Config{
		Addr:        "127.0.0.1:0",
		Token:       testToken,
		Project:     "orchtest",
		RuntimePath: filepath.Join(dir, "runtime.json"),
		RuntimeDir:  dir,
		IdleTTL:     30 * time.Second, // 默认足够长，避免测试期间被回收
	}
	if mutate != nil {
		mutate(&cfg)
	}

	d, err := mtorch.New(cfg, regs, mtorch.WithOrphanCleaner(noopCleaner{}))
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	startErrCh := make(chan error, 1)
	go func() {
		startErrCh <- d.Start(ctx)
		close(done)
	}()

	info, err := mtruntime.Wait(cfg.RuntimePath, 10*time.Second)
	if err != nil {
		cancel()
		// 暴露真实的 Start 错误（若已早退），而非笼统的"runtime 未就绪"——便于定位根因。
		select {
		case startErr := <-startErrCh:
			t.Fatalf("daemon 启动失败: %v（runtime 等待: %v）", startErr, err)
		default:
			t.Fatalf("daemon runtime 文件未就绪: %v", err)
		}
	}

	h := &harness{
		t:           t,
		addr:        info.Addr,
		token:       info.Token,
		runtimePath: cfg.RuntimePath,
		hc:          &http.Client{Timeout: 10 * time.Second},
		cancel:      cancel,
		done:        done,
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Log("daemon Start 在清理窗口内未返回")
		}
	})
	return h
}

// waitExit 等待 daemon 的 Start 协程返回（idle 自动退出）。
func (h *harness) waitExit(timeout time.Duration) bool {
	select {
	case <-h.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// doToken 用指定 token 发请求，返回响应与原始 body。
func (h *harness) doToken(method, path string, body any, token string) (*http.Response, []byte) {
	h.t.Helper()
	var rdr io.Reader
	switch v := body.(type) {
	case nil:
		// 无 body
	case string:
		// 原样发送（用于构造非法 JSON 等场景，绕过 json.Marshal 的校验）
		rdr = bytes.NewReader([]byte(v))
	case []byte:
		rdr = bytes.NewReader(v)
	default:
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://"+h.addr+path, rdr)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set(protocol.HeaderToken, token)
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		h.t.Fatalf("do %s %s: %v", method, path, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, raw
}

// do 用正确 token 发请求。
func (h *harness) do(method, path string, body any) (*http.Response, []byte) {
	return h.doToken(method, path, body, h.token)
}

// acquire 用正确 token 申请租约，返回 leaseID（失败 t.Fatal）。
func (h *harness) acquire() string {
	h.t.Helper()
	resp, raw := h.do(http.MethodPost, protocol.PathAcquire, protocol.AcquireReq{Project: "orchtest", PID: os.Getpid()})
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("acquire 期望 200，得 %d body=%s", resp.StatusCode, raw)
	}
	var ar protocol.AcquireResp
	if err := json.Unmarshal(raw, &ar); err != nil {
		h.t.Fatalf("decode acquire resp: %v", err)
	}
	if ar.LeaseID == "" {
		h.t.Fatal("acquire 返回空 lease_id")
	}
	return ar.LeaseID
}

// state 查询 daemon 状态。
func (h *harness) state() protocol.StateResp {
	h.t.Helper()
	resp, raw := h.do(http.MethodGet, protocol.PathState, nil)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("state 期望 200，得 %d body=%s", resp.StatusCode, raw)
	}
	var st protocol.StateResp
	if err := json.Unmarshal(raw, &st); err != nil {
		h.t.Fatalf("decode state: %v", err)
	}
	return st
}

// oneFakeReg 注册单个 fake 容器，返回注册项与句柄。
func oneFakeReg(name string) (container.ContainerRegistration, *fakeContainer) {
	c := &fakeContainer{}
	return container.ContainerRegistration{Name: name, Start: startFake(c)}, c
}

// heartbeatBeacon 在独立 goroutine 里按 < LeaseTTL 的间隔持续续租，直到 stop 关闭。
// 不调用 t.Fatal（goroutine 安全）：传输错误忽略。
func (h *harness) heartbeatBeacon(leaseID string, stop <-chan struct{}) {
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			b, _ := json.Marshal(protocol.HeartbeatReq{LeaseID: leaseID})
			req, err := http.NewRequest(http.MethodPost, "http://"+h.addr+protocol.PathHeartbeat, bytes.NewReader(b))
			if err != nil {
				continue
			}
			req.Header.Set(protocol.HeaderToken, h.token)
			if resp, err := h.hc.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
	}
}
