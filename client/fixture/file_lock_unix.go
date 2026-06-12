//go:build !windows

package fixture

import (
	"context"
	"os"
	"syscall"
	"time"
)

// lockFile 以 flock 获取整文件排他锁，拿不到则每 50ms 轮询重试，直到成功或 ctx 取消。
// 用非阻塞 LOCK_NB + 轮询而非阻塞 flock，是为了让 ctx 超时/取消能及时生效（阻塞 flock 不响应 ctx）。
func lockFile(ctx context.Context, f *os.File) (func(), error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
