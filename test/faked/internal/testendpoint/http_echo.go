package testendpoint

import (
	nethttp "net/http"

	fakedhttp "github.com/McHarvvvy/mtkit/faked/http"
	httproutekey "github.com/McHarvvvy/mtkit/faked/http/routekey"
)

// HostAccTest 是测试用 HTTP endpoint 的域名。
// 与任何业务 host 不重名（业务侧用 accounts.google.com 等真实域名）。
const HostAccTest = "acc.test.faked"

// PathLogin / PathToken / PathStaticWhoami 与 SUT 角色（测试代码）使用的请求路径一致。
//
// PathLogin 收 application/json body——SUT 主流交互格式；route_key 从 JSON 字段
// account_id 解出。PathToken 收 application/x-www-form-urlencoded，作为次要格式
// 覆盖（OAuth token 端点惯例）。PathStaticWhoami 用 Static() 全局 key。
const (
	PathLogin        = "/v1/login"
	PathToken        = "/token"
	PathStaticWhoami = "/static/whoami"
)

func init() {
	fakedhttp.RegisterEndpoint(&accTestEndpoint{})
}

type accTestEndpoint struct{}

func (accTestEndpoint) Host() string { return HostAccTest }

func (accTestEndpoint) Routes() []fakedhttp.Route {
	return []fakedhttp.Route{
		{
			// POST /v1/login：route_key 从 JSON body 字段 account_id 抽。
			// 覆盖 SUT 主流的 application/json 请求格式。
			RouteIdentity: fakedhttp.RouteIdentity{
				Method: nethttp.MethodPost,
				Path:   PathLogin,
			},
			GenRouteKey: func(r *nethttp.Request) (string, error) {
				return httproutekey.JSONField(r, "account_id")
			},
		},
		{
			// POST /token：route_key 从 form 字段 client_id 抽。覆盖"按 form 字段构造 route_key"。
			RouteIdentity: fakedhttp.RouteIdentity{
				Method: nethttp.MethodPost,
				Path:   PathToken,
			},
			GenRouteKey: func(r *nethttp.Request) (string, error) {
				return httproutekey.FormValue(r, "client_id")
			},
		},
		{
			// GET /static/whoami：route_key 用 Static()，覆盖"全局静态 key" 与 GET 路由。
			RouteIdentity: fakedhttp.RouteIdentity{
				Method: nethttp.MethodGet,
				Path:   PathStaticWhoami,
			},
			GenRouteKey: func(_ *nethttp.Request) (string, error) {
				return httproutekey.Static(), nil
			},
		},
	}
}
