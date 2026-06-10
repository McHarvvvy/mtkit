package grpc

import (
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"

	"github.com/McHarvvvy/mtkit/faked/grpc/protocol"
)

// ControlHandler 暴露 gRPC rule 注册的 HTTP control API（POST /v1/grpc/rules）。
//
// 第一版只提供注册接口；不提供查询 / 删除 / 覆盖。错误响应包含 ok=false、
// error（机器可读 code）、message（人类可读描述）。
type ControlHandler struct {
	store   *RuleStore
	methods *MethodRegistry
}

// NewControlHandler 构造 ControlHandler。
func NewControlHandler(store *RuleStore, methods *MethodRegistry) *ControlHandler {
	return &ControlHandler{store: store, methods: methods}
}

// Register 把本协议的 control API 路由挂到 daemon 提供的 mux 上。
// URL 路径仍归属本包——daemon 无需硬编码协议 path 字符串。
// wrap 非 nil 时包住 handler（宿主用它注入鉴权等中间件）；nil 表示直接挂载。
func (h *ControlHandler) Register(mux *nethttp.ServeMux, wrap func(nethttp.HandlerFunc) nethttp.HandlerFunc) {
	handler := nethttp.HandlerFunc(h.handleRegister)
	if wrap != nil {
		handler = wrap(handler)
	}
	mux.HandleFunc("POST /v1/grpc/rules", handler)
}

// handleRegister 处理单条 rule 注册请求。
func (h *ControlHandler) handleRegister(w nethttp.ResponseWriter, r *nethttp.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, nethttp.StatusBadRequest, protocol.ErrCodeInvalidActionData, "read body: "+err.Error())
		return
	}
	var dto protocol.Rule
	if err := json.Unmarshal(raw, &dto); err != nil {
		writeError(w, nethttp.StatusBadRequest, protocol.ErrCodeInvalidActionData, "unmarshal rule: "+err.Error())
		return
	}
	rule, err := parseRule(h.methods, dto)
	if err != nil {
		writeParseError(w, err)
		return
	}
	if err := h.store.Add(rule); err != nil {
		writeError(w, nethttp.StatusConflict, protocol.ErrCodeDuplicateRule, err.Error())
		return
	}
	writeOK(w, rule.Key().RuleID())
}

// writeParseError 把 parser 返回的 *protocol.Error 映射为合适的 HTTP 状态码。
// duplicate_rule 走 409；其它分类错误均为 400；非分类错误返回 500。
func writeParseError(w nethttp.ResponseWriter, err error) {
	var apiErr *protocol.Error
	if !errors.As(err, &apiErr) {
		writeError(w, nethttp.StatusInternalServerError, "internal", err.Error())
		return
	}
	status := nethttp.StatusBadRequest
	if apiErr.Code == protocol.ErrCodeDuplicateRule {
		status = nethttp.StatusConflict
	}
	writeError(w, status, apiErr.Code, apiErr.Message)
}

func writeOK(w nethttp.ResponseWriter, ruleID string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"rule_id": ruleID,
	})
}

func writeError(w nethttp.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      false,
		"error":   code,
		"message": message,
	})
}
