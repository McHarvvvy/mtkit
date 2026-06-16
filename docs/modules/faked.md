# 模块设计：Faked 假外部依赖

本文覆盖 mtkit 的「假冒外部依赖」一线，让 SUT 的出站 gRPC / HTTP(S) 调用被拦截并按规则应答：

| 包 | 角色 |
| --- | --- |
| `faked` | daemon 侧：编排 gRPC fake server set + HTTP MITM 代理（内嵌模型） |
| `faked/grpc`、`faked/http` | 两套独立引擎：规则存储、匹配、action 执行、调用日志 |
| `faked/http/ca` | HTTPS MITM 用的动态 / 静态 CA |
| `client/fake` | 测试侧：链式注册 fake 规则的客户端 |

`client/fake` 与服务端通过各自 `protocol` 子包通信、互不 import（测试二进制不会拽服务端代码）。gRPC 与 HTTP 是**两套独立实现**——协议语义差异大（gRPC status code vs HTTP 状态码、直连 vs 代理），刻意不强行统一。

---

## 1. 内嵌编排（`faked/server.go`）

faked 不是独立子进程，而是 daemon 进程内的一个 `faked.Server`，控制面与数据面分离：

```
New        构造 store / registry / control handler（不开任何 listener）
RegisterControl  把 /v1/grpc/rules、/v1/http/rules 挂到 daemon 主 mux（与 lease 共用端口+鉴权）
Start      开数据面 listener：gRPC ServerSet（每 endpoint 一个）+ HTTP MITM 代理（一个）
           → 返回 Addrs{GRPCEndpoints, ProxyAddr, CACertFile, 调用日志路径}
Stop       释放 listener 与日志（幂等）
```

