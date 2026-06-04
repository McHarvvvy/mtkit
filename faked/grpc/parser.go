package grpc

import (
	"github.com/McHarvvvy/mtkit/faked/grpc/actions"
	"github.com/McHarvvvy/mtkit/faked/grpc/protocol"
)

// parseRule 校验 control API 收到的 rule DTO，构造可入 store 的 runtime Rule。
//
// 校验顺序由设计文档 §15.1 决定：endpoint 已启用 → route_key 非空 → method 已注册
// → action kind 支持 → action data 形状正确（含 response bytes 可 unmarshal）。
// 任一步失败立即返回，错误包含 code 与详细 message。
func parseRule(methods *MethodRegistry, dto protocol.Rule) (Rule, error) {
	if !methods.HasEndpoint(dto.Endpoint) {
		return Rule{}, protocol.NewError(protocol.ErrCodeInvalidEndpoint,
			"endpoint %q not enabled", dto.Endpoint)
	}
	if dto.RouteKey == "" {
		return Rule{}, protocol.NewError(protocol.ErrCodeInvalidRouteKey, "empty route_key")
	}
	spec, ok := methods.Lookup(dto.Endpoint, dto.FullMethod)
	if !ok {
		return Rule{}, protocol.NewError(protocol.ErrCodeInvalidMethod,
			"%s not registered on endpoint %s", dto.FullMethod, dto.Endpoint)
	}
	action, err := parseAction(spec, dto.Action)
	if err != nil {
		return Rule{}, err
	}
	return Rule{
		Endpoint:   dto.Endpoint,
		FullMethod: dto.FullMethod,
		RouteKey:   dto.RouteKey,
		Action:     action,
	}, nil
}

// parseAction 根据 kind 分发到 actions 包对应的构造函数。
func parseAction(spec MethodSpec, dto protocol.Action) (Action, error) {
	switch dto.Kind {
	case "":
		return nil, protocol.NewError(protocol.ErrCodeUnsupportedAction, "empty kind")
	case protocol.ActionKindStaticResponse:
		return actions.NewStaticResponse(spec.NewResponse, dto.Data)
	case protocol.ActionKindSequenceResponse:
		return actions.NewSequenceResponse(spec.NewResponse, dto.Data)
	case protocol.ActionKindOnceResponse:
		return actions.NewOnceResponse(spec.NewResponse, dto.Data)
	case protocol.ActionKindError:
		return actions.NewError(dto.Data)
	default:
		return nil, protocol.NewError(protocol.ErrCodeUnsupportedAction, "%s", dto.Kind)
	}
}
