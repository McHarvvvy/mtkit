package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/McHarvvvy/mtkit/container"
	"github.com/McHarvvvy/mtkit/orchestrator/protocol"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
	"github.com/testcontainers/testcontainers-go"
)

// TestOR14_ConcurrentAcquireSerializesReadiness 验证并发 Acquire 下“环境就绪准备”被串行化（F1/F6）。
//
// 多个请求并发申请租约时：所有请求都拿到有效租约；容器 SUTEnv 组装与 runtime 落盘只发生一次；
// runtime 文件不被并发写坏。这是 readyMu 串行化的回归保护——旧实现里 faked/prepareSUTEnv 的共享
// 状态写入与同路径 runtime.Write 无锁并发，本用例在 -race 下会直接暴露数据竞态。
//
// 观察面只取对外可见事实（lease 响应 + runtime 文件 + SUTEnv 调用次数），不戳内部锁结构。
func TestOR14_ConcurrentAcquireSerializesReadiness(t *testing.T) {
	var sutEnvCalls atomic.Int32
	reg := container.ContainerRegistration{
		Name:  "redis",
		Start: startFake(&fakeContainer{}),
		SUTEnv: func(_ context.Context, _ testcontainers.Container) (map[string]string, error) {
			sutEnvCalls.Add(1)
			return map[string]string{"REDIS_ADDR": "127.0.0.1:6379"}, nil
		},
	}
	h := newHarness(t, []container.ContainerRegistration{reg}, nil)

	const n = 8
	type result struct {
		status  int
		leaseID string
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			// goroutine 内不调用 t.Fatal（非测试主协程不安全）：失败留给主协程据 results 断言。
			body, _ := json.Marshal(protocol.AcquireReq{Project: "orchtest", PID: 1000 + i})
			req, err := http.NewRequest(http.MethodPost, "http://"+h.addr+protocol.PathAcquire, bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set(protocol.HeaderToken, h.token)
			resp, err := h.hc.Do(req)
			if err != nil {
				return
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			var ar protocol.AcquireResp
			_ = json.Unmarshal(raw, &ar)
			results[i] = result{status: resp.StatusCode, leaseID: ar.LeaseID}
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		if r.status != http.StatusOK || r.leaseID == "" {
			t.Fatalf("并发 acquire[%d] 期望 200+非空 lease，得 status=%d lease=%q", i, r.status, r.leaseID)
		}
	}
	// 就绪准备幂等：容器 SUTEnv 组装恰好一次（prepareSUTEnv 经 readyMu + sutEnvReady 守护）。
	if got := sutEnvCalls.Load(); got != 1 {
		t.Fatalf("SUTEnv 组装期望恰好 1 次（并发就绪应串行幂等），实得 %d 次", got)
	}
	// runtime 文件未被并发写坏，sut_env 完整可读。
	info, err := mtruntime.Read(h.runtimePath)
	if err != nil {
		t.Fatalf("读 runtime 文件: %v", err)
	}
	if info.SUTEnv["REDIS_ADDR"] != "127.0.0.1:6379" {
		t.Fatalf("runtime sut_env 不完整: %v", info.SUTEnv)
	}
}
