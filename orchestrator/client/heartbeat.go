package client

import (
	"context"
	"time"

	mtconstant "github.com/McHarvvvy/mtkit/constant"
)

// StartHeartbeat 启动租约保活心跳，并返回停止函数。
func (c *Client) StartHeartbeat(leaseID string) func() {
	ctx, cancel := context.WithCancel(context.Background())
	interval := mtconstant.HeartbeatInterval
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// 关键决策：心跳失败不立即中断测试流程，租约过期由服务端兜底回收。
				_ = c.Heartbeat(context.Background(), leaseID)
			}
		}
	}()
	return cancel
}
