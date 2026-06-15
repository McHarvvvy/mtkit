package fixtured

import (
	"sync"
	"testing"
)

// TestAllocatorConcurrentBlocksDoNotOverlap 守护"发号是全局唯一性源头"这一根本契约（A1）：
// 无论多少调用者并发抢号，拿到的半开区间两两不相交、覆盖连续、无重无漏。
//
// 验证手段：32 个 goroutine 各 Alloc 一个等长区块，把所有区块展开成单个 token 写进一个
// 集合——只要出现重复 token，或总数对不上 workers×size，就说明 atomic FetchAdd 的串行化被
// 破坏。这是纯内存并发用例（不经 HTTP），必须在 -race 下通过。
func TestAllocatorConcurrentBlocksDoNotOverlap(t *testing.T) {
	var alloc Allocator
	const workers = 32
	const size = int64(128)

	type block struct {
		start int64
		end   int64
	}
	blocks := make(chan block, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start, end := alloc.Alloc("token", size)
			blocks <- block{start: start, end: end}
		}()
	}
	wg.Wait()
	close(blocks)

	seen := make(map[int64]bool, workers*int(size))
	for b := range blocks {
		if b.end-b.start != size {
			t.Fatalf("block size mismatch: [%d,%d)", b.start, b.end)
		}
		for v := b.start; v < b.end; v++ {
			if seen[v] {
				t.Fatalf("duplicate token %d", v)
			}
			seen[v] = true
		}
	}
	if got, want := len(seen), workers*int(size); got != want {
		t.Fatalf("token count = %d, want %d", got, want)
	}
}
