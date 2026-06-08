# `faked` 独立服务 Acceptance 测试计划

> 本计划面向 `test/infra/faked/` 这个独立的 fake daemon 服务（设计见 `faked-design.md` §1-19）。
> 输出三段式：业务事实清单 → 测试策略 → 用例清单，末尾含"为什么不写"清单与实施提示。
> 测试代码统一落到 `test/infra/faked/test/` 子树下，**不污染**业务侧 `test/fake/{grpc,http}/` 与 daemon 主入口 `cmd/main.go`。

---

## 一、业务事实清单

> 离开源码、纯业务语言描述 faked 对消费者（测试用例 / SUT / 排错人员）的承诺。每条用例须可追溯至此清单某条。

1. **注册的 rule 能被匹配请求命中**——通过 control API 注册一条 rule 后，相同 `endpoint + method/full_method + route_key` 的请求会拿到注册的 response 内容。
2. **不匹配任何 rule 的请求 fail-loud**——gRPC 返回 `FailedPrecondition` / HTTP 返回 502，不静默返回空响应。
3. **非法 rule 在注册阶段被拒绝**——未知 endpoint / 未知 method 或 route / 重复 RuleKey / action data 不符合 schema / gRPC response bytes 不能 unmarshal 到目标类型——control API 返回 4xx，rule **不入 store**（事后请求仍 fail-loud）。
4. **stateful rule 状态按 rule 隔离**——`sequence_response` / `once_response` 的计数绑定到具体 RuleKey；多条 rule 交叉调用不串扰；并发命中同一条 rule 不丢不重。
5. **`runtime.json` 写出 = 所有 listener 就绪**——消费者读到该文件后，可直接拨号 control API、所有 gRPC endpoint、HTTP MITM 代理，不需要再做 health check。
6. **所有 fake 请求（命中 + 未命中）会触发调用日志写入**——日志文件位于 `runtime.json` 声明的路径，每次请求至少新增一条记录。**字段顺序 / JSON 形态 / ts 精度属于实现细节，不进契约**。测试按本用例唯一 `route_key`（或 host / path）在 jsonl 中定位本次请求对应的记录行，断言其 `matched` 与（命中时）`route_key` / `status` / `action_kind` 等可归因字段——共用 daemon 下日志是同一份文件，"文件增长"无法归因到本请求，故改为内容级按 key 命中。
7. **HTTPS 经 MITM 代理时 CA 信任链可用**——代理用配置中的 CA 动态签发叶证书；客户端用同一份 CA cert（路径由 `runtime.json.http.ca_cert_file` 暴露）即可信任。
8. **进程生命周期可控**——`-c`/`-r` 命令行参数与 `FAKED_CONFIG` / `FAKED_RUNTIME` 环境变量任一来源非空即可启动；两者同时存在时 env 优先；两者皆缺才启动失败（非零 exit）；优雅终止信号下退出前会 flush 调用日志。

> 代码里看到、不在清单里的（如"内存 map 的 sync.RWMutex"、"具体错误字符串 `no fake rule`"、"日志行 ts 的精度"、"action 的内部分发函数名"）= 实现机制，不进用例。

---

## 二、测试策略摘要

### 被测对象与范围

`test/infra/faked/` daemon（独立进程）整条对外能力面：control API + gRPC endpoint server set + HTTP MITM proxy + runtime.json 协议 + 调用日志 + bootstrap/lifecycle。

### 测试形态：双层

| 层 | 形态 | 覆盖 | 占比 |
|---|---|---|---|
| **acceptance（主力）** | **in-process** 启 `faked.Daemon`（**整个测试包共用一个**，TestMain 启停），go test 进程内同时充当 control client + "SUT 角色" | 业务事实 1-7（除生命周期） | ~85% |
| **smoke（兜底）** | **fork** 编译后的 daemon binary，go test 通过 control API + 真实拨号驱动 | 业务事实 5 / 8（生命周期、bootstrap、信号） | ~15% |

> acceptance 改为**共用 daemon**：更贴近 faked 真实使用（一个 daemon 服务整个套件、多 endpoint、
> 多规则、并发命中），能暴露每用例独占式跑法掩盖的退化。代价是边界隔离变弱，靠测试数据分桶补偿——
> 见隔离策略。fork 入口与生产入口共享 `bootstrap.Run()`，smoke 验证的是真实入口代码而非副本。

理由：in-process 覆盖广、可调试、coverage 易拿；fork 必须存在因为 `cmd/main.go` 的 flag 解析 / env 覆盖 / 信号处理 / 真实进程退出 in-process 无法验证。

