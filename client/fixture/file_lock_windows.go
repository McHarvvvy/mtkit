//go:build windows

package fixture

import (
	"context"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockFile 以 LockFileEx 获取整文件排他锁，拿不到则每 50ms 轮询重试，直到成功或 ctx 取消。
// 用 FAIL_IMMEDIATELY + 轮询而非阻塞锁，是为了让 ctx 超时/取消能及时生效。
func lockFile(ctx context.Context, f *os.File) (func(), error) {
	var overlapped windows.Overlapped
	const flags = windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY
	for {
		err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &overlapped)
		if err == nil {
			return func() {
				_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
			}, nil
		}
		if err != windows.ERROR_LOCK_VIOLATION {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
