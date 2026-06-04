package http

import (
	"fmt"
	nethttp "net/http"
	"sync"
)

// RouteIdentity 是路由的全局唯一身份：Host + Method + Path。
//
// 同时承担两个角色——
//   - 嵌入 Route：作者写 Route 时只填 Method + Path，Host 由 BuildRouteRegistry
//     从 Endpoint.Host() 回填；
//   - 直接作为 RouteRegistry 内部 map key，无影子类型、无嵌套 map。
type RouteIdentity struct {
	// Host 由 BuildRouteRegistry 从 Endpoint.Host() 填入；作者无须设置。
	// 若作者写了非空 Host 且与 Endpoint.Host() 不一致，daemon 拒绝启动。
	Host string
	// Method 是 HTTP method（"GET" / "POST" / ...），大小写敏感，建议全大写。
	Method string
	// Path 使用 http.ServeMux 1.22+ 模式语法，例 "/users/{id}"。
	Path string
}

// Route 是 HTTP endpoint 上一个具体路由的 fake binding。
//
// 嵌入 RouteIdentity（Method + Path 由作者填，Host 由 registry 填）；
// GenRouteKey 是请求到达时从 request 提取 route_key 字符串的提取器，
// 由 Endpoint 作者按业务规则实现（query / form / body / path value 等），
// 返回空字符串视为非法。
type Route struct {
	RouteIdentity
	GenRouteKey func(*nethttp.Request) (string, error)
}

// Endpoint 表示一个外部 HTTP 服务的 fake 边界。
// Host() 返回的字符串既是 SUT 请求的 Host header（域名，不含端口），
// 也是 control API 中 endpoint 字段的取值——HTTP endpoint 没有独立 ID。
type Endpoint interface {
	Host() string
	Routes() []Route
}

// endpointRegistry 是 init() 自注册的 package-level Endpoint 列表。
var endpointRegistry struct {
	mu        sync.Mutex
	endpoints []Endpoint
}

// RegisterEndpoint 在 init() 阶段自注册 Endpoint。
func RegisterEndpoint(e Endpoint) {
	endpointRegistry.mu.Lock()
	defer endpointRegistry.mu.Unlock()
	endpointRegistry.endpoints = append(endpointRegistry.endpoints, e)
}

// RegisteredEndpoints 返回当前已自注册的 Endpoint 副本。
func RegisteredEndpoints() []Endpoint {
	endpointRegistry.mu.Lock()
	defer endpointRegistry.mu.Unlock()
	out := make([]Endpoint, len(endpointRegistry.endpoints))
	copy(out, endpointRegistry.endpoints)
	return out
}

// RouteRegistry 持有 daemon 启动时收集的所有 Route 与其所属 host。
// control API 用它在注册阶段校验 (endpoint, method, path) 已静态注册；
// proxy 用它在请求到达时找到对应 GenRouteKey 提取器。
//
// map key 直接复用 RouteIdentity——单层 map，无影子 key 类型、无嵌套 map。
type RouteRegistry struct {
	routes    map[RouteIdentity]Route
	hostMuxes map[string]*nethttp.ServeMux
}

// BuildRouteRegistry 从一组 Endpoint 收集 Route 与 ServeMux，做一致性校验。
//
// 校验失败说明开发期配置错误（host 冲突、方法+路径重复、Host 拼写不一致），
// daemon 拒绝启动。proxy 不直接执行 action：dispatcher 把 Route 引用回传给
// ActionEngine，由 engine 调用 GenRouteKey 提取器并查 store。
func BuildRouteRegistry(endpoints []Endpoint) (*RouteRegistry, error) {
	r := &RouteRegistry{
		routes:    make(map[RouteIdentity]Route),
		hostMuxes: make(map[string]*nethttp.ServeMux),
	}
	for _, e := range endpoints {
		host := e.Host()
		if host == "" {
			return nil, fmt.Errorf("http endpoint has empty Host()")
		}
		if _, exists := r.hostMuxes[host]; exists {
			return nil, fmt.Errorf("duplicate http endpoint host: %s", host)
		}
		for _, rt := range e.Routes() {
			if err := validateRoute(host, &rt); err != nil {
				return nil, err
			}
			if _, dup := r.routes[rt.RouteIdentity]; dup {
				return nil, fmt.Errorf("duplicate route in endpoint %s: %s %s", host, rt.Method, rt.Path)
			}
			r.routes[rt.RouteIdentity] = rt
		}
		r.hostMuxes[host] = nethttp.NewServeMux()
	}
	return r, nil
}

// validateRoute 校验 Route 必填字段并回填 Host。
// 作者若误写 Host 与 Endpoint.Host() 不一致直接报错——避免静默覆盖。
func validateRoute(host string, rt *Route) error {
	if rt.Method == "" {
		return fmt.Errorf("http endpoint %s: route has empty Method", host)
	}
	if rt.Path == "" {
		return fmt.Errorf("http endpoint %s: route has empty Path", host)
	}
	if rt.GenRouteKey == nil {
		return fmt.Errorf("http endpoint %s: route %s %s has nil GenRouteKey", host, rt.Method, rt.Path)
	}
	if rt.Host != "" && rt.Host != host {
		return fmt.Errorf("http endpoint %s: route %s %s has mismatched Host %q",
			host, rt.Method, rt.Path, rt.Host)
	}
	rt.Host = host
	return nil
}

// Lookup 按 (host, method, path) 查询 Route binding。
// path 必须与 Endpoint.Routes() 中声明的 pattern 完全一致；模糊匹配交给
// proxy 层的 ServeMux 处理后再回查。
func (r *RouteRegistry) Lookup(host, method, path string) (Route, bool) {
	rt, ok := r.routes[RouteIdentity{Host: host, Method: method, Path: path}]
	return rt, ok
}

// HostMuxes 返回 host → ServeMux 的映射；proxy 用它做按 host + path 分发。
// 调用方负责为 mux 注册 handler。
func (r *RouteRegistry) HostMuxes() map[string]*nethttp.ServeMux {
	return r.hostMuxes
}

// Routes 遍历所有已注册路由；proxy / daemon 装配时使用。
func (r *RouteRegistry) Routes() []Route {
	out := make([]Route, 0, len(r.routes))
	for _, rt := range r.routes {
		out = append(out, rt)
	}
	return out
}

// RoutesByHost 按 host 分组返回路由，便于 proxy 一次性给每个 host 的 mux 注册 handler。
func (r *RouteRegistry) RoutesByHost() map[string][]Route {
	out := make(map[string][]Route, len(r.hostMuxes))
	for _, rt := range r.routes {
		out[rt.Host] = append(out[rt.Host], rt)
	}
	return out
}

// HostExists 判断给定 host 是否已经被某个 Endpoint 注册。
func (r *RouteRegistry) HostExists(host string) bool {
	_, ok := r.hostMuxes[host]
	return ok
}