### 依赖处理

| 依赖 | 策略 | 理由 |
|---|---|---|
| faked 自身的 `RuleStore` / `CallLogger` / `Proxy` / gRPC server | **真实** | mock 内部 IO 会让前门退化为假绿；这正是被测对象 |
| 测试用 gRPC service（`Echo.Ping` / `Echo.Whoami`） | **测试专用 proto + endpoint**（`test/internal/testpb` + `test/internal/testendpoint`），不复用业务侧 `forumpb` | faked 是通用包，acceptance 必须能脱离 mssiot_user 业务 endpoint 跑通 |
| 测试用 HTTP host（`acc.test.faked`） | **测试专用 endpoint**（同上） | 同上 |
| HTTPS CA | **测试自生成**（运行时用 `crypto/x509` 生成 CA cert+key 写到 `t.TempDir()`，daemon 与 client 双方都从此读） | CA cert 不是 faked 框架的强制部分，可能随项目演进（轮换、算法变更、不同项目复用 faked 时 CA 路径不同）；测试自带 CA 与仓库内 `test/infra/ca/` 解耦，框架可移植到其他项目时测试无外部依赖 |
| SUT 角色 | go test 代码直接用 `google.golang.org/grpc` 与 `net/http` 充当 | 第一版 SUT 注入侧未实现（§8），不引真实 SUT 进程 |

> 每处替换的理由都能回答"为什么不用真实 + 替换后还能暴露什么真实退化"。

### 数据策略

- **共享 fixture**：测试用 proto `EchoReq{Key, Payload}` / `EchoRes{Code, Msg, Echo}`，结构稳定，所有用例复用。
- **内联差异**：每条用例自行声明注册的 RuleKey、action kind 与 response payload；非法注册类用例 table-driven 表达差异点。
- **不可控输入**：日志 ts 不参与断言（机制）；端口全部 `:0` 由 OS 分配；runtime dir 用 `t.TempDir()`。

### 隔离策略

**acceptance：共用 daemon + 数据集合分桶**——

- 整个包共用一个 in-process daemon（TestMain 启停，runtime dir 用包级临时目录，端口 `:0`）。
- 每条用例用 `caseKey(t)` 派生唯一 `route_key`（`<sanitized t.Name()>-<进程内单调 seq>`）。
  规则常驻、无覆盖接口，唯一 key 同时避免跨用例撞 `409 duplicate_rule`、隔离 sequence/once 状态、
  并让调用日志可按 key 归因。seq 后缀保证 `go test -count=N` 同进程重跑不复用 RuleKey。
- 全部用例 `t.Parallel()`。
- **逃生舱**：少数断言"无 `route_key` 的全局副作用"、无法在共用日志里按 key 归因的用例
  （如 `GenRouteKey` 提取失败的 miss 日志），改用 `newIsolatedHarness(t)` 独占 daemon + 私有日志。
- `routekey.Static()` 这类固定 key 的路由只能做"注册一次的全局只读 fixture"，不可做 per-test 规则。

**smoke：进程级隔离**——每条用例自 fork 一个 daemon，`t.TempDir()` 做 runtime dir，端口 `:0`，测完自动清。

### 风险地图

- **变更点**：fake endpoint 与 testpb 是新增；`cmd/main.go` 的 bootstrap 已稳定但未被自动化测试覆盖过。
- **历史 bug 区**：null——首版尚无 bug 历史；`scripts/probes/smoke` 覆盖窄。
- **隐式边界**：HTTPS MITM + Windows `SSL_CERT_FILE` 不被 Go SystemCertPool 读取（探针注释明列）；Windows 没有 SIGTERM 投递语义，必须走 console control event。
- **横切契约**：第一版无鉴权（§6 明列不做），不构成测试维度。

---

## 三、用例清单

> **断言协议**：关键路径走三通道协同——control response（注册侧）+ 真实 SUT 角色拨号/HTTP 拿到响应（命中侧）+ 调用日志一行（业务级触达侧，事实 6）。
> **table-driven 标记**：标 ⚙ 的用例族建议在同一个 `_test.go` 文件内用表驱动表达；标 ◯ 的因为涉及独立的 client 装填 / 并发原语 / 进程操作，逐条手写更清晰。

### 维度 1 · 核心业务规则（→ 事实 1 / 6 / 7）

#### TC-01 gRPC 注册-命中-日志 三角验证 ◯

