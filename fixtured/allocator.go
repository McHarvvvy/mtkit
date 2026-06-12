package fixtured

import "sync/atomic"

// Allocator 是唯一性的源头：串行发放全局唯一、单调递增的 int64 token 区块。
//
// v1 单一全局 kind（kind 参数仅占位）：所有形态共用同一条 token 序列，
// 唯一性只在此一处保证，每种格式只是 token 到目标格式的单射，碰撞免于逐格式证明（见 fixture-spec §1）。
type Allocator struct {
	counter atomic.Int64
}

// Alloc 返回半开区间 [start, start+size)，跨调用者天然不相交。
// 实现是一次 atomic FetchAdd：newEnd = counter.Add(size)，无锁，并发安全。
// size 的合法性（>0 且 ≤ 上界）由调用方 handleAlloc 校验，此处不再重复判断。
func (a *Allocator) Alloc(kind string, size int64) (start, end int64) {
	newEnd := a.counter.Add(size)
	return newEnd - size, newEnd
}
