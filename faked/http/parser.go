package http

import (
	"github.com/McHarvvvy/mtkit/faked/http/actions"
	"github.com/McHarvvvy/mtkit/faked/http/protocol"
)

// parseRule 校验 control API 收到的 HTTP rule DTO，构造可入 store 的 runtime Rule。
//
// 校验顺序：endpoint 已启用 → route_key 非空 → (method, path) 已静态注册到该 endpoint
// → action kind 支持 → action data 形状正确（status / body_base64 可 decode）。
func parseRule(routes *RouteRegistry, dto protocol.Rule) (Rule, error) {
	if !routes.HostExists(dto.Endpoint) {
		return Rule{}, protocol.NewError(protocol.ErrCodeInvalidEndpoint,
			"endpoint %q not enabled", dto.Endpoint)
	}
	if dto.RouteKey == "" {
		return Rule{}, protocol.NewError(protocol.ErrCodeInvalidRouteKey, "empty route_key")
	}
	if _, ok := routes.Lookup(dto.Endpoint, dto.Method, dto.Path); !ok {
		return Rule{}, protocol.NewError(protocol.ErrCodeInvalidRoute,
			"%s %s not registered on endpoint %s", dto.Method, dto.Path, dto.Endpoint)
	}
	action, err := parseAction(dto.Action)
	if err != nil {
		return Rule{}, err
	}
	return Rule{
		Endpoint: dto.Endpoint,
		Method:   dto.Method,
		Path:     dto.Path,
		RouteKey: dto.RouteKey,
		Action:   action,
	}, nil
}

// parseAction 根据 kind 分发到 actions 包对应的构造函数。
// HTTP 协议下没有 error kind——错误响应通过带特定 status 的 static_response 表达。
func parseAction(dto protocol.Action) (Action, error) {
	switch dto.Kind {
	case "":
		return nil, protocol.NewError(protocol.ErrCodeUnsupportedAction, "empty kind")
	case protocol.ActionKindStaticResponse:
		return actions.NewStaticResponse(dto.Data)
	case protocol.ActionKindSequenceResponse:
		return actions.NewSequenceResponse(dto.Data)
	case protocol.ActionKindOnceResponse:
		return actions.NewOnceResponse(dto.Data)
	default:
		return nil, protocol.NewError(protocol.ErrCodeUnsupportedAction, "%s", dto.Kind)
	}
}
