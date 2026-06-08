# Faked 设计实现文档

`faked` 是 API / 入口级集成测试用的 fake 外部依赖 daemon。它替代 SUT 真实依赖的外部 gRPC 与 HTTP/HTTPS 服务，让 SUT 仍以独立进程、通过真实网络协议访问 fake。

设计约束：

- 测试用例只注册 **rule**，不注册跨进程 Go handler；fake 与 SUT 进程边界清晰。
- faked 不理解业务字段，只回放测试预先注册的响应数据。
- 协议具体 request / response 类型只出现在 endpoint 路由层，框架核心层不依赖任何 pb 包。
- 框架核心只做 rule 查找、action 执行、状态维护、日志与生命周期。
- 失败一律 fail loud，绝不静默返回空 response。

## 1. 分层

faked 分三层：

**框架核心层**（跨协议复用，逻辑固定）：daemon 生命周期、单一 control API、gRPC endpoint server 与 HTTP MITM proxy 的启停、每协议各自的 `RuleStore` 与 `ActionEngine`、action runtime、两份调用日志、测试侧 client。核心层不解析 request 字段，不写业务分支。

**协议层**（`faked/grpc` 与 `faked/http`，互不 import）：包名分别为 `grpc` / `http`，与标准库冲突时按约定 alias 为 `fakedgrpc` / `fakedhttp`。各自绑定一种外部协议，定义 endpoint、route 路由、action data schema。

**测试数据层**（fixture 性质）：测试侧用 `client.New()` 的链式 API 注册 rule；typed response 与业务场景数据由 builder 构造，测试用例不手写 base64。

## 2. Rule

两协议的 rule key 各自定义，不强行统一。

| 协议 | RuleKey 组成 |
|---|---|
| gRPC | `endpoint` + `full_method` + `route_key` |
| HTTP | `endpoint` + `method` + `path` + `route_key` |

- **gRPC `endpoint`**：外部 gRPC 地址边界，如 `forum`。`full_method`：canonical full method name，如 `/protos.ForumUser/GetUserInfo`（生成代码里有 `ForumUser_GetUserInfo_FullMethodName` 常量）。
- **HTTP `endpoint`**：外部服务域名，与 SUT 请求 Host header 一致，不含端口，如 `accounts.google.com`。`path`：`http.ServeMux` 1.22+ 模式语法（支持 `{name}` 占位符）。

### 2.1 RouteKey 构造

`route_key` 不是 request 中固定存在的字段，由协议层从已有信息构造：

- **gRPC**：typed 薄壳直接读字段，如 `req.GetGuid()`。
- **HTTP**：route binding 上挂 `func(*http.Request) (string, error)`。

`routekey` 包提供构造 helper（返回值不能为空，空值即失败）：

| Helper | gRPC | HTTP | 说明 |
|---|---|---|---|
| `Single(value)` | ✓ | ✓ | 单值直传 |
| `Static()` | ✓ | ✓ | 固定 `_`，用于全局静态数据 |
| `Composite(fields)` | ✓ | ✓ | 字段名字母序排序，escaping 后拼为 `a=x&b=y`；空值字段也参与 |
| `Query` / `PathValue` / `Header` / `FormValue` | | ✓ | 从 request 对应位置取值 |
| `JSONField(r, jsonPath)` | | ✓ | 读 body、解析 JSON、取字段；读后重置 `r.Body` |

`route_key` 是否全局唯一由测试数据与测试组织保证，faked 不推断（见 §13）。

### 2.2 Rule ID

`rule_id` 是展示 ID，用于日志与错误信息：

```text
gRPC: <endpoint>|<full_method>|<route_key>   例: forum|/protos.ForumUser/GetUserInfo|user_001
HTTP: <endpoint>|<method>|<path>|<route_key> 例: accounts.google.com|POST|/o/oauth2/token|client_xxx
```

字段含分隔符时展示层做 escaping，内部仍以结构化 key 为准。重复注册相同 `RuleKey` 返回 `409 duplicate_rule`，不提供覆盖接口。

## 3. gRPC 协议层

### 3.1 Typed 薄壳

每个需要 fake 的 unary RPC 一个薄壳，只做四件事：接收 typed request → 构造 `route_key` → 创建 typed response 容器 → 调用通用 `HandleUnary`。允许重复模板代码，禁止写业务 fake 逻辑。

```go
func (s *forumUserService) GetUserInfo(ctx context.Context, req *forumpb.GetUserInfoReq) (*forumpb.GetUserInfoRes, error) {
    out := &forumpb.GetUserInfoRes{}
    if err := s.Engine.HandleUnary(ctx, fakedgrpc.Call{
        Endpoint:   EndpointForum,
        FullMethod: forumpb.ForumUser_GetUserInfo_FullMethodName,
        RouteKey:   req.GetGuid(),
        Response:   out,
    }); err != nil {
        return nil, err
    }
    return out, nil
}
```

