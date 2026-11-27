package fixture

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPersistEnvMissingKeyPanics 守护 PersistEnv 的 fail-loud 契约：取一个未注册（值缺失）的
// sutEnvKey 时，每个后端访问器都应 panic。这是纯进程内行为（mustValue 在任何 I/O 之前就 panic），
// 不需要真实容器，故留在进程内单测——把它从 e2e 里剥离，免得用真容器的慢成本去养与容器无关的逻辑。
func TestPersistEnvMissingKeyPanics(t *testing.T) {
	env := NewPersistEnv(nil)
	cases := map[string]func(){
		"Redis":        func() { env.Redis("MISSING") },
		"MySQL":        func() { env.MySQL("MISSING") },
		"Mongo":        func() { env.Mongo("MISSING") },
		"TDengine":     func() { env.TDengine("MISSING") },
		"PulsarClient": func() { env.PulsarClient("MISSING") },
		"PulsarAdmin":  func() { env.PulsarAdmin("MISSING") },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("取未注册的 %s key 应 fail-loud panic，但没有", name)
				}
			}()
			fn()
		})
	}
}

// TestPingWithRetrySucceedsAfterTransientFailure 验证首次连接短暂失败后会在超时前继续重试。
func TestPingWithRetrySucceedsAfterTransientFailure(t *testing.T) {
	attempts := 0
	err := pingWithRetry(context.Background(), func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("not ready")
		}
		return nil
	}, time.Millisecond, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("期望重试后成功，实际返回错误: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("期望尝试 3 次，实际 %d 次", attempts)
	}
}

// TestPingWithRetryReturnsLastErrorAfterTimeout 验证持续失败时在总超时后返回最后一次错误。
func TestPingWithRetryReturnsLastErrorAfterTimeout(t *testing.T) {
	sentinel := errors.New("still not ready")
	err := pingWithRetry(context.Background(), func(context.Context) error {
		return sentinel
	}, 2*time.Millisecond, 25*time.Millisecond)
	if !errors.Is(err, sentinel) {
		t.Fatalf("期望返回最后一次错误 %v，实际 %v", sentinel, err)
	}
}

// TestPingWithRetryStopsOnContextCancellation 验证调用方取消上下文后不再继续重试。
func TestPingWithRetryStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := pingWithRetry(ctx, func(ctx context.Context) error {
		return ctx.Err()
	}, time.Millisecond, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望返回 context.Canceled，实际 %v", err)
	}
}
