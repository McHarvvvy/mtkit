package echoendpoint

import (
	nethttp "net/http"

	fakedhttp "github.com/McHarvvvy/mtkit/faked/http"
	httproutekey "github.com/McHarvvvy/mtkit/faked/http/routekey"
	"github.com/McHarvvvy/mtkit/test/e2e/internal/echopb"
)

func init() {
	fakedhttp.RegisterEndpoint(&accEndpoint{})
}

type accEndpoint struct{}

func (accEndpoint) Host() string { return echopb.Host }

func (accEndpoint) Routes() []fakedhttp.Route {
	return []fakedhttp.Route{
		{
			// POST /v1/login：route_key 从 JSON body 字段 account_id 抽——SUT 经代理发 HTTPS 命中此路由。
			RouteIdentity: fakedhttp.RouteIdentity{
				Method: nethttp.MethodPost,
				Path:   echopb.PathLogin,
			},
			GenRouteKey: func(r *nethttp.Request) (string, error) {
				return httproutekey.JSONField(r, "account_id")
			},
		},
	}
}