### 3.2 ActionEngine

```go
type Call struct {
    Endpoint, FullMethod, RouteKey string
    Response                       proto.Message
}

func (e *ActionEngine) HandleUnary(ctx context.Context, call Call) error
```

`route_key` 为空返回 `codes.FailedPrecondition`；查 rule 失败同样 `FailedPrecondition`；命中后 `rule.Action.Execute(call.Response)`。response 容器由薄壳创建，因此同一 action 实现可处理任意类型——Engine 不做任何类型断言，不依赖 pb 包。仅支持 unary RPC。

### 3.3 MethodRegistry 与 Endpoint

endpoint 静态声明它承载的 method，供 control API 校验 `endpoint + full_method` 存在并取得 response 类型用于校验 protobuf bytes：

```go
type MethodIdentity struct{ Endpoint, FullMethod string } // 既嵌入 MethodSpec，也作 registry map key
type MethodSpec struct {
    MethodIdentity
    NewResponse func() proto.Message
}

type Endpoint interface {
    ID() string
    Register(s *grpc.Server, deps EndpointDeps) // EndpointDeps{Engine *ActionEngine}
    Methods() []MethodSpec
}
```

endpoint、service、method 全部代码静态定义。一个外部进程地址上的多个 proto service 注册到同一 endpoint。

### 3.4 Action data schema

**static_response** — 每次命中返回同一份 protobuf：

```json
{ "kind": "static_response", "data": { "resp": "<base64 protobuf bytes>" } }
```

**sequence_response** — 按访问次数返回不同 response，调用计数是 rule 级状态：

```json
{
  "kind": "sequence_response",
  "data": {
    "responses": [ {"resp": "...first..."}, {"resp": "...second..."} ],
    "on_exhausted": "repeat_last"
  }
}
```

`on_exhausted`：`repeat_last` 超出后返回最后一份；`error` 超出后返回 gRPC error，默认 `FailedPrecondition`，可用可选 `on_exhausted_err`（`{code, message}`）自定义，与 `once.on_second_call` 同形。`repeat_last` 下忽略该字段。

**once_response** — 只允许命中一次，第二次起返回指定 error：

```json
{ "kind": "once_response", "data": { "resp": "...", "on_second_call": { "code": "AlreadyExists", "message": "..." } } }
```

**error** — 直接返回 gRPC error，`code` 取 `codes.Code` 名称字符串（`NotFound` / `AlreadyExists` / ...）：

```json
{ "kind": "error", "data": { "code": "NotFound", "message": "forum user not found" } }
```

## 4. HTTP 协议层

### 4.1 Route Binding 与 Endpoint

HTTP 无 typed handler，但仍保留"已知路由表"以保证 fail loud。endpoint 即 host 本身，无独立 ID：

```go
type RouteIdentity struct{ Host, Method, Path string } // 既嵌入 Route，也作 registry map key
type Route struct {
    RouteIdentity                                       // 作者只填 Method+Path，Host 由 registry 从 Endpoint.Host() 回填
    GenRouteKey func(*http.Request) (string, error)
}

type Endpoint interface {
    Host() string
    Routes() []Route
}
```

作者若写了非空 `Host` 但与 `Endpoint.Host()` 不一致，构建 registry 时报错。复杂提取直接在 binding 里写 Go，不引入声明式 DSL。endpoint 不持有 Engine 引用——proxy 收到请求后按 Host 定位 endpoint，按 `(method, path)` 找 route，调 `GenRouteKey` 后交给 Engine。

### 4.2 ActionEngine

```go
type Call struct {
    Endpoint, Method, Path, RouteKey string
    W http.ResponseWriter
    R *http.Request
}

func (e *ActionEngine) HandleHTTP(call Call) error
```

`route_key` 为空或查 rule 失败一律写 `502`（不返回 mux 默认的 404/405），并记调用日志 `matched:false`。ctx 经 `call.R.Context()` 取，不额外加字段。`RouteRegistry` 复用 `RouteIdentity` 作 map key，校验 `endpoint + method + path` 是否注册。HTTP 的 request / response 都是字节流，注册阶段只能校验 status / headers / body 形状，没有 gRPC 那样的 response 类型校验。

### 4.3 Action data schema

单条 response 形状统一为 `{ "status", "headers", "body_base64" }`，注册阶段校验 `status ∈ [100,599]`、`headers` 是 string→string、`body_base64` 可 decode。

**static_response**：

