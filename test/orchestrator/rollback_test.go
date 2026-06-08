package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/McHarvvvy/mtkit/container"
	"github.com/McHarvvvy/mtkit/orchestrator/protocol"
	"github.com/testcontainers/testcontainers-go"
)

// TestOR07_RollbackOnStartFailure 验证任一容器启动失败时回滚已起容器（F3）。
//
// Given 两容器：好容器立即成功、坏容器延迟后返回 error；
// When acquire；Then 返回 5xx；已起的好容器被终止；state.InfraReady=false。
func TestOR07_RollbackOnStartFailure(t *testing.T) {
	good := &fakeContainer{}
	regs := []container.ContainerRegistration{
		{Name: "good", Start: startFake(good)},
		// 坏容器延迟返回错误，确保并发启动时好容器先就位、从而进入回滚终止路径。
		{Name: "bad", Start: startErrAfter(60*time.Millisecond, errors.New("boom"))},
	}
	h := newHarness(t, regs, nil)

	resp, raw := h.do(http.MethodPost, protocol.PathAcquire, protocol.AcquireReq{PID: 1})
	if resp.StatusCode < 500 {
		t.Fatalf("启动失败 acquire 期望 5xx，得 %d body=%s", resp.StatusCode, raw)
	}
	if !good.wasTerminated() {
		t.Fatal("已起的好容器应在回滚中被终止")
	}
	if h.state().InfraReady {
		t.Fatal("回滚后 state.InfraReady 应为 false")
	}
}

// TestOR08_RollbackOnInitFailure 验证 Init 失败时回滚全部已起容器（F3）。
//
// Given 两容器都成功启动，其一 Init 返回 error；
// When acquire；Then 返回 5xx；两容器均被终止；state.InfraReady=false。
func TestOR08_RollbackOnInitFailure(t *testing.T) {
	a := &fakeContainer{}
	b := &fakeContainer{}
	regs := []container.ContainerRegistration{
		{Name: "a", Start: startFake(a)},
		{
			Name:  "b",
			Start: startFake(b),
			Init:  func(context.Context) error { return errors.New("seed failed") },
		},
	}
	h := newHarness(t, regs, nil)

	resp, raw := h.do(http.MethodPost, protocol.PathAcquire, protocol.AcquireReq{PID: 1})
	if resp.StatusCode < 500 {
		t.Fatalf("Init 失败 acquire 期望 5xx，得 %d body=%s", resp.StatusCode, raw)
	}
	if !a.wasTerminated() || !b.wasTerminated() {
		t.Fatalf("Init 失败应回滚全部容器：a.terminated=%v b.terminated=%v", a.wasTerminated(), b.wasTerminated())
	}
	if h.state().InfraReady {
		t.Fatal("回滚后 state.InfraReady 应为 false")
	}
}

// staticSUTEnv 返回一个不触达容器方法的 SUTEnv 回调（直接返回给定 env）。
func staticSUTEnv(env map[string]string) func(context.Context, testcontainers.Container) (map[string]string, error) {
	return func(context.Context, testcontainers.Container) (map[string]string, error) { return env, nil }
}
