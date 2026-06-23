// Package endpoint 是业务侧 fake HTTP(S) endpoint 的自注册入口（你项目里的 test/fake/http/endpoint）。
//
// 每个外部 HTTP 服务一个文件：endpoint 定义（Host + Routes + 每路由的 RouteKey 提取函数）。
// init() 调 fakedhttp.RegisterEndpoint 自注册，由 bootstrap blank-import 触发。
// 本包是**服务端叶子**：只 import faked/http 与其 routekey/，**禁止反向依赖 bootstrap / 用例 / harness / 业务源码**。
// 针对 endpoint 注册响应规则的客户端封装（如有复用需要）放同级 rule 包；规则不复用时直接在用例里 inline 注册。
//
// 关于 HTTPS：faked 对出站 HTTPS 做 MITM、用动态 CA 现签叶证书，**SUT 必须信任这张 CA 才能握手**。
// 故凡要 fake HTTPS，SUT 的 main 包必须拷入 mtkit/addons/mtkit_ca_addon.go 并以 -tags mtkit_it 构建
// （bootstrap/run.go 已加该 tag）。纯明文 HTTP / 纯 gRPC 不需要 CA 插件。
package endpoint

import (
	nethttp "net/http"

	fakedhttp "github.com/McHarvvvy/mtkit/faked/http"
	"github.com/McHarvvvy/mtkit/faked/http/routekey"
)

// HostExchange 是汇率接口的外部域名；也是 control API 的 endpoint 字段值。与 SUT 请求 Host header 一致、不含端口。
const HostExchange = "rates.example.com"

func init() { fakedhttp.RegisterEndpoint(&exchangeEndpoint{}) }

type exchangeEndpoint struct{}

func (exchangeEndpoint) Host() string { return HostExchange }

// Routes 声明本 endpoint 需 fake 的全部 (method, path)。RouteKey 取 query 字段 base——
// 每个用例据此用专属 base 值分流，互不串扰（并发隔离）。
func (exchangeEndpoint) Routes() []fakedhttp.Route {
	return []fakedhttp.Route{
		{
			RouteIdentity: fakedhttp.RouteIdentity{
				Method: nethttp.MethodGet,
				Path:   "/v1/rate",
				// Host 留空：BuildRouteRegistry 用 Endpoint.Host() 回填。
			},
			GenRouteKey: func(r *nethttp.Request) (string, error) {
				return routekey.Query(r, "base")
			},
		},
	}
}