```json
{ "kind": "static_response", "data": { "status": 200, "headers": {"Content-Type": "application/json"}, "body_base64": "..." } }
```

**sequence_response**：`on_exhausted` 同 gRPC；`error` 默认返回 `502`，可用可选 `on_exhausted_resp`（形状同单条 response）自定义，与 gRPC 的 `on_exhausted_err` 对称。这是"HTTP 错误即带特定 status 的响应"约定的延伸，非新增 error kind。

```json
{
  "kind": "sequence_response",
  "data": {
    "responses": [ {"status": 200, "headers": {}, "body_base64": "..."}, {"status": 503, "headers": {}, "body_base64": "..."} ],
    "on_exhausted": "error",
    "on_exhausted_resp": { "status": 429, "headers": {"Retry-After": "30"}, "body_base64": "..." }
  }
}
```

**once_response**：首次返回 `response`，第二次起返回 `on_second_call`（两者均为完整 response 形状）：

```json
{ "kind": "once_response", "data": { "response": {"status": 200, ...}, "on_second_call": {"status": 409, ...} } }
```

**无 error kind**：HTTP 下任何错误响应都是带特定 status 的 `static_response`。这是与 gRPC 的明确不对称。

### 4.4 MITM 代理

整个 faked 只暴露一个 HTTP 代理 listener，所有 HTTP host 共享：

- 监听 `127.0.0.1:<port>`，写入 `runtime.json` 的 `http.proxy_addr`。
- 处理 CONNECT 隧道：动态用 CA 签发叶证书，完成 TLS 握手并接管明文流；明文 HTTP 直接代理。
- 按 Host 分发到对应 endpoint 的 mux。未注册 host / path（含 method 不符）/ rule，或 `route_key` 提取失败 → 统一收敛为 `502` + 写日志 `matched:false`。
- 强制 HTTP/1.1（TLS NextProtos 只通告 `http/1.1`），简化代理实现。
- 支持 keep-alive：明文与隧道都在同一连接上循环处理多请求，每请求处理后 drain body 维持边界，决定不复用时回 `Connection: close`。
- 不支持 streaming / SSE / chunked，响应一次性写出。

### 4.5 CA Bundle 与 HTTPS 信任

> **【已修订：faked 内嵌】** faked 迁入 mtkit、内嵌 daemon 主进程后：CA 由 daemon 进程内生成/加载，cert 路径暴露到 `mtkit.runtime.json` 的 `faked.ca_cert_file`（非独立 faked runtime 文件）；信任路径**统一为「SUT 程序级装入 `RootCAs`」**，弃用 `SSL_CERT_FILE`（仅 Linux 生效）与「导入系统信任库」脚本路径。详见 `faked-integration-plan.md` §7.4。CA Subject CommonName 改通用值（不含项目名）。

faked 提供**双模式 CA**。消费者（SUT / 测试 / 调试工具）无须感知模式，统一从 `mtkit.runtime.json` 的 `faked.ca_cert_file` 拿绝对路径，程序级装填 `RootCAs`（§7.4）。

- **动态模式（默认）**：`ca_cert_file` 与 `ca_key_file` 都为空时启用。daemon 调 `ca.Generate(<RuntimeDir>)` 现场生成 RSA 2048 自签 CA，落到 `<RuntimeDir>/faked/generated-ca.{crt,key}`，cert 路径暴露到 runtime。CA 24 小时有效、每次启动新生成，适合 CI / 一次性 dev——无须管理 CA 生命周期。**本期采用此模式**。
- **静态模式**：两字段都填时启用（经 `mtkit.config.json` 的 `faked.http.ca_cert_file` / `ca_key_file`，无独立 env 覆盖）。daemon 走 `ca.LoadFromFiles` 严格加载。
- **半填配置**（只填一个）daemon 拒绝启动，避免静默退化覆盖用户意图。

`faked/http/ca/ca.go` 只提供 `Generate` 与 `LoadFromFiles`，不持有任何 PEM 字节、不 `//go:embed`——CA 数据与框架代码彻底解耦。

#### 4.5.1 SUT 信任装填（HTTPS MITM 能成立的核心另一半）

MITM 代理握手时用根 CA 私钥**现场 per-host 签发叶证书**冒充服务端；这只有在 **SUT 信任那张根 CA** 时才能完成握手。否则代理截得到流量却建不起 TLS——HTTPS faking 失效。故「SUT 怎么信任」与「faked 怎么签」是同一机制的两半，一并记在此。

装填方式：**SUT 程序级装入 `RootCAs`**，跨平台一致、零部署、零系统库污染。

