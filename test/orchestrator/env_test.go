package orchestrator

import (
	"net/http"
	"testing"

	"github.com/McHarvvvy/mtkit/container"
	"github.com/McHarvvvy/mtkit/orchestrator/protocol"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

// TestOR09_SUTEnvAssembledAndPersisted 验证容器 SUTEnv 回调产出的连接 env 被组装并落盘（F6）。
//
// 观察面：runtime 文件的 sut_env 段（发布产物的反向读）——不戳内部装配过程。
func TestOR09_SUTEnvAssembledAndPersisted(t *testing.T) {
	c := &fakeContainer{}
	reg := container.ContainerRegistration{
		Name:   "redis",
		Start:  startFake(c),
		SUTEnv: staticSUTEnv(map[string]string{"REDIS_ADDR": "127.0.0.1:6379"}),
	}
	h := newHarness(t, []container.ContainerRegistration{reg}, nil)

	_ = h.acquire()

	info, err := mtruntime.Read(h.runtimePath)
	if err != nil {
		t.Fatalf("读 runtime 文件: %v", err)
	}
	if got := info.SUTEnv["REDIS_ADDR"]; got != "127.0.0.1:6379" {
		t.Fatalf("runtime sut_env[REDIS_ADDR] = %q，期望 127.0.0.1:6379（全量 sut_env=%v）", got, info.SUTEnv)
	}
}

// TestOR10_SUTEnvCollisionRejected 验证两容器贡献同名 env key 时拒绝（F6，不静默覆盖）。
func TestOR10_SUTEnvCollisionRejected(t *testing.T) {
	c1, c2 := &fakeContainer{}, &fakeContainer{}
	regs := []container.ContainerRegistration{
		{Name: "c1", Start: startFake(c1), SUTEnv: staticSUTEnv(map[string]string{"DUP": "a"})},
		{Name: "c2", Start: startFake(c2), SUTEnv: staticSUTEnv(map[string]string{"DUP": "b"})},
	}
	h := newHarness(t, regs, nil)

	resp, raw := h.do(http.MethodPost, protocol.PathAcquire, protocol.AcquireReq{PID: 1})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("env key 冲突 acquire 期望 500，得 %d body=%s", resp.StatusCode, raw)
	}
}
