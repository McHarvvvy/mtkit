package http

import (
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"

	"github.com/McHarvvvy/mtkit/faked/http/protocol"
)

// ControlHandler 暴露 HTTP rule 注册的 control API（POST /v1/http/rules）。
type ControlHandler struct {
	store  *RuleStore
	routes *RouteRegistry
}

// NewControlHandler 构造 ControlHandler。
func NewControlHandler(store *RuleStore, routes *RouteRegistry) *ControlHandler {
	return &ControlHandler{store: store, routes: routes}
}

// Register 把本协议的 control API 路由挂到 daemon 提供的 mux 上。
// wrap 非 nil 时包住 handler（宿主用它注入鉴权等中间件）；nil 表示直接挂载。
func (h *ControlHandler) Register(mux *nethttp.ServeMux, wrap func(nethttp.HandlerFunc) nethttp.HandlerFunc) {
	handler := nethttp.HandlerFunc(h.handleRegister)
	if wrap != nil {
		handler = wrap(handler)
	}
	mux.HandleFunc("POST /v1/http/rules", handler)
}

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
	rule, err := parseRule(h.routes, dto)
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