- **一张根 CA 通吃所有域名**：叶证书握手时动态签发，SUT 从不需预知任何叶证书——只验证「叶证书能否链到它信任的根 CA」。新增 HTTPS 依赖只注册 endpoint，CA 不变。
- **SUT 只拿根 CA 公钥**：从 `mtkit.runtime.json` 的 `faked.ca_cert_file`（`.crt`）读，装入进程内存的 `RootCAs`；私钥只在 daemon 进程内签发用，从不出 daemon、从不进仓库。
- **用动态 CA**（每次跑临时生成、用完即弃），避免静态 CA 共享私钥成 MITM 后门。
- **跨平台统一**：程序级 `RootCAs` 在 Win/Linux/mac 行为一致，无系统信任库导入、无平台分支、不依赖 `SSL_CERT_FILE`（仅 Linux 生效，已弃用）。

落地形态：mtkit 在 `pkg/mtkit/addons/mtkit_ca_addon.go` 提供模板插件（`package main` + `//go:build mtkit_it`，`init` 读 `MTKIT_CA_CERT_FILE` 把公钥追加进 `http.DefaultTransport` 的 `RootCAs`，从 `SystemCertPool` 起步不丢系统根）。SUT 把该文件**拷贝**到自己的 `cmd/<sut>/` 即接入（main 包文件，init 自动执行，无需 import）；boot plan 构建 SUT 时加 `-tags mtkit_it`，生产构建逐字节不含。**接线见 `faked-integration-plan.md` §7.4 与 `pkg/mtkit/addons/README.md`**。

两条限制：① 只覆盖走 `http.DefaultTransport` 的 client；自建 `http.Transport` 并显式设 `TLSClientConfig` 的 client 需自行装 `RootCAs`。② `init` 在 `main()` 前执行，无并发风险，但 SUT 不应在 init 阶段发起需被 fake 的外部 HTTPS 请求。

## 5. RuleStore

每协议一份独立 store，互不共享。内部 map 以结构化 `RuleKey` 为键，不用字符串拼接判重。store 自身保证并发安全；action 状态（sequence / once 计数）按 rule 隔离、加锁保护。

```go
// faked/grpc
type Rule struct {
    Endpoint   string `json:"endpoint"`
    FullMethod string `json:"full_method"`
    RouteKey   string `json:"route_key"`
    Action     Action `json:"action"`
}
// faked/http: 把 FullMethod 换成 Method + Path

type Action struct {
    Kind string          `json:"kind"`
    Data json.RawMessage `json:"data"`
}
```

## 6. Control API

control API 按协议拆 path 前缀，不做鉴权。**内嵌后这些 handler 挂在 daemon 主 mux**（与 lease / fixtured 共用 `daemon.addr`），非 faked 独立 listener（见 §13 step 5、`faked-integration-plan.md` §5.2）；下列 path 与请求/响应契约不变：

```text
POST /v1/grpc/rules
POST /v1/http/rules
```

请求体即 §5 的 `Rule`（gRPC 用 `endpoint/full_method/route_key`，HTTP 用 `endpoint/method/path/route_key`），`action` 见各协议 schema。成功响应：

```json
{ "ok": true, "rule_id": "forum|/protos.ForumUser/GetUserInfo|user_001" }
```

错误码：

- 通用：`400 invalid_endpoint` / `unsupported_action` / `invalid_action_data` / `invalid_route_key`，`409 duplicate_rule`。
- gRPC 专用：`400 invalid_method`（full_method 未注册到该 endpoint）/ `invalid_resp`（`resp` 存在但 base64 / unmarshal 失败）。
- HTTP 专用：`400 invalid_route`（method+path 未注册）/ `invalid_status` / `invalid_body`。

第一版只提供注册接口，无查询 API；调试依赖调用日志和启动日志。

## 7. Runtime 文件（**已修订：faked 内嵌，无独立 runtime 文件**）

> faked 内嵌 daemon 后**不再写独立 `faked.runtime.json`**：地址由 daemon 直接写入 `mtkit.runtime.json` 的 `faked` 段；`control_addr` 是 daemon 主端口（faked control 挂主 mux，非独立 listener）。下列字段即 `mtkit.runtime.json.faked` 的形状（见 `faked-integration-plan.md` §3）：

```json
// mtkit.runtime.json 节选
{
  "control_addr": "127.0.0.1:18081",   // daemon 主端口；faked rule 注册打 /v1/{grpc,http}/rules
  "faked": {
    "grpc_endpoints": { "MSSIOT_IT_RPC_FORUM_ADDR": "127.0.0.1:31001" },
    "proxy_addr": "127.0.0.1:31900",
    "ca_cert_file": "/…/runtime/faked/generated-ca.crt"
  }
}
```