- 目的：保护"注册成功后匹配请求拿到注册 response + 调用日志记录命中"核心承诺
- Given：in-process Daemon 已启动；测试用 Echo endpoint 已自注册
- When：注册一条 `route_key=caseKey(t)` 的 PingRes；真实 `grpc.Dial` 调 `Echo.Ping(PingReq{Key:caseKey, Payload:"p"})`
- Then：(a) control API 返回 200 + 非空 `rule_id`；(b) gRPC 响应字段与注册值逐字段相等；(c) `grpc_calls.jsonl` 中存在一行 `matched==true && route_key==caseKey && action_kind==static_response`（按 key 归因，不约束字段顺序 / ts 等机制层细节）

#### TC-02 HTTP 注册-命中-日志 三角验证 ◯

- 目的：同 TC-01 的 HTTP 协议对应面。**主路径走 application/json**（SUT 主流格式），form-urlencoded 作并列次要子用例
- Given：in-process Daemon；测试 HTTP host `acc.test.faked` 已自注册。主路由 `POST /v1/login` 用 `routekey.JSONField("account_id")` 抽 key；次路由 `POST /token` 用 `routekey.FormValue("client_id")`
- When：注册 status=200 + JSON body 的 static_response；通过 daemon 代理 `POST http://acc.test.faked/v1/login`（`Content-Type: application/json`，body `{"account_id":caseKey}`）。form 子用例对 `/token` 发 `client_id=caseKey`
- Then：(a) control 返回 200；(b) HTTP 响应 status/headers/body 与注册值匹配；(c) `http_calls.jsonl` 中存在 `matched==true && route_key==caseKey && status==200` 行

#### TC-03 HTTPS MITM 命中（→ 事实 7） ◯

- 目的：保护 HTTPS 走 MITM 代理时 CA 动态签发 + 客户端信任整条链路
- Given：in-process Daemon；client 装填 `runtime.json.http.ca_cert_file` 到 `tls.Config.RootCAs`；走 `http.ProxyURL(代理地址)`
- When：注册一条 `POST /v1/login` 的 static_response；客户端 `POST https://acc.test.faked/v1/login`（application/json，body `{"account_id":caseKey}`）
- Then：TLS 握手成功；HTTP 响应匹配注册值；`http_calls.jsonl` 中存在 `matched==true && route_key==caseKey && status==200` 行

### 维度 2 · 异常路径与容错（→ 事实 2）

#### TC-04 fail-loud 等价类集 ⚙（建议 table-driven，5 条）

- 目的：保护"未命中不静默"核心承诺；维度 1 等价类全保留
- 表头：`name / protocol / setup / action / wantStatus`

| name | protocol | setup（注册了什么） | action（发了什么请求） | wantStatus |
|---|---|---|---|---|
| grpc-no-rule | gRPC | 不注册 | `Echo.Ping(Key="x")` | `FailedPrecondition` |
| grpc-empty-route-key | gRPC | 不注册 | `Echo.Ping(Key="")` | `FailedPrecondition` |
| http-unknown-host | HTTP | 不注册（host 静态注册了） | 经代理 `POST http://unknown.faked/token` | 502 |
| http-unknown-route | HTTP | 不注册 | 经代理 `POST http://acc.test.faked/unknown` | 502 |
| http-no-rule | HTTP | 不注册 rule（route 已声明） | 经代理 `POST http://acc.test.faked/token` (client_id=k1) | 502 |

> 每条断言：仅 SUT 角色看到的 status / gRPC code。
> **不在本表断言日志**——职责分离：所有 HTTP 未命中（未注册 host / 未注册 path / method 不符 / route_key 提取失败 / rule 未注册）现在都统一 502 + 写一条 `matched:false` 日志，日志验证集中在 `TestCallLog_MissesLogged`，本表只验响应状态。
> daemon 已把所有未命中分类收敛为 502（不再返回 mux 默认 404/405）；`http-unknown-route` 期望 502。

### 维度 3 · 输入校验（→ 事实 3）

#### TC-05 非法注册等价类集 ⚙（建议 table-driven，gRPC + HTTP 共 9 条）

- 目的：保护"非法 rule 立即拒绝且不入 store"承诺；断言每条对应的精确契约错误码
- 表头：`name / protocol / payload / wantCode / wantStatus`

