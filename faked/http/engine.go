package http

import (
	nethttp "net/http"
	"time"

	"github.com/McHarvvvy/mtkit/faked/http/protocol"
)

// Call 是 proxy 在请求到达时打包传入的请求上下文。
// W / R 是 MITM 代理在 dispatch 阶段提供的；ctx 通过 R.Context() 获取，不额外加字段。
type Call struct {
	Endpoint string
	Method   string
	Path     string
	RouteKey string
	W        nethttp.ResponseWriter
	R        *nethttp.Request
}

// CallLogger 由 engine 调用记录命中 / 未命中事件。
type CallLogger interface {
	Log(event CallEvent)
}

// ActionEngine 在 HTTP 请求到达时做规则查找与 action 调度。
type ActionEngine struct {
	store  *RuleStore
	logger CallLogger
}

// NewActionEngine 构造 ActionEngine。logger 为 nil 时退化为 NoopCallLogger。
func NewActionEngine(store *RuleStore, logger CallLogger) *ActionEngine {
	if logger == nil {
		logger = NoopCallLogger
	}
	return &ActionEngine{store: store, logger: logger}
}

// HandleHTTP 处理一次 HTTP 请求。未命中或 route_key 为空都返回 502 + 写调用日志。
//
// 返回 error 仅用于通知调用方（proxy）发生了响应写出错误；当 rule 命中但 action
// 内部出错（如 Write 失败），error 透传给 proxy，但响应可能已部分写出。
func (e *ActionEngine) HandleHTTP(call Call) error {
	ts := time.Now().UTC()
	if call.RouteKey == "" {
		e.logMiss(ts, call, "empty fake route_key")
		nethttp.Error(call.W, "empty fake route_key", nethttp.StatusBadGateway)
		return nil
	}
	key := protocol.RuleKey{
		Endpoint: call.Endpoint,
		Method:   call.Method,
		Path:     call.Path,
		RouteKey: call.RouteKey,
	}
	rule, err := e.store.Get(key)
	if err != nil {
		e.logMiss(ts, call, err.Error())
		nethttp.Error(call.W, err.Error(), nethttp.StatusBadGateway)
		return nil
	}
	sw := newStatusWriter(call.W)
	execErr := rule.Action.Execute(sw, call.R)
	e.logMatch(ts, call, rule, sw.status, execErr)
	return execErr
}

func (e *ActionEngine) logMatch(ts time.Time, call Call, rule Rule, statusCode int, execErr error) {
	ev := CallEvent{
		Timestamp:  ts,
		Endpoint:   call.Endpoint,
		Method:     call.Method,
		Path:       call.Path,
		RouteKey:   call.RouteKey,
		RuleID:     rule.Key().RuleID(),
		Matched:    true,
		ActionKind: rule.Action.Kind(),
		Status:     statusCode,
	}
	if execErr != nil {
		ev.Error = execErr.Error()
	}
	e.logger.Log(ev)
}

func (e *ActionEngine) logMiss(ts time.Time, call Call, errStr string) {
	e.logger.Log(CallEvent{
		Timestamp: ts,
		Endpoint:  call.Endpoint,
		Method:    call.Method,
		Path:      call.Path,
		RouteKey:  call.RouteKey,
		Matched:   false,
		Error:     errStr,
	})
}

// statusWriter 包装 ResponseWriter 抓取实际写出的 status code 给调用日志使用。
// 仅记录首次 WriteHeader 的值；与 net/http 行为一致——首次 WriteHeader 决定最终 status。
type statusWriter struct {
	nethttp.ResponseWriter
	status int
	wrote  bool
}

func newStatusWriter(w nethttp.ResponseWriter) *statusWriter {
	return &statusWriter{ResponseWriter: w}
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.wrote = true
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(nethttp.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
