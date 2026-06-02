package client

import (
	"context"
	"log"
	"time"
)

// Runnable 表示可执行的测试入口。*testing.M 天然满足此接口。
type Runnable interface {
	Run() int
}

// RunUnderLease 在 client 模式下连接 daemon、持租约运行 m，并在结束后释放租约。
//
// 它封装 re-exec client-mode 的完整生命周期（发现/自启 daemon → Acquire → 心跳 → m.Run → Release），
// 供 orchestrator 的模式分流调用，使分流层保持极薄。
func RunUnderLease(ctx context.Context, cfg Config, m Runnable) int {
	c, err := New(ctx, cfg)
	if err != nil {
		log.Printf("mtkit client.New failed: %v", err)
		return 1
	}

	lease, err := c.Acquire(ctx)
	if err != nil {
		log.Printf("mtkit acquire failed: %v", err)
		return 1
	}
	stopHB := c.StartHeartbeat(lease.LeaseID)

	code := m.Run()
	// 关键决策：先停止心跳再释放租约，避免 release 阻塞期间继续续租导致 SUT 迟迟不回收。
	stopHB()
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Release(releaseCtx, lease.LeaseID, code)
	return code
}