| name | protocol | payload 核心差异 | wantCode | wantStatus |
|---|---|---|---|---|
| grpc-unknown-endpoint | gRPC | endpoint=`nonexist` | `invalid_endpoint` | 400 |
| grpc-unknown-method | gRPC | endpoint=echo / full_method=`/x/y/z` | `invalid_method` | 400 |
| grpc-empty-route-key | gRPC | route_key="" | `invalid_route_key` | 400 |
| grpc-unsupported-action | gRPC | action.kind=`bogus` | `unsupported_action` | 400 |
| grpc-invalid-action-data | gRPC | kind=static_response / data 缺 resp | `invalid_action_data` | 400 |
| grpc-invalid-resp | gRPC | resp base64 解码失败（或 unmarshal 不进 PingRes） | `invalid_resp` | 400 |
| http-unknown-endpoint | HTTP | host=`x.unknown` | `invalid_endpoint` | 400 |
| http-invalid-route | HTTP | host=acc.test.faked / method+path 未声明 | `invalid_route` | 400 |
| http-invalid-status | HTTP | status=600 | `invalid_status` | 400 |
| http-invalid-body | HTTP | body_base64 不可 decode | `invalid_body` | 400 |

> **每条断言**：`*APIError` 的 `Code` 与 `HTTPStatus` 与契约（设计 §6.4）一致；只放过自由文本
> `Message`（详细调试信息、非契约）。等价类表每行对应一个**不同的拒绝理由**——只断言 4xx 大类
> 会让不同 bug 命中同一 4xx 而漏检，等价类失去意义。
> 注：缺失 `resp` 归 `invalid_action_data`（schema 不符），`invalid_resp` 专指 resp 存在但 decode/unmarshal 失败。

#### TC-06 重复注册（→ 事实 3 中的 duplicate_rule 等价类） ◯

- 目的：保护"重复 RuleKey 失败、第一条仍生效"语义；此条不与 TC-05 合并因为涉及两次请求 + 状态断言
- Given：in-process Daemon
- When：用同一 RuleKey 注册两条不同 response 的 static_response；之后真实拨号命中
- Then：第一次注册 200；第二次返回 409；命中拿到的是**第一次**的 response（不是第二次）

### 维度 4 · 状态流转 & 幂等（→ 事实 4）

#### TC-07 stateful action 行为集 ⚙（table-driven，5 条）

- 目的：保护 sequence/once 单 rule 行为与 on_exhausted 语义
- 表头：`name / protocol / action / hits / wantResponses`

| name | protocol | action | hits | wantResponses |
|---|---|---|---|---|
| grpc-sequence-repeat-last | gRPC | sequence[r1,r2,r3] / on_exhausted=repeat_last | 5 | [r1,r2,r3,r3,r3] |
| grpc-sequence-error | gRPC | sequence[r1,r2] / on_exhausted=error(自定义 ResourceExhausted) | 4 | [r1,r2,err,err]，err.code==ResourceExhausted（验证耗尽错误可定制） |
| grpc-once | gRPC | once(r1, on_second_call={code:AlreadyExists}) | 3 | [r1, err(AlreadyExists), err(AlreadyExists)] |
| http-sequence-repeat-last | HTTP | sequence[200/a1,503/a2] / on_exhausted=repeat_last | 4 | [200/a1,503/a2,503/a2,503/a2] |
| http-sequence-error | HTTP | sequence[200,503] / on_exhausted=error(自定义 504/exhausted) | 3 | [200,503,504/exhausted]（验证耗尽响应可定制；未自定义时回退 502） |
| http-once | HTTP | once(200, on_second_call={status:409}) | 2 | [200,409] |

#### TC-08 跨 rule 状态隔离（→ 事实 4） ◯

- 目的：保护"两条 sequence rule（不同 route_key）交叉命中、各自计数独立"
- Given：注册 rule A (route_key=ka, sequence[a1,a2])、rule B (route_key=kb, sequence[b1,b2])
- When：交叉调用顺序 A→B→A→B
- Then：响应序列为 a1,b1,a2,b2

#### TC-09 单 rule 并发命中（→ 事实 4） ◯

- 目的：保护"并发命中同一 sequence rule 不丢不重"
- Given：单条 sequence rule，长度 N=10，on_exhausted=error
- When：M=50 个 goroutine 并发命中，等所有返回
- Then：成功响应集合（去重后）= 注册的 10 份（不断言顺序）；失败响应数 = 40

### 维度 5 · 副作用契约（调用日志，→ 事实 6）

> 命中 / 未命中的日志写入已被 TC-01/02/03/04 顺带覆盖（三角验证）。本节只补一条"日志路径契约"。

