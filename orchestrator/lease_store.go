package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type lease struct {
	ID         string
	PID        int
	LastBeatAt time.Time
	ExpireAt   time.Time
}

type leaseStore struct {
	mu     sync.Mutex
	leases map[string]lease
}

func newLeaseStore() *leaseStore {
	return &leaseStore{leases: map[string]lease{}}
}

func (s *leaseStore) acquire(pid int, ttl time.Duration) lease {
	now := time.Now()
	l := lease{
		ID:         "lease-" + randHex(8),
		PID:        pid,
		LastBeatAt: now,
		ExpireAt:   now.Add(ttl),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leases[l.ID] = l
	return l
}

func (s *leaseStore) heartbeat(id string, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[id]
	if !ok {
		return false
	}
	now := time.Now()
	l.LastBeatAt = now
	l.ExpireAt = now.Add(ttl)
	s.leases[id] = l
	return true
}

func (s *leaseStore) release(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.leases[id]
	if ok {
		delete(s.leases, id)
	}
	return ok
}

func (s *leaseStore) activeCount(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	// 关键决策：activeCount 不直接做删除，只负责统计，
	// 清理由 gc 统一处理，避免在高频读路径引入额外写放大。
	for _, l := range s.leases {
		if l.ExpireAt.After(now) {
			count++
		}
	}
	return count
}

func (s *leaseStore) gc(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, l := range s.leases {
		if !l.ExpireAt.After(now) {
			delete(s.leases, id)
		}
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
