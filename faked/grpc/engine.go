package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/McHarvvvy/mtkit/faked/grpc/protocol"
)

// Call 是 typed 薄壳调 HandleUnary 时打包传入的请求上下文。
// Response 是薄壳预先创建好的 typed proto 容器：engine 不感知具体 pb 类型，
// 把容器透传给 action 由 action 写入。
type Call struct {
	Endpoint   string
	FullMethod string
	RouteKey   string
	Response   proto.Message
}

// CallLogger 由 engine 调用记录命中 / 未命中事件。
// 具体实现见 call_log.go；测试场景可注入 NoopCallLogger。
type CallLogger interface {
	Log(event CallEvent)
}

// ActionEngine 在请求到达时做规则查找与 action 调度。
// 不感知具体 pb 类型，不写业务分支；典型分支只有 "route_key 为空" 与 "rule 未注册"。
type ActionEngine struct {
	store  *RuleStore
	logger CallLogger
}

// NewActionEngine 构造 ActionEngine。logger 不能为 nil；调用方应传 NoopCallLogger
// 表达"丢弃事件"语义。
func NewActionEngine(store *RuleStore, logger CallLogger) *ActionEngine {
	if logger == nil {
		logger = NoopCallLogger
	}
	return &ActionEngine{store: store, logger: logger}
}

// HandleUnary 是 typed 薄壳处理 unary RPC 时统一入口。
//
// 设计文档明确：streaming RPC 不在第一版支持范围。route_key 为空、rule 未注册
// 都返回 codes.FailedPrecondition，让 SUT 立即 fail loud。
func (e *ActionEngine) HandleUnary(_ context.Context, call Call) error {
	ts := time.Now().UTC()
	if call.RouteKey == "" {
		e.logMiss(ts, call, "empty fake route_key")
		return status.Error(codes.FailedPrecondition, "empty fake route_key")
	}
	key := protocol.RuleKey{
		Endpoint:   call.Endpoint,
		FullMethod: call.FullMethod,
		RouteKey:   call.RouteKey,
	}
	rule, err := e.store.Get(key)
	if err != nil {
		e.logMiss(ts, call, err.Error())
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	e.logMatch(ts, call, rule)
	return rule.Action.Execute(call.Response)
}

func (e *ActionEngine) logMatch(ts time.Time, call Call, rule Rule) {
	e.logger.Log(CallEvent{
		Timestamp:  ts,
		Endpoint:   call.Endpoint,
		FullMethod: call.FullMethod,
		RouteKey:   call.RouteKey,
		RuleID:     rule.Key().RuleID(),
		Matched:    true,
		ActionKind: rule.Action.Kind(),
	})
}

func (e *ActionEngine) logMiss(ts time.Time, call Call, errStr string) {
	e.logger.Log(CallEvent{
		Timestamp:  ts,
		Endpoint:   call.Endpoint,
		FullMethod: call.FullMethod,
		RouteKey:   call.RouteKey,
		Matched:    false,
		Error:      errStr,
	})
}