调用日志（`<runtime_dir>/faked/{grpc,http}_calls.jsonl`）路径按约定派生，daemon 进程内持有引擎、无需经文件传递地址。原「写 `.tmp` 再 rename 作就绪信号」对独立 faked 进程才必要；内嵌后 `Engine.Start` 返回即就绪。

## 8. SUT 环境注入（当前未启用）

SUT 尚未具备读取以下 env 的逻辑，故暂不注入 fake 相关变量（只注入容器依赖 env）。待 SUT 完成消费侧改造后启用：daemon 从引擎地址产出下列 faked env，与容器连接 env 合并后经 `StartSUTInput.Env` 统一注入 SUT、并落 `mtkit.runtime.json` 的 `sut_env` 段（详见 `faked-integration-plan.md` §7.1/§7.2）：

```text
MSSIOT_IT_RPC_FORUM_ADDR=127.0.0.1:31001   # 每个 enabled gRPC endpoint 一个
HTTP_PROXY=http://127.0.0.1:31900
HTTPS_PROXY=http://127.0.0.1:31900
NO_PROXY=127.0.0.1,localhost               # 排除回环，避免 SUT 自身健康检查 / 内部 RPC 被代理
MTKIT_CA_CERT_FILE=/…/faked/generated-ca.crt   # 程序级装入 RootCAs（弃用 SSL_CERT_FILE，§7.4）
```

## 9. 调用日志

每协议各写一份 JSONL，路径派生自 `runtime_dir` 并写入 runtime.json。命中记录含 `ts/endpoint/route 标识/route_key/rule_id/matched:true/action_kind`（HTTP 额外含 `status`）；未命中记录 `matched:false` + `error`，且不含 `rule_id`。

```json
{ "ts": "...", "endpoint": "forum", "full_method": "/protos.ForumUser/GetUserInfo", "route_key": "user_001",
  "rule_id": "forum|/protos.ForumUser/GetUserInfo|user_001", "matched": true, "action_kind": "static_response" }
```

调用日志用于排查路由 / 响应问题，不作默认断言接口——测试优先断言 response、前门反向读和 observe helper。

## 10. 测试侧 Client

测试通过 `client` 包向 control API 注册 rule。`client` 合一，统一 `Client` 入口；`GRPC()` / `HTTP()` 只是按被 fake 协议划分的 rule builder 命名空间，底层都走 HTTP control API。`client` 只 import 两侧 `protocol` 子包（纯 DTO），不拽服务端代码进测试 binary。

### 10.1 入口

> **【已修订：faked 内嵌】** client（`mtkit/client/fake`）经 **`mtkit.runtime.json.control_addr`**（daemon 主端口）发现 control API，不再有 `FAKED_RUNTIME` / `ResolveRuntimeDir` 发现链。

```go
func New() *Client                                       // 经 mtkit.runtime.json 发现（解析失败 panic）
func NewFromControlAddr(addr string) (*Client, error)    // 直接给 daemon control 地址
func (c *Client) GRPC() *grpcClient                       // 返回类型未导出，只能链式访问
func (c *Client) HTTP() *httpClient
func (c *Client) Addr() string
```

`Client` 内部持有未导出的 control transport（拿 daemon `control_addr`、POST `/v1/{grpc,http}/rules`、解码统一错误体 `*APIError`）。`GRPC()` / `HTTP()` 返回的命名空间类型刻意不导出——链式调用是唯一对外接口。

### 10.2 gRPC 链式 API

`On(endpoint, fullMethod)` 描述 route 坐标，`RouteKey()` 单独填 route_key，一条链一条 rule。链中编码错误（nil proto、marshal 失败）挂到 builder 上，统一在 `Apply(t)` 里 `t.Fatal`。`Apply` 内部用 `context.Background()` 提交，靠 transport 的 10s timeout 兜底。

```go
On(endpoint, fullMethod) → RouteKey(key) → { Static / Sequence / Once / Error } → ... → Apply(t)

func (b *grpcRuleBuilder) Static(resp proto.Message) *grpcStaticBuilder
func (b *grpcRuleBuilder) Sequence() *grpcSequenceBuilder
func (b *grpcRuleBuilder) Once(resp proto.Message) *grpcOnceBuilder
func (b *grpcRuleBuilder) Error(code codes.Code, msg string) *grpcErrorBuilder

func (b *grpcSequenceBuilder) Add(resp proto.Message) *grpcSequenceBuilder
func (b *grpcSequenceBuilder) RepeatLastOnExhausted() *grpcSequenceBuilder
func (b *grpcSequenceBuilder) ErrorOnExhausted(code codes.Code, msg string) *grpcSequenceBuilder
func (b *grpcOnceBuilder) ThenError(code codes.Code, msg string) *grpcOnceBuilder
```

