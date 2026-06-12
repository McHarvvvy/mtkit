package fixture

import (
	"context"
	"fmt"
)

// Registry 提供跨进程“幂等执行一次”的保证。v1 实现为文本账本（见 FileRegistry）。
type Registry interface {
	// EnsureOnce 在互斥保护下幂等执行 fn。
	//   skipped=true：key 已注册，fn 未执行；
	//   err!=nil：fn 失败或基建故障，key 不被标记，下次可重入；
	//   ctx 控制等锁超时，fn 内部应自行处理自身超时。
	// skipped 刻意不走 error 通道：幂等跳过是正常路径，用哨兵错误会迫使所有调用方过滤“非错误的错误”，易漏判。
	EnsureOnce(ctx context.Context, key string, fn func() error) (skipped bool, err error)
}

// Provision 遍历 resources，对每个资源调 EnsureOnce，保证同一 daemon 生命周期内同一 key 只 Apply 一次（跨进程安全）。
// 失败返回带 key 的错误，由调用方（TestMain 接入处）log.Fatal 终止——schema 不就绪则测试无意义。
func Provision(ctx context.Context, reg Registry, env *PersistEnv, resources []Resource) error {
	for _, r := range resources {
		if r == nil {
			return fmt.Errorf("provision nil resource")
		}
		key := r.Key()
		if key == "" {
			return fmt.Errorf("provision resource has empty key")
		}
		if _, err := reg.EnsureOnce(ctx, key, func() error {
			return r.Apply(ctx, env)
		}); err != nil {
			return fmt.Errorf("provision %s: %w", key, err)
		}
	}
	return nil
}