#### TC-10 调用日志路径与 runtime.json 声明一致（→ 事实 6） ◯

- 目的：保护排错人员从 runtime.json 定位日志文件的能力 + engine 命中/未命中都写日志
- Given：in-process Daemon 已启动；用唯一 hitKey / missKey 各执行一次命中 + 一次未命中（gRPC 与 HTTP 各一组）
- Then：`runtime.json.grpc.call_log_file` 与 `runtime.json.http.call_log_file` 声明的路径下文件存在；按 key 在两份 jsonl 中分别定位到 `matched==true(hitKey)` 与 `matched==false(missKey)` 行（命中 / 未命中都带 route_key，可归因；不约束字段顺序等机制层细节）

### 维度 6 · 进程生命周期（→ 事实 5 / 8，全部 smoke 形态）

> 全部走 fork。daemon binary 用 `test/smoke/cmd/`（blank-import 测试 endpoint）编译，避免依赖业务侧 `cmd/main.go` 拽进 `test/fake/`。

#### TC-11 fork happy path（→ 事实 5 / 8） ◯

- 目的：保护 bootstrap 全链路 + runtime.json 写出 = 就绪信号承诺
- Given：构造合法 `config.json` 落到 `t.TempDir()`；用 `-c`/`-r` flag 启动编译好的 daemon binary
- When：轮询等 `runtime.json` 出现；解析后立刻拨号 control_addr + grpc endpoints + http proxy_addr
- Then：三个 addr 均 3 秒内可建立连接；通过 control API 注册一条 rule 并命中成功

#### TC-12 bootstrap 参数解析（→ 事实 8） ⚙（table-driven，6 条）

- 目的：保护"flag 与 env 任一来源即可、env 优先、两者皆缺才失败"完整承诺
- 表头：`name / args / env / want`

| name | args | env | want |
|---|---|---|---|
| flag-only | `-c=A -r=B` | — | 启动成功，runtime.json 落到 B |
| env-only | （无 flag） | `FAKED_CONFIG=A FAKED_RUNTIME=B` | 启动成功，runtime.json 落到 B |
| env-wins | `-c=X -r=Y` | `FAKED_CONFIG=A FAKED_RUNTIME=B` | 启动成功，runtime.json 落到 **B**（不是 Y），证明 env 优先 |
| missing-config | 仅 `-r=B` | 无 `FAKED_CONFIG` | 非零 exit + stderr 非空 |
| missing-runtime | 仅 `-c=A` | 无 `FAKED_RUNTIME` | 非零 exit + stderr 非空 |
| bad-config-json | `-c=<dir with malformed config.json> -r=B` | — | 非零 exit + stderr 非空 |

> 成功行断言：runtime.json 在期望路径出现且可解析；通过 control_addr 能完成一次注册验证 daemon 真的活着。
> 失败行断言：exit code != 0、stderr 非空。**不断言** stderr 具体文案（机制层）。

#### TC-13 优雅退出（跨平台，→ 事实 8） ◯

- 目的：保护"优雅终止信号下退出前 flush 调用日志"承诺；Windows / Linux 行为对齐
- 平台分支用 build tag 隔离测试代码，单条用例两套实现：

| 平台 | 启动参数 | 发送信号 | 期望 |
|---|---|---|---|
| Linux | 默认 + `Setpgid:true`（独立 process group，避免 ctrl+c 同时杀测试进程） | `cmd.Process.Signal(syscall.SIGTERM)` | exit code 0；调用日志文件已被关闭且最后一行写完整 |
| Windows | `CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP` | `windows.GenerateConsoleCtrlEvent(syscall.CTRL_BREAK_EVENT, uint32(pid))` | 同上 |

- Given：fork daemon + 触发一次命中（在 jsonl 末尾留一行）
- When：发上述信号
- Then：daemon 3 秒内退出，`cmd.Wait()` 返回 exit code 0；调用日志文件可被重新打开读取，最后一行 JSON 可解析（验证 flush）

#### TC-14 强杀（→ 事实 5 兜底） ◯

- 目的：保护"runtime.json 写出 = 就绪"——即使被强杀，runtime.json 已先于 wait-signal 落地
- Given：fork daemon；等 `runtime.json` 出现后立刻 `cmd.Process.Kill()`
- Then：`runtime.json` 存在且可解析为合法 Runtime 结构（含三个 addr）；exit code 非 0（被强杀的正常表现）

---

## 四、为什么不写