```go
ctl := fakedclient.New()

ctl.GRPC().On(fakegrpc.EndpointForum, forumpb.ForumUser_GetUserInfo_FullMethodName).
    RouteKey(uid).Static(resp).Apply(t)

ctl.GRPC().On(endpoint, fullMethod).RouteKey(uid).
    Sequence().Add(resp1).Add(resp2).RepeatLastOnExhausted().Apply(t)

ctl.GRPC().On(endpoint, fullMethod).RouteKey(uid).
    Once(resp).ThenError(codes.AlreadyExists, "fake response already consumed").Apply(t)
```

### 10.3 HTTP 链式 API

HTTP 单条 response 是 status + headers + body 组合，单独提供 `Resp()` 构造，`Static` / `Sequence.Add` / `Once.ThenRespond` / `ErrorOnExhausted` 都复用它。HTTP 无 `Error` action——错误响应用带特定 status 的 `Static` / `Sequence` / `Once` 表达。

```go
func Resp() *HTTPRespBuilder
func (b *HTTPRespBuilder) Status(int) / Header(k,v) / Headers(map) / Body([]byte) / JSONBody(any) / FormBody(url.Values) *HTTPRespBuilder

On(endpoint, method, path) → RouteKey(key) → { Static / Sequence / Once } → ... → Apply(t)

func (b *httpRuleBuilder) Static(resp *HTTPRespBuilder) *httpStaticBuilder
func (b *httpRuleBuilder) Sequence() *httpSequenceBuilder
func (b *httpRuleBuilder) Once(resp *HTTPRespBuilder) *httpOnceBuilder

func (b *httpSequenceBuilder) Add(resp *HTTPRespBuilder) *httpSequenceBuilder
func (b *httpSequenceBuilder) RepeatLastOnExhausted() *httpSequenceBuilder
func (b *httpSequenceBuilder) ErrorOnExhausted(resp *HTTPRespBuilder) *httpSequenceBuilder
func (b *httpOnceBuilder) ThenRespond(resp *HTTPRespBuilder) *httpOnceBuilder
```

```go
ctl.HTTP().On(fakehttp.HostGooglePay, http.MethodPost, "/o/oauth2/token").
    RouteKey(clientID).Static(fakedclient.Resp().Status(200).JSONBody(body)).Apply(t)

ctl.HTTP().On(host, http.MethodPost, path).RouteKey(clientID).
    Sequence().
    Add(fakedclient.Resp().Status(200).JSONBody(v1)).
    Add(fakedclient.Resp().Status(503).Header("Retry-After", "30").Body(raw)).
    ErrorOnExhausted(fakedclient.Resp().Status(429).JSONBody(errBody)).Apply(t)

ctl.HTTP().On(host, http.MethodPost, path).RouteKey(clientID).
    Once(fakedclient.Resp().Status(200).JSONBody(ok)).
    ThenRespond(fakedclient.Resp().Status(409).JSONBody(errBody)).Apply(t)
```

不提供 `RegisterForumGetUserInfo` 这类业务注册 helper——endpoint 常量、typed 薄壳和 route 声明在 `test/fake/{grpc,http}/`，测试用例直接用 `fakedclient.New()` 组合请求，避免 helper 层为每种 action 变体重复封装、避免 package-level client 变量带来的并发与生命周期负担。

## 11. Endpoint 注册

每个外部服务一个文件，集中存放 endpoint 定义 + typed 薄壳（仅 gRPC）+ route 声明。endpoint 通过 `init()` 自注册到协议子包的 package-level 列表，无显式应用侧 registry 文件。

```go
// test/fake/grpc/forum.go — package grpc（按需 alias 为 fakedgrpc）
func init() { fakedgrpc.RegisterEndpoint(&forumEndpoint{}) }
func (forumEndpoint) ID() string { return EndpointForum }
func (forumEndpoint) Register(s *googrpc.Server, deps fakedgrpc.EndpointDeps) {
    forumpb.RegisterForumUserServer(s, &forumUserService{Engine: deps.Engine})
}
func (forumEndpoint) Methods() []fakedgrpc.MethodSpec { /* MethodIdentity + NewResponse */ }

// test/fake/http/googlepay.go — package http（按需 alias 为 fakedhttp）
func init() { fakedhttp.RegisterEndpoint(&googlepayEndpoint{}) }
func (googlepayEndpoint) Host() string { return HostGooglePay }
func (googlepayEndpoint) Routes() []fakedhttp.Route {
    return []fakedhttp.Route{{
        RouteIdentity: fakedhttp.RouteIdentity{Method: nethttp.MethodPost, Path: "/o/oauth2/token"}, // Host 留空回填
        GenRouteKey:   func(r *nethttp.Request) (string, error) { return routekey.FormValue(r, "client_id") },
    }}
}
```

