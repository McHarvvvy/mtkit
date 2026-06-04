package grpc

import (
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/McHarvvvy/mtkit/faked/grpc/protocol"
)

// Action 是 gRPC rule 命中后执行的行为。
//
// Action 实例与 Rule 绑定（一对一），状态（sequence index / once consumed 等）
// 按 rule 隔离。Execute 由 ActionEngine 在 SUT 请求到达时调用。
//
// resp 是协议层薄壳创建的 typed response 容器；Execute 把响应数据写入容器，
// 或返回 gRPC error 让薄壳直接 return err。
type Action interface {
	Kind() string
	Execute(resp proto.Message) error
}

// Rule 是 store 内部持有的 gRPC rule 运行时形态。
//
// 与 protocol.Rule 的差异：Action 字段是已解析、可直接 Execute 的 runtime 类型，
// 而非 wire DTO。runtime Action 自带可能存在的状态（sequence 计数 / once 标志）。
type Rule struct {
	Endpoint   string
	FullMethod string
	RouteKey   string
	Action     Action
}

// Key 返回 rule 唯一性 key。
func (r Rule) Key() protocol.RuleKey {
	return protocol.RuleKey{
		Endpoint:   r.Endpoint,
		FullMethod: r.FullMethod,
		RouteKey:   r.RouteKey,
	}
}

// RuleStore 维护 gRPC rule 的注册、查询与并发安全。
//
// 同一 RuleKey 严禁覆盖注册：测试组织层负责生成不冲突的 route_key；store 仅
// 报错。action 自带的状态（sequence index / once 标志）由 action 自己加锁保护。
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