| 项目 | 理由 |
|---|---|
| 每个错误的自由文本 `message` 精确文案 | 详细调试信息、非契约；TC-05/06 已断言契约 `code` + `HTTPStatus`（设计 §6.4），只放过 `message` |
| `headers` 类型错误 / `body_base64` 解码失败等非法 action data 的逐项 | 同属"非法注册立即拒绝"等价类，TC-05 一条 `grpc-invalid-action-data` 已代表；逐项展开是假多样性 |
| env 覆盖每个标量字段（`FAKED_CONTROL_LISTEN` / `FAKED_HTTP_PROXY_LISTEN` 等） | 同一机制"env > 文件值"，取 1 条代表即可；可在 TC-12 之外补 1 条"env 覆盖生效"的样例（暂列实施提示） |
| rule list / rule 调用次数查询 API | §19 明列不做，无契约可测 |
| streaming RPC / SSE / chunked transfer encoding / TLS alert / 连接级故障 | §19 明列不做 |
| 调用日志的字段顺序 / 行序 / ts 精度 / 完整 JSON schema | 机制层——日志记录形态可能随 daemon 演进调整。测试只按 `route_key`/host/path 定位本次请求的行并断言 `matched` 等可归因字段，不约束字段顺序 / ts / 完整 schema。事实 6 已对应放宽契约面 |
| 仓库内 `test/infra/ca/` 是否与 daemon 加载的 CA 一致 | CA 是测试资产 / 部署资产，不是 faked 框架契约；本测试用运行时生成的临时 CA，与 `test/infra/ca/` 解耦 |
| 并发用例的"前 N 次成功且按顺序" | 并发本来不保证顺序；TC-09 改为"成功响应集合 = 注册集合" |
| 验证 checked-in `test/.runtime/config.json` 自身合法 | 价值有限——dev 跑 daemon 自测会发现；强行加会让测试 binary 拉进业务侧 fake endpoint，破坏"acceptance 不依赖业务 fake"的隔离 |
| 后门兜底（直查 RuleStore 内存） | 前门完整：control API 注册响应 + SUT 角色发请求看响应 + 调用日志，三通道覆盖所有契约面 |
| 鉴权 / 限流类横切契约 | §6 明列第一版不做鉴权 |
| Linux SIGTERM 之外的其他信号（如 SIGINT、SIGHUP） | SIGINT 与 SIGTERM 在 daemon 入口走同一分支（`signal.Notify(SIGINT, SIGTERM)`），等价；SIGHUP 不在承诺面 |
| 性能 / 吞吐 / TLS 握手开销 | 压测范畴，不属于 acceptance |

---

## 五、实施提示（非用例本体）

### 1. 目录结构（新增物）

```text
test/infra/faked/
└── test/                                    # 测试代码与测试用资源全在这
    ├── acceptance/                          # in-process（TC-01 ~ TC-10）
    │   ├── helper_test.go                   # daemon 启停、CA 生成、HTTPS client 装填、日志大小断言
    │   ├── dispatch_grpc_test.go            # TC-01, TC-04 grpc 行, TC-07 grpc 行, TC-08, TC-09
    │   ├── dispatch_http_test.go            # TC-02, TC-03, TC-04 http 行, TC-07 http 行
    │   ├── register_invalid_test.go         # TC-05 (table-driven), TC-06
    │   └── call_log_test.go                 # TC-10
    ├── smoke/                               # fork（TC-11 ~ TC-14）
    │   ├── smoke_test.go                    # TC-11 ~ TC-14
    │   ├── proc_unix.go                     # build tag !windows：Setpgid + SIGTERM
    │   ├── proc_windows.go                  # build tag windows：CREATE_NEW_PROCESS_GROUP + CTRL_BREAK_EVENT
    │   └── cmd/main.go                      # 测试专用 daemon 入口，blank-import test/internal/testendpoint
    └── internal/                            # Go internal 约束自动隔离
        ├── testpb/                          # codegen 产物
        │   ├── echo.proto                   # service Echo { rpc Ping(PingReq) returns (PingRes); rpc Whoami(WhoamiReq) returns (WhoamiRes); }
        │   ├── echo.pb.go
        │   └── echo_grpc.pb.go
        └── testendpoint/                    # 测试专用 fake endpoint（init() 自注册）
            ├── grpc_echo.go                 # echo endpoint + Ping/Whoami 薄壳
            └── http_echo.go                 # acc.test.faked + POST /token / GET /static/whoami
```

### 2. 测试 proto 形态（`test/internal/testpb/echo.proto`）