daemon 入口用 `import _` 触发 init()：

```go
import (
    _ "mssiot_user/test/fake/grpc"
    _ "mssiot_user/test/fake/http"
)
```

新增外部 gRPC service 必须引入对应 pb 包、注册 generated service、提供 typed 薄壳；新增外部 HTTP 服务必须实现 `Endpoint` 接口并声明 route binding。**不通过配置动态创建未知 endpoint / service / method / route。**

## 12. 启动参数与配置

### 12.1 引擎配置（内嵌，无独立 bootstrap）

> **【已修订：faked 内嵌】** faked 不再是独立被调用进程——无 `-c`/`-r` flag、无 `FAKED_*` env、无独立 `config.json`/`runtime.json`。引擎参数是 `mtkit.config.json` 的 `faked` 块（见 `faked-integration-plan.md` §2），由 daemon 进程内构造引擎时传入；调用日志与生成 CA 落 `<mtkit.runtime_dir>/faked/`。本节描述引擎**消费的配置形状**，不再描述独立进程的 bootstrap。

引擎配置（`mtkit.config.json` 的 `faked` 块，全部可选）：

```json
"faked": {
  "enabled": true,
  "grpc": { "endpoints": [ { "id": "forum", "listen": "127.0.0.1:0" } ] },
  "http": { "proxy_listen": "127.0.0.1:0", "ca_cert_file": "", "ca_key_file": "" }
}
```

- `env_key` 不在配置里——移到 `Endpoint.EnvKey()` 接口方法（`faked-integration-plan.md` §9.2）。
- `grpc.endpoints` 仅用于**覆盖某 endpoint 的监听地址**；省略则全部 `init()` 注册的 endpoint 以随机端口启用。
- 删 `http.endpoints`（host 列表）：HTTP endpoint 启用由 `init()` 自注册唯一决定（§9.3）。
- `ca_cert_file` / `ca_key_file` 省略走动态 CA，都填走静态；半填拒绝启动。
- control 不在此配端口——并入 daemon 主 mux（`mtkit.config.json` 的 `daemon.addr`）。

## 13. 启动流程

> **【已修订：faked 内嵌】** 启动由 daemon 进程内驱动（`faked.New` + `Engine.Start` + `RegisterControl`），非独立进程 bootstrap。

```text
1. daemon 据 mtkit.config.json 的 faked 块构造 faked.Config（RuntimeDir = <mtkit.runtime_dir>/faked）。
2. 加载静态 gRPC / HTTP endpoint registry（项目 TestMain blank-import 触发 init() 自注册）；收集 MethodRegistry / RouteRegistry。
3. 校验配置中 grpc.endpoints[].id 已静态注册。
4. faked/http 按动态 / 静态模式准备 CA Bundle。
5. faked.Engine.RegisterControl(daemonMux)：两侧 control handler 挂 daemon 主 mux（/v1/{grpc,http}/rules），不开独立 control listener。
6. faked.Engine.Start：为每个 enabled gRPC endpoint 启动 grpc.Server；启动 HTTP MITM proxy——返回 Addrs。
7. daemon 把 Addrs 写入 mtkit.runtime.json 的 faked 段（与 lease/fixtured 同文件）。
8. 测试 runtime 经 mtkit.runtime.json.control_addr 用 client 注册 rule；SUT env 注入见 §8（暂未启用）。
9. SUT 发起 gRPC / HTTP 请求 → 命中 ActionEngine → 执行 action。
```

## 14. 校验策略

**注册阶段** — 通用：endpoint 已启用、`route_key` 非空、action kind 支持、action data 符合 schema、`RuleKey` 未重复。gRPC 额外：`full_method` 已注册到该 endpoint；`resp` 存在非空（缺失归 `invalid_action_data`）、可 base64 decode 且能 unmarshal 到目标 response 类型（失败归 `invalid_resp`）；static / sequence / once 对缺失 `resp` 归类一致。HTTP 额外：`method+path` 已注册、`status ∈ [100,599]`、`headers` 是 string→string、`body_base64` 可 decode。

> `invalid_action_data` 表达"结构 / 必填字段不符"，`invalid_resp` 专指"`resp` 存在但 base64 / unmarshal 失败"。

**请求命中** — 通用：协议层构造的 `route_key` 非空、rule 存在、action 状态合法。gRPC 失败返回 `FailedPrecondition` 或 action 指定的 error；HTTP 失败返回 `502` + 日志 `matched:false`。

## 15. 与 CaseScope / BindToScope 的关系

