package fixture_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fixture "github.com/McHarvvvy/mtkit/client/fixture"
	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

// countLedgerKey 反向读账本这一对外产物，统计某 key 出现次数。账本格式（protocol.Ledger）是 daemon
// 与 client 共享的 wire 契约，按它断言不算咬内部实现。
func countLedgerKey(t *testing.T, path, key string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var l protocol.Ledger
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatalf("decode ledger: %v", err)
	}
	c := 0
	for _, k := range l.Applied {
		if k == key {
			c++
		}
	}
	return c
}

// TestEnsureOnceConcurrentRunsOnce 守护 provision 的核心契约（C1）：同一 key 在并发争抢下也**只执行一次**。
//
// 验证手段：16 个 goroutine 各自 NewFileRegistry(同路径)——故各持独立 fd，对同一文件触发**真实** OS 锁
// （flock / LockFileEx）争用——并发 EnsureOnce(同 key)。断言：fn 计数恰为 1、所有调用都成功返回、
// 且反向读账本该 key 恰一条。若文件锁未真正互斥，会出现 fn 多跑或账本重复，被当场抓住。配 -race 跑。
func TestEnsureOnceConcurrentRunsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), protocol.LedgerFileName)
	const key = "mysql.test.users"
	const n = 16

	var runs int64
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reg := fixture.NewFileRegistry(path)
			_, err := reg.EnsureOnce(context.Background(), key, func() error {
				atomic.AddInt64(&runs, 1)
				return nil
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d EnsureOnce 失败: %v", i, err)
		}
	}
	if runs != 1 {
		t.Fatalf("fn 执行了 %d 次, want 1（互斥被破坏）", runs)
	}
	if got := countLedgerKey(t, path, key); got != 1 {
		t.Fatalf("账本中 key 出现 %d 次, want 1", got)
	}
}

// TestEnsureOnceLockWaitTimeout 守护"等锁超时则不执行、不标记"契约（C4）：
// 抢不到锁时，到截止时间应原样退出，绝不在未持锁的情况下执行 fn 或污染账本。
//
// 验证手段：一个 goroutine 持锁并阻塞在 fn 内（模拟一次很慢的 Apply），另一线程用 300ms deadline 的
// ctx 调 EnsureOnce(另一个 key)。断言：返回错误且可识别为 DeadlineExceeded（用 errors.Is，不咬文案）、
// fn 从未执行、该 key 未进账本。
func TestEnsureOnceLockWaitTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), protocol.LedgerFileName)

	holding := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		reg := fixture.NewFileRegistry(path)
		_, _ = reg.EnsureOnce(context.Background(), "holder", func() error {
			close(holding) // 已持锁，进入 fn
			<-release      // 持锁不放，直到测试放行
			return nil
		})
	}()
	<-holding

	reg := fixture.NewFileRegistry(path)
	var ran bool
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := reg.EnsureOnce(ctx, "blocked-key", func() error {
		ran = true
		return nil
	})
	if err == nil {
		t.Fatal("抢不到锁却返回 nil error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want 可识别为 context.DeadlineExceeded", err)
	}
	if ran {
		t.Fatal("等锁超时却执行了 fn")
	}

	close(release)
	<-done
	if got := countLedgerKey(t, path, "blocked-key"); got != 0 {
		t.Fatalf("超时的 key 被标记了 %d 次, want 0", got)
	}
}

// TestEnsureOnceCrossProcessRunsOnce 是 C1 的**真实环境 smoke**：文件锁的全部存在理由就是跨进程安全，
// 这里用真实多进程验证"同一 key 跨进程也只执行一次"。
//
// 验证手段：父进程 re-exec 本测试二进制起 5 个子进程（经 env 进入 child 分支），各自对同一账本
// EnsureOnce(同 key)，fn 成功时向共享 apply-log 以 O_APPEND 追加一行。断言 apply-log 全局恰一行——
// 若跨进程互斥失效，会出现多行。这是进程内并发用例（同地址空间）覆盖不到的真实跨进程一面。
func TestEnsureOnceCrossProcessRunsOnce(t *testing.T) {
	// child 分支：被父进程经 env re-exec 时进入，只做一次 EnsureOnce 后退出。
	if ledger := os.Getenv("FIXTURE_XPROC_LEDGER"); ledger != "" {
		applyLog := os.Getenv("FIXTURE_XPROC_APPLYLOG")
		reg := fixture.NewFileRegistry(ledger)
		_, err := reg.EnsureOnce(context.Background(), "mysql.xproc.users", func() error {
			f, e := os.OpenFile(applyLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if e != nil {
				return e
			}
			defer f.Close()
			_, e = f.WriteString("applied\n")
			return e
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "child EnsureOnce:", err)
			os.Exit(3)
		}
		return
	}

	// parent 分支：起多个子进程争同一账本。
	dir := t.TempDir()
	ledger := filepath.Join(dir, protocol.LedgerFileName)
	applyLog := filepath.Join(dir, "apply.log")
	const procs = 5

	var wg sync.WaitGroup
	for i := range procs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run", "^TestEnsureOnceCrossProcessRunsOnce$")
			cmd.Env = append(os.Environ(),
				"FIXTURE_XPROC_LEDGER="+ledger,
				"FIXTURE_XPROC_APPLYLOG="+applyLog,
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("子进程 %d 失败: %v\n%s", i, err, out)
			}
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(applyLog)
	if err != nil {
		t.Fatalf("read apply log: %v", err)
	}
	// 每次执行写一行 "applied"；用空白切分计数，恰一条即跨进程只执行一次。
	if got := len(strings.Fields(string(data))); got != 1 {
		t.Fatalf("apply-log 记了 %d 次执行, want 1（跨进程互斥失效）\n%q", got, data)
	}
}