```protobuf
syntax = "proto3";
package faked.test.echo.v1;
option go_package = "mssiot_user/test/infra/faked/test/internal/testpb";

service Echo {
  rpc Ping(PingReq) returns (PingRes);     // route_key 从 PingReq.key 抽
  rpc Whoami(WhoamiReq) returns (WhoamiRes); // route_key 用 routekey.Static()
}

message PingReq  { string key = 1; string payload = 2; }
message PingRes  { int32 code = 1; string msg = 2; string echo = 3; }
message WhoamiReq {}
message WhoamiRes { string id = 1; }
```

按项目惯例 codegen，生成物提交。

### 3. 跨平台优雅退出实现（关键代码骨架）

```go
// test/smoke/proc_unix.go
//go:build !windows

package smoke

import (
    "os/exec"
    "syscall"
)

func setupGracefulStop(cmd *exec.Cmd) {
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func sendGracefulStop(cmd *exec.Cmd) error {
    return cmd.Process.Signal(syscall.SIGTERM)
}
```

```go
// test/smoke/proc_windows.go
//go:build windows

package smoke

import (
    "os/exec"
    "syscall"

    "golang.org/x/sys/windows"
)

func setupGracefulStop(cmd *exec.Cmd) {
    cmd.SysProcAttr = &syscall.SysProcAttr{
        CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
    }
}

func sendGracefulStop(cmd *exec.Cmd) error {
    return windows.GenerateConsoleCtrlEvent(
        syscall.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid),
    )
}
```

> daemon 侧 `cmd/main.go` 现有 `signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)` **不需要改**——Windows 上 Go runtime 把 CTRL_BREAK_EVENT 路由到 `os.Interrupt` / `syscall.SIGINT`；Linux 上 SIGTERM 直接命中。
> 同样的 SysProcAttr 用于 TC-11 ~ TC-14 所有 fork 用例，保证 ctrl+c 测试进程时子 daemon 不会被同时干掉。

### 4. 测试 CA 生成 helper（HTTPS 解耦的关键）

CA 与 testpb 同等地位——测试自包含的资产，不依赖仓库内 `test/infra/ca/`。helper 在 `t.TempDir()` 内生成自签 CA：

```go
// test/acceptance/helper_test.go（节选）

func mustGenerateTestCA(t testing.TB, dir string) (certPath, keyPath string) {
    t.Helper()
    // 生成 ECDSA P-256 key（比 RSA 快）
    key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
    require.NoError(t, err)

    tmpl := &x509.Certificate{
        SerialNumber:          big.NewInt(1),
        Subject:               pkix.Name{CommonName: "faked-acceptance-test-ca"},
        NotBefore:             time.Now().Add(-time.Hour),
        NotAfter:              time.Now().Add(24 * time.Hour),
        KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
        ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
        BasicConstraintsValid: true,
        IsCA:                  true,
    }
    der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
    require.NoError(t, err)

    certPath = filepath.Join(dir, "test-ca.crt")
    keyPath  = filepath.Join(dir, "test-ca.key")
    writePEM(t, certPath, "CERTIFICATE", der)
    keyDER, _ := x509.MarshalECPrivateKey(key)
    writePEM(t, keyPath,  "EC PRIVATE KEY", keyDER)
    return
}
```

每个 harness 自己生成一份，跟 `t.TempDir()` 一起被 testing 框架清理。生成约 1-2 ms 量级，无需 `sync.Once` 优化。

### 5. 共用 Daemon + harness（关键 API 形态）

整个包共用一个 daemon：`TestMain` 启停，CA 走 daemon 动态生成模式（config 留空 CACertFile/CAKeyFile）。
`sharedHarness(t)` 返回指向共用 daemon 的句柄；`newIsolatedHarness(t)` 为个别用例起独占 daemon（逃生舱）。

```go
// test/acceptance/helper_test.go（节选）

var (
    sharedRuntimeDir string
    sharedRuntime    faked.Runtime
    sharedClient     *fakedclient.Client
)

func TestMain(m *testing.M) {
    code, err := runWithSharedDaemon(m) // MkdirTemp → NewDaemon(sharedConfig) → Start → 读 runtime.json → m.Run → Stop
    if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
    os.Exit(code)
}

func sharedHarness(t testing.TB) *harness { /* 返回 {sharedRuntimeDir, sharedRuntime, sharedClient} */ }
func newIsolatedHarness(t testing.TB) *harness { /* 每用例独占 daemon + t.TempDir + t.Cleanup(Stop) */ }

// caseKey 派生跨用例 + 跨 -count=N 重跑唯一的 route_key 基：<sanitized t.Name()>-<atomic seq>。
func caseKey(t testing.TB) string { /* ... */ }
```