- **控制面**（注册规则）走 daemon 主端口；**数据面**（SUT 实际调用）走 faked 独立监听。两者地址经 `Addrs` 交回 orchestrator，写入 `runtime.json` 并注入 SUT（见 [architecture.md §5.1](../architecture.md#51-依赖连接注入链容器--faked--sut)）。
- `Start` 幂等；任一子部件启动失败会回滚已起部分。
- faked 本身不感知 token：鉴权由宿主 daemon 的 `withAuth` 包在 control handler 外（进程内测试可传 `nil` 不鉴权）。

### 1.1 endpoint 静态注册模型

fake 的 endpoint **不在配置里枚举，而在代码里 `init()` 自注册**（`RegisteredEndpoints()`）——因为「有哪些外部依赖、监听什么地址、用什么 env key」是**项目领域知识**，属于代码而非运行期配置：

- **gRPC endpoint** 声明 `ID()`、`Listen()`（监听地址，可 `:0` 让 OS 分配）、`EnvKey()`（注入 SUT 的环境变量名，SUT 用它拿到直连地址）、`Register(srv, deps)`（把 typed service 注册到 grpc.Server，其 handler 是调 `engine.HandleUnary` 的薄壳）。
- **HTTP endpoint** 声明路由（method、path、`GenRouteKey`）。

`Listen()` 为空视为开发期错误，拒绝启动。

### 1.2 声明一个 fake endpoint（主扩展点）

新增一个 fake 外部依赖 = 写一个 endpoint 文件、`init()` 自注册、项目 `TestMain` blank-import 触发。

**gRPC**：实现 `Endpoint` 接口，并为每个 unary RPC 写一个 typed 薄壳（严守「四步法」：收 typed request → 构造 route_key → 建 typed response 容器 → 调 `Engine.HandleUnary`，**不写业务分支**）：

```go
func init() { fakedgrpc.RegisterEndpoint(&echoEndpoint{}) }

type echoEndpoint struct{}
func (echoEndpoint) ID() string      { return "echo" }              // control API 的 endpoint 取值
func (echoEndpoint) EnvKey() string  { return "ECHO_GRPC_ADDR" }    // 注入 SUT 的直连地址 env 名
func (echoEndpoint) Listen() string  { return "127.0.0.1:0" }       // 监听地址，:0 随机端口
func (echoEndpoint) Register(s *googrpc.Server, deps fakedgrpc.EndpointDeps) {
    pb.RegisterEchoServer(s, &echoService{Engine: deps.Engine})
}
func (echoEndpoint) Methods() []fakedgrpc.MethodSpec {              // 供注册期校验 response 类型
    return []fakedgrpc.MethodSpec{{
        MethodIdentity: fakedgrpc.MethodIdentity{Endpoint: "echo", FullMethod: pb.Echo_Ping_FullMethodName},
        NewResponse:    func() proto.Message { return &pb.PingRes{} },
    }}
}

// typed 薄壳：四步法，route_key 从真实请求字段取
func (s *echoService) Ping(ctx context.Context, req *pb.PingReq) (*pb.PingRes, error) {
    out := &pb.PingRes{}
    err := s.Engine.HandleUnary(ctx, fakedgrpc.Call{
        Endpoint: "echo", FullMethod: pb.Echo_Ping_FullMethodName,
        RouteKey: req.GetKey(), Response: out,
    })
    return out, err
}
```

**HTTP**：实现 `Endpoint{ Host(), Routes() }`，每个 `Route` 填 `Method`/`Path` + `GenRouteKey`（`Host` 由 registry 回填）：

```go
func (googlepayEndpoint) Host() string { return "accounts.google.com" }
func (googlepayEndpoint) Routes() []fakedhttp.Route {
    return []fakedhttp.Route{{
        RouteIdentity: fakedhttp.RouteIdentity{Method: "POST", Path: "/o/oauth2/token"},
        GenRouteKey:   func(r *http.Request) (string, error) { return httproutekey.FormValue(r, "client_id") },
    }}
}
```

daemon 启动时 `BuildMethodRegistry` / `BuildRouteRegistry` 会做一致性校验（endpoint ID 重复、method/route 重复、`Host` 拼写不一致、`NewResponse`/`GenRouteKey` 为 nil 等）——开发期配置错误一律**拒绝启动**。

---

## 2. route_key：并发用例隔离机制（**核心设计**）

多个并发用例共享同一个 faked 引擎，靠 **route_key** 互不串扰。这是理解 faked 的关键。

一条规则的坐标是 `(endpoint, method/fullMethod, route_key)`。注册时由测试给定 route_key；**调用时由引擎从真实请求中现算 route_key**，再按完整坐标查规则：

```
注册：fake.HTTP().On("api.example.com","POST","/login").RouteKey("case-abc").Static(...)
调用：SUT 发 POST api.example.com/login
      → proxy 命中路由后 rt.GenRouteKey(request) 算出 "case-abc"（如从 body 某字段提取）
      → 按 (host, POST, /login, case-abc) 精确查到该用例注册的规则
```

于是 N 个并发用例对同一路由各注册一条带不同 route_key 的规则，互不命中对方。**route_key 为空一律 fail-loud**（gRPC `FailedPrecondition`、HTTP `502`），杜绝「裸路由」误匹配。route_key 通常由 `caseKey(t)` 派生，保证每用例唯一。

### 2.1 route_key helper（Endpoint 作者用）

`GenRouteKey` 怎么从请求里取值，由 `faked/{grpc,http}/routekey` 包提供现成 helper，按取值来源命名，作者直接组合：

| helper | gRPC | HTTP | 说明 |
| --- | --- | --- | --- |
| `Single(v)` | ✓ | ✓ | 把已取到的值包成 route_key，空值报错 |
| `Static()` | ✓ | ✓ | 固定 `"_"`，用于全局静态数据（无路由维度的 RPC） |
| `Composite(fields)` | ✓ | ✓ | 字段名字母序稳定拼成 `k1=v1&k2=v2` |
| `Query` / `PathValue` / `Header` / `FormValue` | | ✓ | 从 URL query / `{name}` 占位 / header / 表单取值 |
| `JSONField(r, "user.id")` | | ✓ | 读 body、按点分路径取字段，并**重置 body** 让后续可再读 |

> 注意：`FormValue` 会 `ParseForm` **消费 body**；后续仍需读 body 的链路应改用 `JSONField`（它读后重置 body）。所有 helper 取到空值都返回 error，最终被引擎当未命中（fail-loud）。

---

## 3. gRPC 引擎（`faked/grpc`）

- **仅支持 unary**，streaming 不在第一版范围。
- `HandleUnary`：route_key 为空 → `FailedPrecondition`；规则未命中 → `FailedPrecondition`；命中 → `action.Execute(typed response)`。
- 引擎**不感知具体 pb 类型**：typed 薄壳预先创建好 response 容器透传给 action 写入，engine 只做查找与调度、不写业务分支。
- `ServerSet` 为每个 endpoint 起独立 listener + `grpc.Server` 并发 Serve，`Start` 返回 `EnvKey → "host:port"`。

---

## 4. HTTP MITM 代理（`faked/http`）

整个 daemon 只暴露**一个**代理监听地址（写入 `runtime.json` 的 `proxy_addr`），所有 fake host 共享。SUT 通过注入的 `HTTP_PROXY` / `HTTPS_PROXY` 把请求发到此代理。

- **HTTPS（CONNECT 隧道）**：回 `200 Connection Established` → 用 CA **动态签发该 host 的叶证书** → 完成 TLS 握手 → 接管明文流。
- **明文 HTTP**：按 `Host` header 路由。
- **每 host 一个 `ServeMux`**：`(method, path)` 命中后 `GenRouteKey` → `engine.HandleHTTP`。
- **未命中统一 502**（host 未注册 / 路由不存在 / method 不符），并记一条 `matched=false` 调用日志——保持 fail-loud + 「所有 fake 请求都有日志」。

> **重要限制**：强制 HTTP/1.1（TLS 只通告 `http/1.1`）；**不支持 streaming / SSE / chunked**，响应一次性写出（用 `Content-Length`）。代理是测试基础设施，不追求生产级协议完整度。

### 4.1 CA（`faked/http/ca`）

- **动态模式**（`ca_cert_file`/`ca_key_file` 都空）：`Generate` 现生成一个 CA，公钥路径经 `Addrs.CACertFile` 暴露。
- **静态模式**（两者都给）：`LoadFromFiles` 复用固定 CA。
- CA 公钥经 `MTKIT_CA_CERT_FILE` 注入 SUT，由 SUT 侧拷入的 CA 插件（[`addons/mtkit_ca_addon.go`](../../addons/)）装填进信任库——否则 SUT 对 fake HTTPS 会证书校验失败。插件从 `SystemCertPool` 起步再追加 faked CA（不替换、只增信任），读不到 CA 文件时 fail-loud panic。

**CA 插件的两条限制**（接 HTTPS faking 时必须知道）：
- ① **只覆盖走 `http.DefaultTransport` 的 client**：插件改的是 `http.DefaultTransport.TLSClientConfig.RootCAs`。SUT 里自建 `http.Transport` 并显式设了 `TLSClientConfig` 的 client，需自行装填 `RootCAs`。
- ② **SUT 不应在 `init` 阶段发起需被 fake 的 HTTPS**：插件的 `init` 在 `main()` 前执行、无并发风险，但若 SUT 在 init 期就发外部 HTTPS，可能早于信任装填或 faked 就绪。

---

## 5. Action 种类

| kind | gRPC | HTTP | 行为 |
| --- | --- | --- | --- |
| `static_response` | ✅ proto.Message | ✅ status+headers+body | 每次命中返回同一响应 |
| `sequence_response` | ✅ | ✅ | 按 `Add` 顺序逐次返回；耗尽后 `RepeatLast` 或 `Error`（gRPC: code+msg / HTTP: 自定义响应） |
| `once_response` | ✅ | ✅ | 首次返回 resp，之后按 `ThenError`(gRPC) / `ThenRespond`(HTTP) 返回 |
| `error` | ✅ 直接 gRPC status error | ➖ | gRPC 独有；HTTP 的「错误」就是带特定 status code 的普通响应，无独立 kind |

gRPC response 以 `proto.Marshal` + base64 编码传输；HTTP response 的 body 同样 base64。

### 5.1 调用日志

两侧引擎都把命中/未命中事件写 **JSONL**（`grpc_calls.jsonl` / `http_calls.jsonl`，落 `<runtime_dir>/faked/`）。每条含 endpoint、method、route_key、`matched`、`action_kind`、（HTTP 还有 `status`）、未命中时的 `error`。这是「SUT 到底调没调、调了什么」的可观察证据。

---

## 6. 用法主线（`client/fake`）

`fake.New(controlAddr, token)` 的 `controlAddr`/`token` 即 `runtime.json` 的 daemon control 地址与 token（与 lease/fixture 共用）。链式构造规则、`Apply(t)` 提交，失败直接 `t.Fatal`：

```go
fc := fake.New(runtimeInfo.Addr, runtimeInfo.Token)

// gRPC：On(endpoint, fullMethod) → RouteKey → action → Apply
fc.GRPC().On("user-svc", "/pb.User/Get").
    RouteKey(caseKey).
    Static(&pb.GetResp{Name: "alice"}).
    Apply(t)

fc.GRPC().On("user-svc", "/pb.User/Get").
    RouteKey(caseKey).
    Error(codes.NotFound, "no such user").
    Apply(t)

// HTTP：On(endpoint, method, path) → RouteKey → action → Apply
fc.HTTP().On("api.example.com", "POST", "/login").
    RouteKey(caseKey).
    Static(fake.Resp().Status(200).JSONBody(map[string]any{"token": "xyz"})).
    Apply(t)

// 序列：逐次返回，耗尽后重复最后一份
fc.HTTP().On("api.example.com", "GET", "/poll").
    RouteKey(caseKey).
    Sequence().
    Add(fake.Resp().Status(202)).
    Add(fake.Resp().Status(200).JSONBody(result)).
    RepeatLastOnExhausted().
    Apply(t)
```

`fake.Resp()` 构造 HTTP 响应：`.Status(code)`（必填，越界报废 builder）、`.Header/.Headers`、`.Body/.JSONBody/.FormBody`。

---

## 7. 注意事项汇总

- **route_key 必填**：为空一律 fail-loud；它是并发用例隔离的根基，通常由 `caseKey(t)` 派生（见 §2）。
- **gRPC 仅 unary**：streaming 第一版不支持。
- **HTTP 仅 HTTP/1.1、无 streaming/chunked/SSE**：响应一次性写出（见 §4）。
- **HTTPS 必须装 CA 插件**：否则 SUT 证书校验失败（见 §4.1 与 `addons/`）。
- **未命中是 fail-loud 而非放行**：gRPC `FailedPrecondition`、HTTP `502`，且必有 `matched=false` 调用日志——便于定位「漏注册规则」。
- **gRPC error 是独立 action，HTTP 没有**：HTTP 用 status code 表达错误。
- **endpoint 在代码 `init()` 注册**：新增 fake 依赖要在代码里声明 endpoint（ID/Listen/EnvKey/Register 或路由），不是改配置（见 §1.2）。
- **gRPC 仅明文 h2c**：fake gRPC endpoint 不做 server-side TLS，测试 client 一律 insecure dial——与 HTTP 侧的 MITM + 动态签发 CA **刻意不对称**（gRPC 是直连，无需冒充证书）。
- **重复注册同 `RuleKey` → 409**：control API 第一版只提供注册，**不提供覆盖/删除/查询**；重复注册返回 `409 duplicate_rule`。
- **注册期就校验**：gRPC 侧校验 `full_method` 已注册到该 endpoint、且 `resp` 能 base64 decode 并 unmarshal 到目标 response 类型（靠 `Methods()` 的 `NewResponse`）；HTTP 侧校验 `method+path` 已注册、`status ∈ [100,599]`、body 可 decode。注册非法即报错，不等到调用时。
- 控制路由（`/v1/grpc/rules`、`/v1/http/rules`）是**内部 wire 协议**，经 `client/fake` 间接使用。
