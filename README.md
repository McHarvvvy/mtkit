# mtkit

mtkit 是 Go 应用的**集成测试基础设施框架**。它让你在 `TestMain` 一个入口里，把被测服务（SUT）需要的真实依赖环境一次性准备就绪，再开始跑测试——不必在每个测试包里手写「拉容器、等就绪、塞连接串、清理」的样板。

它解决这些问题：

- **容器编排**：按声明启动 MySQL / Redis / MongoDB / TDengine / Pulsar 等测试容器，管理生命周期与清理。
- **被测服务管理**：编译、启动、就绪探测、优雅停止 SUT 进程，并把依赖连接信息注入它。
- **假外部依赖（faked）**：用内嵌的 fake gRPC server 与 HTTP(S) MITM 代理拦截 SUT 的出站调用，按规则应答。
- **唯一测试数据（fixture）**：跨进程、跨用例生成无碰撞的 UUID / Email / Phone 等。
- **前置数据装配（provision）**：跨测试进程只执行一次建表 / 索引 / 种子数据。
- **跨包复用**：多个测试包共享一个后台 daemon 与同一组容器，靠租约复用，不重复拉起。

> 想了解内部如何运作，读 [docs/architecture.md](docs/architecture.md)；术语以 [CONTEXT.md](CONTEXT.md) 为准。

---

## 工作原理（一分钟版）

mtkit **不另起独立守护程序**：第一个测试进程会把**自己这个测试二进制** re-exec 成一个后台 daemon（设环境变量 `MTKIT_RUN_MODE=daemon`），由它持有容器 / faked / SUT；后续测试进程作为 client 发现并复用它，靠租约 + 心跳保活，空闲超时后 daemon 自动退出并清理。