### 6. 调用日志内容级断言（按 route_key 归因）

共用 daemon 下日志是同一份文件，"文件增长"无法归因到本请求；改为按本用例唯一 `route_key`（或 host/path）
扫描 jsonl 命中行。日志写入 happen-before 客户端拿到响应（engine 在 Action 执行前 / 响应 flush 前记日志，
`FileCallLogger` 无缓冲直写），正常一读即中；轮询只兜底并发写入造成的尾部半行，逐行解析跳过失败行。

```go
// test/acceptance/helper_test.go（节选）

// 直接 import faked/grpc、faked/http 的导出 CallEvent，不重定义结构。
func (h *harness) requireGRPCHit(t testing.TB, routeKey, wantKind string) // matched && route_key && action_kind
func (h *harness) requireGRPCMiss(t testing.TB, routeKey string)          // !matched && route_key
func (h *harness) requireHTTPHit(t testing.TB, routeKey string, wantStatus int)
func (h *harness) requireHTTPMiss(t testing.TB, desc string, match func(fakedhttp.CallEvent) bool)
```

不向 `faked/client` 暴露日志读取 API——读取逻辑完全留在测试代码中，且只断言可归因字段，不约束字段顺序 / ts。

### 7. Table-driven 复用模式（TC-05 样例）

```go
func TestRegisterInvalid_GRPC(t *testing.T) {
    t.Parallel()
    h := sharedHarness(t)
    key := caseKey(t)
    cases := []struct {
        name       string
        rule       grpcproto.Rule
        wantCode   string // 契约错误码（设计 §6.4）
        wantStatus int
    }{
        // ... 各行内联非法点 + 期望 code/status ...
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            err := h.client.GRPC().RegisterRule(context.Background(), tc.rule)
            assertAPIError(t, err, tc.wantCode, tc.wantStatus) // 断言 Code+HTTPStatus，只放过 message
        })
    }
}
```

### 8. 实施顺序

1. **codegen 准备**——先摸清项目 protoc/buf 入口，落 `test/internal/testpb/echo.proto` 并生成代码（不写测试逻辑）。
2. **测试 endpoint**——`test/internal/testendpoint/{grpc_echo,http_echo}.go` + `test/smoke/cmd/main.go`，能 `go build` 通过。
3. **acceptance happy path**——`helper_test.go` + TC-01 + TC-02 + TC-03（HTTPS）跑通 ⇒ 给你看一版形态。
4. **铺开 acceptance 余下用例**——TC-04 ~ TC-10，重 table-driven 部分先写。
5. **smoke 用例**——`proc_{unix,windows}.go` + TC-11 ~ TC-14。
6. **CI 验证**——确保 Linux runner 上 TC-13 Linux 分支跑通。
7. **清理旧物**——确认全绿后删除 `scripts/probes/smoke/` 与 `scripts/smoke.{ps1,sh}`。

### 9. 已知风险点

- Windows 上 `CTRL_BREAK_EVENT` 的时序：`bootstrap.Run()` **在 `d.Start()` 之前**装好 `signal.Notify`——
  runtime.json 写出即被视为就绪，若把 Notify 推迟到 Start 之后，runtime.json 出现到 handler 装载之间存在
  竞争窗口，此窗口内的信号会走默认处理直接终止（Windows 表现为 exit `0xC000013A`），错过优雅退出。
  生产入口与 smoke 入口共享同一个 `bootstrap.Run()`，TC-13 验证的是真实入口。
- `test/internal/testendpoint` 通过 init() 自注册到 `fakedgrpc.RegisteredEndpoints()` / `fakedhttp.RegisteredEndpoints()` 全局 slice——多个测试包并发 import 时不会重复注册（init 只跑一次），但要确认 daemon Stop 后下一个测试再 Start 时 endpoint registry 仍可用（应是 OK 的，registry 是 process-global 静态状态，不绑 daemon 实例）。
- TC-13 优雅退出在 Linux CI 上的 Setpgid 行为：测试进程必须不与 daemon 共享 process group，否则向 daemon 发 SIGTERM 不会泄漏到测试自身——`cmd.SysProcAttr.Setpgid=true` 已保证。
- 测试自生成 CA 的 `NotAfter` 设 24 小时即可，跨午夜跑的 CI 不会跨期失效（每次跑都新生成）。