faked 不接收 test case id，也无法证明某次 SUT 调用来自哪个 case——它只知道"某条 rule 是哪段测试代码注册的"。并发隔离的关键在测试数据层：

```text
CaseScope 生成唯一业务数据 → Seeder 创建会出现在外部请求里的字段（可用 BindToScope 记录 field/value↔case）
→ 测试用例用同一 value 注册 fake rule → SUT 调 fake 时 request 携带该 value
→ 协议层提取 value 构造 route_key
```

返回全局静态数据的 method / route 用 `routekey.Static()` 构造稳定 key；这类 rule 能否并发共享由测试包并发策略、`t.Parallel()` 和注册时机决定。

## 16. 目录结构

> **【已修订：faked 内嵌】** 下为迁移后的终态布局——引擎入 `mtkit/faked`、client 入 `mtkit/client/fake`、自测随引擎入 mtkit；项目侧只剩 endpoint。`mssiot_user/test/infra/faked/` 整目录删除。

```text
# ── mtkit 模块（github.com/McHarvvvy/mtkit）──────────
pkg/mtkit/
├── faked/                          # 引擎本体（内嵌 daemon 调用）
│   ├── server.go                   # faked.New / Server.Start / RegisterControl / Stop（由原 daemon.go 改造）
│   ├── config.go  runtime.go       # 引擎 Config（消费 mtkit.config.faked）+ Addrs / runtime 段装配
│   ├── grpc/                       # package grpc（alias fakedgrpc）
│   │   ├── protocol/{rule,action,error,runtime}.go
│   │   ├── routekey/routekey.go    actions/{static,sequence,once,error}_response.go
│   │   ├── store.go  engine.go  parser.go
│   │   └── registry.go（Endpoint 接口含 EnvKey()）  control_api.go  call_log.go  server.go
│   ├── http/                       # package http（alias fakedhttp）
│   │   ├── protocol/{rule,action,error,runtime}.go
│   │   ├── routekey/routekey.go    actions/{static,sequence,once}_response.go
│   │   ├── ca/ca.go                # Generate + LoadFromFiles，无 PEM 数据，CA Subject 用通用名
│   │   ├── store.go  engine.go  parser.go
│   │   └── registry.go  control_api.go  call_log.go  proxy.go
│   └── test/                       # 引擎自测（随引擎入 mtkit 模块）
│       ├── acceptance/*_test.go    smoke/...  internal/{testpb,testendpoint}/
├── client/fake/{control.go, grpc.go, http.go}  # 统一 Client（经 mtkit.runtime.json.control_addr）
└── orchestrator/...                # daemon 进程内持 *faked.Engine，编排启停（§6 plan）

# ── 项目模块（mssiot_user）──────────
test/
├── infra/run.go / sut.go / containers.go  # Run(m)：New mtkit + Register* + blank-import endpoint；不再 fork faked
└── fake/                                  # 项目 endpoint（仅改 import 路径）
    ├── grpc/forum.go                      # init() 自注册；实现 EnvKey()
    └── http/googlepay.go
```

`faked/grpc` 与 `faked/http` 互不 import，只被 `faked/`（engine）与 `mssiot_user/test/fake/{grpc,http}/` 业务包 import。`mtkit/client/fake` 只 import 两侧 `protocol` 子包（纯 DTO），不拽服务端代码进测试 binary 的 client 路径。endpoint 经项目 `TestMain` blank-import 自注册——daemon == 测试二进制，故引擎可见这些 endpoint（无独立 `cmd/faked`，§见 plan §0.3.2）。

## 17. 明确不做

- 不修改 SUT 源码加测试钩子；不注册跨进程 Go handler；不用全局覆盖式 handler 支撑并发。
- ActionEngine 不写业务分支、不从 request 反射提取字段。
- 不提供 rule 覆盖接口、不提供调用查询 API、不做 control API 鉴权。
- 不让测试用例手写 base64；不静默返回空 response。
- 不通过配置动态创建未知 endpoint / service / method / route。
- 不在 `client` 包提供自由函数风格 helper（统一用方法）；不引入显式应用侧 registry 文件（用 `init()` + `import _`）。
- HTTP：无 `error` action kind（用带 status 的 `static_response`）；不支持 streaming / SSE / chunked；不模拟连接级故障（reset、握手失败、TLS alert）。
- gRPC：仅 unary，不支持 streaming；endpoint 仅明文（h2c），不实现 server-side TLS——与 HTTP 侧的 MITM + 动态签发不对称。当前测试客户端一律 insecure dial；待 SUT 集成阶段若有强制 TLS 的真实外部 gRPC 依赖，再评估给 endpoint 加 TLS（可复用同一 CA Bundle）。
