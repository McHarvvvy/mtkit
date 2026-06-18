package orchestrator

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/McHarvvvy/mtkit/container"
	mtorch "github.com/McHarvvvy/mtkit/orchestrator"
	"github.com/McHarvvvy/mtkit/orchestrator/protocol"
)

// TestOR12_IdleAutoExit 验证租约全释放且空闲超 IdleTTL 后 daemon 自动退出（F5）。
//
// Given daemon IdleTTL=1s；When acquire 后 release 并等待；
// Then daemon 的 Start 协程返回；runtime 文件刻意保留（不在退出时删除）——供测试结束后查阅，
// 正确性由下次启动的发现逻辑兜底（探活失败即删除重写）。
func TestOR12_IdleAutoExit(t *testing.T) {
	reg, _ := oneFakeReg("redis")
	h := newHarness(t, []container.ContainerRegistration{reg}, func(c *mtorch.Config) {
		c.IdleTTL = 1 * time.Second
	})

	leaseID := h.acquire()
	resp, raw := h.do(http.MethodPost, protocol.PathRelease, protocol.ReleaseReq{LeaseID: leaseID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("release 期望 200，得 %d body=%s", resp.StatusCode, raw)
	}

	// reaper tick=2s + IdleTTL=1s，约数个 tick 后退出；给足容忍窗口。
	if !h.waitExit(15 * time.Second) {
		t.Fatal("空闲 daemon 应在 IdleTTL 后自动退出")
	}
	if _, err := os.Stat(h.runtimePath); err != nil {
		t.Fatalf("退出后 runtime 文件应保留（不在退出时删除），os.Stat err=%v", err)
	}
}

// TestOR13_ReaperSuppressedWhileLeased 验证持续续租期间 daemon 不被回收（F5 边界）。
//
// Given daemon IdleTTL=1s；When 持有一个 lease 并持续心跳；
// Then 数倍 IdleTTL 时间内 daemon 不退出、仍可服务。
func TestOR13_ReaperSuppressedWhileLeased(t *testing.T) {
	reg, _ := oneFakeReg("redis")
	h := newHarness(t, []container.ContainerRegistration{reg}, func(c *mtorch.Config) {
		c.IdleTTL = 1 * time.Second
	})

	leaseID := h.acquire()
	stop := make(chan struct{})
	go h.heartbeatBeacon(leaseID, stop)
	defer close(stop)

	if h.waitExit(5 * time.Second) {
		t.Fatal("持租约期间 daemon 不应退出")
	}
	// 仍可服务：state 正常且活跃租约 ≥ 1。
	if st := h.state(); st.ActiveLeases < 1 {
		t.Fatalf("持租约期间 ActiveLeases 应 ≥ 1，得 %d", st.ActiveLeases)
	}
}