你的代码对这一切无感——只写一次 `kit.Run(m)`，框架自动分流 daemon / client 两种角色。详见 [架构文档 §3](docs/architecture.md#3-运行模式与-re-exec-模型)。

---

## 安装

```bash
go get github.com/McHarvvvy/mtkit
```

```go
import mtkit "github.com/McHarvvvy/mtkit"
```

**前置要求**：本机可用的 Docker（容器编排与孤儿清理都依赖它）。

---

## 快速开始

### 1. 在 TestMain 装配

```go
package myapp_test

import (
    "context"
    "os"
    "testing"

    mtkit "github.com/McHarvvvy/mtkit"
    "github.com/McHarvvvy/mtkit/container"
    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/wait"
)

func TestMain(m *testing.M) {
    kit, err := mtkit.New("myapp", &mtkit.Config{
        RuntimeDir: os.TempDir() + "/myapp-mtkit",
    })
    if err != nil {
        panic(err)
    }

    kit.RegisterContainers(func(ctx context.Context) ([]container.ContainerRegistration, error) {
        return []container.ContainerRegistration{{
            Name: "redis",
            Start: func(ctx context.Context) (testcontainers.Container, error) {
                return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
                    ContainerRequest: testcontainers.ContainerRequest{
                        Name:         "myapp-redis",
                        Image:        "redis:7-alpine",
                        ExposedPorts: []string{"6379/tcp"},
                        WaitingFor:   wait.ForListeningPort("6379/tcp"),
                    },
                    Started: true,
                })
            },
            SUTEnv: func(ctx context.Context, c testcontainers.Container) (map[string]string, error) {
                host, _ := c.Host(ctx)
                port, _ := c.MappedPort(ctx, "6379/tcp")
                return map[string]string{"REDIS_ADDR": host + ":" + port.Port()}, nil
            },
        }}, nil
    })

    // 同一句同时适配 daemon 与 client 两种模式，无需自己判断。
    os.Exit(kit.Run(m))
}
```

`kit.Run(m)` 返回后透传 `m.Run()` 的退出码；进入 `m.Run()` 时容器已就绪、（若启用）SUT 与 faked 已就绪。

### 2. 在用例里用 fixture 生成唯一数据

用例需要 daemon 的地址与 token——从 runtime 文件读出（`kit.RuntimePath()` 给出路径）。

> ⚠️ **必须 lazy 初始化**：runtime 文件在 `kit.Run(m)` **内部**才写好（daemon 自启 + 拿到租约之后），所以**不能在 `TestMain` 里 `Run` 之前**读它。用包级 `sync.Once`，在测试函数执行期才读 runtime、建 client。

```go
import (
    fixture "github.com/McHarvvvy/mtkit/client/fixture"
    mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

var (
    runtimePath string        // 在 TestMain 里由 kit.RuntimePath() 赋值
    fxOnce      sync.Once
    fxClient    *fixture.Client
)

func fxScope(t testing.TB) *fixture.CaseScope {
    t.Helper()
    fxOnce.Do(func() {        // 进程级只建一次，测试执行期才读 runtime
        info, err := mtruntime.Read(runtimePath)
        if err != nil {
            t.Fatalf("fixture runtime unavailable: %v", err)
        }
        fxClient = fixture.New(info.Addr, info.Token)
    })
    return fxClient.NewScope(t)
}

func TestSignup(t *testing.T) {
    s := fxScope(t)
    email := s.Email()      // fixture+ab12...@mtkit.test，全局唯一
    phone := s.Phone("CN")  // +86 开头的 E.164
    id    := s.UUID()
    _ = email; _ = phone; _ = id
}
```

注册 fake 外部依赖、建数据库连接池（`PersistEnv`）同理，都遵守这条 lazy 规则——见 [faked 模块文档](docs/modules/faked.md) 与 [fixture 模块文档](docs/modules/fixture.md)。

---

## 配置

两种配置方式，二选一：

1. **编程式**：`mtkit.New("project", &mtkit.Config{...})`（如上例）。
2. **JSON 文件**：`mtkit.New("project", nil)` 会从 `MTKIT_CONFIG_PATH` 或默认 `~/.mtkit/<project>/mtkit.config.json` 加载。

`RuntimeDir` 是**唯一必填**项。其余关键字段与默认值：

| 字段                                            | 说明                                                    | 默认                              |
| ----------------------------------------------- | ------------------------------------------------------- | --------------------------------- |
| `RuntimeDir`                                    | 产物目录（runtime 文件 / 日志 / 构建产物 / faked 产物） | **必填**                          |
| `RuntimeFile`                                   | runtime 文件路径（纯文件名 / 绝对路径）                 | `<RuntimeDir>/mtkit.runtime.json` |
| `DaemonMode.Addr`                               | daemon 监听地址                                         | `127.0.0.1:0`（随机端口）         |
| `DaemonMode.IdleTTLSeconds`                     | 空闲自动退出秒数                                        | 36                                |
| `ClientMode.HTTPTimeoutSeconds`                 | client 请求 daemon 超时                                 | 60                                |
| `SUT.Enabled` + `BinaryPath`/`SourceDir`        | 启用被测服务（给二进制或源码目录二选一）                | 关                                |
| `SUT.Lifecycle.*Seconds`                        | 构建 / 就绪 / 优雅停止超时                              | 120 / 45 / 5                      |
| `Faked.Enabled`                                 | 启用内嵌 faked 引擎                                     | 关                                |
| `Faked.HTTP.{ProxyListen,CACertFile,CAKeyFile}` | HTTP 代理监听 / 静态 CA（成对，留空则动态生成 CA）      | 动态 CA                           |

启用 SUT 后必须调 `RegisterSUT(plan)`（实现 `SUTBootPlan`），启用 faked 涉及 HTTPS 时 SUT 需装 CA 插件——细节见各模块文档。

---

## 能力与文档导航

| 你想做的事                     | 怎么配                           | 详见                                                                         |
| ------------------------------ | -------------------------------- | ---------------------------------------------------------------------------- |
| 拉起依赖容器并把连接串注入 SUT | `RegisterContainers`             | [container.md](docs/modules/container.md)                                    |
| 编译并管理被测服务进程         | `RegisterSUT` + `SUT.Enabled`    | [orchestration.md §5](docs/modules/orchestration.md#5-sut被测服务的语义边界) |
| 生成唯一测试数据               | `fixture.Client` / `CaseScope`   | [fixture.md A](docs/modules/fixture.md#a-唯一数据生成)                       |
| 跨进程幂等装配前置数据         | `WithSchema([]fixture.Resource)` | [fixture.md B](docs/modules/fixture.md#b-前置数据装配provision)              |
| fake gRPC / HTTP(S) 外部依赖   | `Faked.Enabled` + `fake.Client`  | [faked.md](docs/modules/faked.md)                                            |

程序 API 的逐项参照以源码 godoc 为准（导出标识符均有中文注释）。

---

## 调用者职责：框架之外你要自己写的部分

mtkit 是「**机制框架 + 一组缝（seam）**」：上面那些能力，框架给的都是**机制**，每条机制都留了一个**必须由你用领域知识填的缝**。下表是分工地图——左侧框架包办，右侧是你要自己写的：

| 能力        | 框架提供的机制                            | 你必须自己写的领域内容                                                                  | 详见                                                            |
| ----------- | ----------------------------------------- | --------------------------------------------------------------------------------------- | --------------------------------------------------------------- |
| 唯一数据    | 全局发号 + HiLo 缓存 + 保形加密           | （几乎无）直接调 `CaseScope`                                                            | [fixture.md A](docs/modules/fixture.md#a-唯一数据生成)          |
| fake server | gRPC fake set + HTTP MITM 代理 + 调用日志 | **endpoint 声明**、**CA addon 拷入 SUT**、**TestMain blank-import**、**逐用例规则注册** | [faked.md](docs/modules/faked.md)                               |
| 前置装配    | 跨进程幂等 once-runner + 文本账本         | **`Resource` 实现**（框架不内置任何类型）、**幂等 `Apply`**                             | [fixture.md B](docs/modules/fixture.md#b-前置数据装配provision) |
| 种子数据    | 复用 provision 或 fixture+PersistEnv      | **判定「共享种子 vs 每用例数据」并自己读写**                                            | 见下 §3                                                         |

### 贯穿规则：一切在 `m.Run()` 期间 lazy 初始化

runtime 文件（含 `SUTEnv`、faked 地址、token）只有在 `kit.Run(m)` **内部**（daemon 自启 + 拿到租约之后）才写好。所以 **fake client / fixture client / PersistEnv 都不能在 `TestMain` 的 `Run` 之前建**——必须用包级 `sync.Once`，在测试函数执行期才 `mtruntime.Read(runtimePath)` 再构造。下面三块都遵守这条规则。

### 1. fake server：声明 → 接线 → 注册

框架给了 gRPC fake server set 与 HTTP MITM 代理，但「有哪些外部依赖、监听什么、用什么 env key」是**项目领域知识**，要你写代码声明。三步：

**① 声明 endpoint（代码里 `init()` 自注册）。** gRPC 每个 unary RPC 写一个 typed 薄壳，严守「四步法」（收 typed 请求 → 取 route_key → 建 response 容器 → 调 `Engine.HandleUnary`，不写业务分支）：

```go
func init() { fakedgrpc.RegisterEndpoint(&echoEndpoint{}) }

type echoEndpoint struct{}
func (echoEndpoint) ID() string     { return "echo" }               // control API 里的 endpoint 取值
func (echoEndpoint) EnvKey() string { return "ECHO_GRPC_ADDR" }     // 注入 SUT 的直连地址 env 名
func (echoEndpoint) Listen() string { return "127.0.0.1:0" }        // :0 让 OS 分配端口
func (echoEndpoint) Register(s *googrpc.Server, deps fakedgrpc.EndpointDeps) {
    pb.RegisterEchoServer(s, &echoService{Engine: deps.Engine})
}
func (echoEndpoint) Methods() []fakedgrpc.MethodSpec { /* 供注册期校验 response 类型 */ }

func (s *echoService) Ping(ctx context.Context, req *pb.PingReq) (*pb.PingRes, error) {
    out := &pb.PingRes{}
    err := s.Engine.HandleUnary(ctx, fakedgrpc.Call{
        Endpoint: "echo", FullMethod: pb.Echo_Ping_FullMethodName,
        RouteKey: req.GetKey(), Response: out, // route_key 从真实请求字段取
    })
    return out, err
}
```

HTTP 同理：实现 `Endpoint{ Host(), Routes() }`，每个 `Route` 给 `Method`/`Path` + `GenRouteKey`。细节与 route_key helper 见 [faked.md §1.2 / §2.1](docs/modules/faked.md#12-声明一个-fake-endpoint主扩展点)。

**② 让 SUT 信任 fake 的 HTTPS（仅 HTTPS faking 需要）。** faked 对出站 HTTPS 做 MITM、用动态 CA 现签叶证书，SUT 必须信任这张 CA 才能握手。把 [`addons/mtkit_ca_addon.go`](addons/) **拷进 SUT 的 main 包**（boot plan 加 `-tags mtkit_it`，框架默认已加）。两条限制：只覆盖走 `http.DefaultTransport` 的 client；SUT 不可在 `init` 阶段就发起需被 fake 的 HTTPS。

**③ TestMain blank-import + 用例里注册规则。** blank-import endpoint 包（让 client 与 daemon 都注册它），用例里链式注册、`Apply(t)` 提交：

```go
import _ "yourproject/test/fakeendpoints" // 触发 init() 注册

fc := fake.New(info.Addr, info.Token) // info 从 mtruntime.Read 懒读
fc.GRPC().On("echo", pb.Echo_Ping_FullMethodName).
    RouteKey(caseKey).                       // 必填，并发隔离根基（见下「为什么能并行」）
    Static(&pb.PingRes{Msg: "pong"}).
    Apply(t)
```

### 2. 前置数据装配（provision）：自己实现 Resource

**框架刻意不提供任何内置 `Resource` 类型，只给接口。** 理由：provision 形态由业务决定、无法预见，且语义全在 DDL/建库/建 namespace 等具体动作里，封装成框架类型并无价值。用一个通用 `funcResource`（key + apply 闭包）即可，经 `kit.WithSchema` 注册：

```go
type funcResource struct {
    key   string
    apply func(ctx context.Context, env *fixture.PersistEnv) error
}
func (r funcResource) Key() string { return r.key }
func (r funcResource) Apply(ctx context.Context, env *fixture.PersistEnv) error { return r.apply(ctx, env) }

kit.WithSchema([]fixture.Resource{
    funcResource{key: "mysql.app.db", apply: func(ctx context.Context, env *fixture.PersistEnv) error {
        _, err := env.MySQL("MYSQL_DSN").ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `app`")
        return err
    }},
    funcResource{key: "mysql.app.users", apply: func(ctx context.Context, env *fixture.PersistEnv) error {
        _, err := env.MySQL("MYSQL_DSN").ExecContext(ctx,
            "CREATE TABLE IF NOT EXISTS app.users (uid BIGINT PRIMARY KEY)") // IF NOT EXISTS → 重入幂等
        return err
    }},
})
```

三条硬约束：

- **slice 顺序即执行顺序**——「建库」排「建表」前。
- **`Key()` 一旦确定不可再改**——改了等于换标识，旧账本条目永不命中，会重复 `Apply`。
- **`Apply` 必须幂等可重入**——`IF NOT EXISTS` / 容忍「已存在」/ list-then-create / 多写用事务。框架保证同一 daemon 生命周期内同一 key 只跑一次，但 daemon 复用、重启会再次进入。

连接从 `Apply` 的 `*PersistEnv` 拿（`env.MySQL(key)` / `Redis` / `Mongo` / `PulsarAdmin` / `TDengine`）；`key` 是 `SUTEnv` 的 key（如 `"MYSQL_DSN"`），不是容器名。深入见 [fixture.md B](docs/modules/fixture.md#b-前置数据装配provision)。

### 3. 种子数据（seeding）：没有专用原语，二选一

**mtkit 没有 seeding API。** 种子数据是上面两条机制的**用法**，按「数据归谁」二选一：

| 数据性质                                                 | 用什么               | 怎么做                                                                     |
| -------------------------------------------------------- | -------------------- | -------------------------------------------------------------------------- |
| **跨用例共享、应只建一次**（字典表、基准账号、固定枚举） | provision            | 当成一条 `Resource`，在 `Apply` 里 `INSERT`，享受跨进程只跑一次 + 账本幂等 |
| **每用例独占、不可共享**（本用例要创建的实体）           | fixture + PersistEnv | 用例内用 `CaseScope` 生成唯一值，经进程级单例 `PersistEnv` 写库            |

> 判据一句话：**「跨用例可共享且只建一次」→ provision；「每用例独占」→ fixture + PersistEnv。** 把每用例数据塞进 provision 是错配——provision 是「只跑一次」语义，与并行用例的隔离需求相悖。

---

## 为什么能并行：数据隔离机制

mtkit 在两个层面提升效率，二者都靠**隔离**而非串行化达成：

- **跨进程复用**：多个测试包共享**同一个 daemon + 同一组容器**（靠租约），不必每个包重拉容器。`go test -p N ./...` 并行跑多个包时，它们汇聚到一个后台环境。
- **进程内并行**：用例可放心 `t.Parallel()`。并行用例之所以不互相踩，是因为**每个用例操作的是一块互不相交的数据**，而不是靠加锁排队。

隔离模型分两层——想清楚「哪层共享、哪层独占」是用好并行的关键：

### 共享层：只读 / 幂等，建一次给所有人用

容器与 provision 出的 schema/种子属于此层。它们要么只读、要么幂等，被所有用例（跨进程、跨 goroutine）共享读取。这一层**不是**隔离对象——正因为它稳定共享，才不必每个用例重建。

### 独占层：每用例切一块互不相交的命名空间

并行用例真正「各写各的」靠两套**唯一命名**机制，它们是数据隔离的核心：

**① fixture 唯一值——单一真相源的全局计数器。** 唯一性只在一处保证：daemon 侧一个原子计数器，每次发号返回半开区间 `[start, end)`，**跨调用者天然不相交**。于是任意两个并行用例各自 `s.UUID()` / `s.Email()` / `s.UniqueInt()` 拿到的值必不相同——它们可以写同一张表，但落在不同的行，零冲突。测试侧用 HiLo 缓存把热路径变成本地无锁自增，**唯一性跨 goroutine、跨进程都成立**。

```go
// 两个并行用例各自创建「一个用户」，uid/email 必不同 → 同表不同行，无需协调
func TestA(t *testing.T) { t.Parallel(); s := fxScope(t); createUser(t, s.UniqueInt(10), s.Email()) }
func TestB(t *testing.T) { t.Parallel(); s := fxScope(t); createUser(t, s.UniqueInt(10), s.Email()) }
```

**② faked route_key——同一路由按用例分流。** N 个并行用例共享同一个 faked 引擎，靠 route_key 互不串扰：每个用例注册规则时带**本用例专属的 route_key**，引擎在 SUT 真实调用时从请求里现算 route_key，按 `(endpoint, method, route_key)` 精确命中——只命中本用例那条规则。建议把 route_key 派生自 `t.Name()` + 进程内原子序号，保证跨用例、跨重复执行都唯一：

```go
var caseKeySeq atomic.Uint64
func caseKey(t testing.TB) string {
    name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
    return fmt.Sprintf("%s-%d", name, caseKeySeq.Add(1))
}
```

### 为什么这套设计能真正并行

隔离靠「命名空间不相交」而非加锁，所以**并行用例之间没有竞争点**。全局仅剩两处串行化，都低频且廉价：HiLo 补块（每 65536 次取号一次回环往返）、provision 账本锁（每个 key 一次、且 `IF NOT EXISTS`）。热路径全程无锁。

要让并行真正成立，你（调用方）需守住三条契约：

- **外部可见数据一律从 fixture 取唯一值**，不写死字面值、不用固定主键 / 固定 key——否则两个并行用例会撞同一行。
- **fake 规则必须带 per-case route_key**（为空一律 fail-loud，正是为杜绝「裸路由」误匹配）。
- **每用例数据不进 provision**（那是「只跑一次」的共享语义）；`PersistEnv` / fixture client 是进程级单例，**别在用例 cleanup 里 `Close`**，否则会拖垮其他并发用例。

---

## 注意事项

- **必须有 Docker**：daemon 冷启动会列容器做孤儿清理；无 Docker 会启动失败。
- **退出码如实透传**：`kit.Run(m)` 返回 `m.Run()` 的码；装配非法（如未 `RegisterContainers`、容器重名、SUT 启用却没 `RegisterSUT`）直接返回 1 且不跑用例。
- **空闲自动回收**：所有测试进程结束、空闲超过 `IdleTTLSeconds` 后 daemon 自动退出并清理容器 / runtime 文件。调短它可加快本地回收，调长可让多个测试包之间复用环境。
