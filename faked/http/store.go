package http

import (
	"fmt"
	nethttp "net/http"
	"sync"

	"github.com/McHarvvvy/mtkit/faked/http/protocol"
)

// Action 是 HTTP rule 命中后执行的行为。
//
// 与 gRPC 协议不同：HTTP 没有 typed response 容器，response 直接写到
// http.ResponseWriter。所有错误响应通过带特定 status 的 static_response
// 表达，故 HTTP 协议下无 error kind。
type Action interface {
	Kind() string
	Execute(w nethttp.ResponseWriter, r *nethttp.Request) error
}

// Rule 是 store 内部持有的 HTTP rule 运行时形态。
type Rule struct {
	Endpoint string
	Method   string
	Path     string
	RouteKey string
	Action   Action
}

// Key 返回 rule 唯一性 key。
func (r Rule) Key() protocol.RuleKey {
	return protocol.RuleKey{
		Endpoint: r.Endpoint,
		Method:   r.Method,
		Path:     r.Path,
		RouteKey: r.RouteKey,
	}
}

// RuleStore 维护 HTTP rule 的注册、查询与并发安全。
type RuleStore struct {
	mu    sync.RWMutex
	rules map[protocol.RuleKey]Rule
}

// NewRuleStore 创建空的 RuleStore。
func NewRuleStore() *RuleStore {
	return &RuleStore{rules: make(map[protocol.RuleKey]Rule)}
}

// Add 注册一条 rule；同一 RuleKey 已存在时返回 duplicate_rule 错误。
func (s *RuleStore) Add(rule Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := rule.Key()
	if _, exists := s.rules[key]; exists {
		return fmt.Errorf("duplicate_rule: %s", key.RuleID())
	}
	s.rules[key] = rule
	return nil
}

// Get 按 key 查询 rule；未注册时返回 no fake rule 错误。
func (s *RuleStore) Get(key protocol.RuleKey) (Rule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rule, ok := s.rules[key]
	if !ok {
		return Rule{}, fmt.Errorf("no fake rule: %s", key.RuleID())
	}
	return rule, nil
}
