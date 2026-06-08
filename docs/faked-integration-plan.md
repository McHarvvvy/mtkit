# faked 集成 mtkit 实施方案

本文是 **faked 接入 mtkit** 的实施落地方案，对应 `test-harness-design.md` 演进阶段 **P1**：把 faked **引擎代码迁入 mtkit**，并让 mtkit daemon **在自己进程内**直接托管 faked（不再独立子进程、不再测试进程侧 fork）。

读者：实现者与方案评审者。设计依据：`test-harness-design.md`（总模块/daemon 视角）、`faked-design.md`（faked 实现细节）、`mtkit-migration-plan.md`（段1 迁移已完成的基线）。

> **本方案对既有设计做了一次重要修订：faked 由「独立子进程·daemon 监督」改为「内嵌 daemon 主进程」。** 修订理由见 §0.4，需回写 `test-harness-design.md` §4.1 / §8 / §4.2 / §10 与 `faked-design.md` 配置 / CA 章节。

---

## 0. 目标与不变量

### 0.1 做什么

1. **faked 引擎代码迁移（本次核心）**：把 faked **引擎本体 + client + 自测**从 `mssiot_user/test/infra/faked` 迁入 mtkit（`mtkit/faked` + `mtkit/client/fake`），收敛到 `test-harness-design.md` §3 终态。映射见 §0.3。
2. **faked 内嵌 daemon 主进程**：mtkit daemon 在进程内直接构造并启动 faked 引擎（开 gRPC / HTTP 代理 listener、control 挂主 mux），替代现状 `test/infra/run.go` 的测试进程侧 fork，也不再独立子进程。理由见 §0.4。
3. **配置 / runtime 单一化**：faked 不再有独立配置文件与独立 runtime 文件——引擎参数折进 `mtkit.config.json` 的 `faked` 块，地址折进 `mtkit.runtime.json`。环境变量全集收窄到 **2 个**。
4. **统一进程引擎只服务 SUT**：`proc` 包（`ManagedProcess`）保留，但**只有 SUT 是它的实例**（SUT 是被测的生产二进制，必须独立进程）。faked 不再走 proc。
5. **门面 API 调整**：`SUTBootPlan`（Go 接口，不可序列化）从 `Config` 移出、瘦身（生命周期参数移入配置），改注册式 API。

### 0.2 不变量

- mtkit 仍是独立 git repo / Go module（`github.com/McHarvvvy/mtkit`），**不得 import 任何 `mssiot_user` 下的包**。项目只经「注入」向 mtkit 提供专属内容：容器注册回调、`SUTBootPlan`、以及经 `init()` 自注册的 fake endpoint（项目 `TestMain` blank-import 触发，见 §0.3.2）。
- 沿用 re-exec daemon 模型（**测试二进制 == daemon 二进制**）。faked 引擎随测试二进制编译进来——这是「内嵌」的前提，也是 re-exec 模型早已认账的代价（§0.4）。

### 0.3 包结构与迁移映射

`test-harness-design.md` §3 终态把 `mtkit/faked` 定义为 **fake engine + protocol + 服务端**、`mtkit/client/fake` 定义为 faked 客户端。本次把引擎迁入，并由 daemon 在进程内调用它。

#### 0.3.1 迁移映射（已逐文件核实：引擎在 `test/infra/faked` 内部自引用，零 mssiot_user 业务耦合）

| 源（`test/infra/faked/`） | 目标（`pkg/mtkit/`） | 说明 |
| --- | --- | --- |
| `config.go` `control_api.go` `daemon.go` `runtime_file.go` | `faked/` | 跨协议编排 + 服务端（`Daemon.Start/Stop` 成为 daemon 进程内调用入口，见 §5） |
| `grpc/**` | `faked/grpc/**` | gRPC 引擎/协议/registry/actions/routekey/store/server/parser/call_log |
| `http/**` | `faked/http/**` | HTTP MITM 引擎/proxy/ca/协议/registry/actions/routekey/store/call_log |
| `client/` | `client/fake/` | 消费端 thin client（设计文档 §3 客户端区） |
| `test/{acceptance,smoke,internal}` | `faked/test/**` | 引擎自测，随引擎入 mtkit（用中立 `echo.proto`，零业务依赖） |

不再迁移的（内嵌后作废）：

- `envvar/`：原集中 `FAKED_*` 与 faked 独立文件名。内嵌后 faked 无独立配置 / runtime 文件、无独立 env；少量仍需的常量（call log 文件名、生成 CA 文件名）并入 `faked` 包自身。
- `bootstrap/` + `cmd/main.go`：原「独立 faked 二进制」的 flag / 信号 / 配置加载入口。内嵌后没有独立二进制，整体删除。

#### 0.3.2 endpoint 如何进入 daemon（无 shim、无独立二进制）

fake endpoint（`forum` / `googlepay`）是**项目专属代码**（import `api/forumpb` 等），留在 `mssiot_user/test/fake/{grpc,http}`，仅改 import 路径（`mssiot_user/test/infra/faked/*` → `github.com/McHarvvvy/mtkit/faked/*`）。它们经 `init()` 自注册进 mtkit faked registry。

daemon == 测试二进制；只要测试二进制 import 了这些 endpoint 包，registry 即被填充。触发点是**项目已有的 `TestMain` bootstrap**（`test/infra/run.go`）：

```go
import (
    _ "mssiot_user/test/fake/grpc"   // init() → faked.RegisterEndpoint(forum)
    _ "mssiot_user/test/fake/http"   // init() → faked.RegisterEndpoint(googlepay)
)
```

**这不是新增的 shim**——它就是测试 bootstrap 本身。daemon 启动 faked 引擎时调 `faked.RegisteredEndpoints()` 取到它们。没有独立 `cmd/faked`、没有 `go build` faked、没有 fork。

> **不留独立 `cmd/faked` 调试二进制**（决策）：内嵌后它不在集成路径上；隔离调试 fake 行为可写聚焦测试。日后确有手动跑 faked 的需求再补一个薄 main，不预先背它。

#### 0.3.3 迁移硬约束

1. **不得 import mssiot_user**（§0.2）：`http/ca/ca.go` 的 CA Subject CommonName 改通用值（如 `"mtkit faked Integration Test CA"`）。其余自引用路径机械改写。
2. **go.mod**：`pkg/mtkit/go.mod` 中 `google.golang.org/grpc` / `protobuf` 现为 indirect，迁移后转直接依赖，`go mod tidy`（依赖已在树中，无新增外部库）。
3. **双模块自测**：引擎自测随引擎入 mtkit 模块，归 mtkit 的 `go test` 跑；CI 需增加对 mtkit 模块的测试执行。
4. **§9.2 / §9.3 配置面重构并入本次**：迁移已要重写这些文件，顺手做完 `env_key`→`Endpoint.EnvKey()`、删 `http.endpoints`。

### 0.4 为什么内嵌而非独立子进程（修订设计文档 §4.1 / §8）

设计文档 §4.1 当初把 faked 定为「daemon 监督的独立子进程」，核心顾虑是「re-exec 下若由**测试进程**各自 fork → N 个 faked 实例」。但该顾虑的真正解法是「**由唯一的 daemon 拥有 faked**」——而「daemon 在自己进程内跑 faked」同样满足单实例，且比「daemon fork 子进程」更直接。「独立子进程」是更强的主张，只被两点支撑：

- **依赖隔离**（faked 引擎不进测试二进制）：但 re-exec 早已把 docker SDK 链进测试二进制（设计 §2 认账「拿不到瘦二进制」），此处仅增量。
- **廉价独立 reload**（改 endpoint 只重建重启 faked、容器不动）：经评估这是**低频场景**——endpoint 是「新增外部依赖」时才动，rule（每请求行为，高频）走运行时注入、两种形态都无需重启任何东西。为这一低频优化背「proc 监督 + 独立配置 / runtime / 发现链 + 跨平台进程树终止」整层机械，**收益不抵复杂度**。

**结论：内嵌。** 收益：daemon 完全托管 faked（用户零接触其启停）、删掉独立二进制 / shim / 发现链 / 进程树终止整层、control 面并入 daemon 主 mux（贴合设计 §4.3）。代价：① 放弃容器长活下的「秒级 reload faked」（日常 `go test` / CI 因每次重编测试二进制、内嵌 faked 自动最新；仅 `test-env-up` 长活环境改 endpoint 需重启测试环境——**本期明确不做 reload**）；② 测试二进制增大（已在 re-exec 账上）。

---

## 1. 配置管理

### 1.1 设计原则

> **以配置文件为中心：环境相关、因人/因机而异的数据全部进配置文件；配置文件可不提交版本库，从而支持多人多环境各自独立配置。**

- 配置文件格式：**JSON**，全局只有 `mtkit.config.json` 一份（faked 参数是其中一个子块）。
- **环境变量全集仅 2 个**（见 §1.5）：`MTKIT_CONFIG_PATH`（配置文件路径）+ `MTKIT_RUN_MODE`（re-exec 内部模式信号）。

> **注意区分**：本规则约束「mtkit **自身**的配置」。SUT 子进程运行所需的环境变量（`MSSIOT_IT_RPC_FORUM_ADDR`、`HTTP_PROXY` 等，见 §8）是 mtkit **注入给 SUT** 的运行配置，不在本规则约束范围内。

### 1.2 配置加载优先级

```text
1. New(project, cfg) 传入的 *Config（非 nil）   ← 调用者自行组装，最高优先级
2. MTKIT_CONFIG_PATH 环境变量指向的文件
3. 默认路径：<UserHomeDir>/.mtkit/<project>/mtkit.config.json
4. 以上都拿不到 → 报错（mtkit 配置含必填项，无法全默认启动）
5. 任一解析出的路径读取/解析失败 → fail fast
```

`project` **由代码传入** `New(project string, cfg *Config)`，**不写进配置文件**：默认路径含 `<project>` 段，若写配置里则读文件前无法推导默认路径（鸡生蛋）。project 是应用身份标识（相当于 module name），属「代码事实」，写死在 bootstrap 代码合理。

### 1.3 配置校验流程

```text
解析 JSON（缺失字段为零值）
→ 校验必填项：为空则报错
→ 可选项填默认值
→ 跨字段校验（条件必填、互斥/共现约束）
```

### 1.4 re-exec 下的配置一致性

re-exec daemon 模型下，client 进程与被 re-exec 的 daemon 进程是**同一二进制、走同一份 `TestMain → New(project, cfg) → Register* → Run`**：

- `cfg == nil`：client 与 daemon 都从**同一来源**（同一 `MTKIT_CONFIG_PATH` 或同一默认路径）加载 → 天然一致。
- `cfg != nil`：daemon 重新执行同一段 bootstrap 代码 → 重建出**相同**的 cfg 字面量。
- `RegisterContainers` / `RegisterSUT` 与 endpoint blank-import 同理：daemon 侧再次执行，得到相同的容器集、boot plan 与已注册 endpoint。

因此**配置无需经额外通道在 client/daemon 间传递**——re-exec 模型的固有红利。

### 1.5 环境变量全集（仅 2 个）

| 环境变量            | 类别     | 用途                                                                          | 设置者                                          |
| ------------------- | -------- | ----------------------------------------------------------------------------- | ----------------------------------------------- |
| `MTKIT_CONFIG_PATH` | 配置     | mtkit 配置文件路径（§1.2 优先级 2）                                           | 人 / CI                                         |
| `MTKIT_RUN_MODE`    | plumbing | re-exec 内部模式信号，值 `daemon` 表示守护进程模式；缺省/其他视为 client 模式 | mtkit client 自启 daemon 时注入，**非用户配置** |

**`MTKIT_RUN_MODE` 是 re-exec 模型的固有需求**：被重新执行的同一测试二进制必须有信号区分自己走 daemon 还是 client 路径；命令行参数方案在 `go test` 二进制上不可靠（test flag 解析拒绝未知 flag）。它不承载环境数据，定性为「进程模式 plumbing」。

**已废弃的 env：**

| 废弃 env | 原用途 | 替代 |
| --- | --- | --- |
| `MT_MODE` | 旧 re-exec 模式信号 | 重命名为 `MTKIT_RUN_MODE` |
| `MT_RUNTIME` | 把 runtime 路径传给 daemon | daemon 从配置读 `runtime_dir`；re-exec 同源配置必然一致 |
| `FAKED_CONFIG_PATH` / `FAKED_RUNTIME` / `FAKED_CONTROL_LISTEN` / `FAKED_HTTP_*` | faked 独立进程的配置 / runtime / 监听 / CA | **全部删除**：faked 内嵌，参数进 `mtkit.config.json` 的 `faked` 块，地址进 `mtkit.runtime.json`，control 并入 daemon 主 mux |

> 改动落点：`pkg/mtkit/constant/env.go`（`MT_MODE`→`MTKIT_RUN_MODE`、删 `MT_RUNTIME`）；原 `test/infra/faked/envvar` 包随引擎迁移整体废止（§0.3.1）。

---

## 2. mtkit.config.json 规格

SUT 是被 `proc` 监督的子进程（有 `lifecycle` 与 `idle_ttl`）；faked 是 daemon 进程内的引擎（只有引擎参数，无构建 / 生命周期字段）。

```json
{
  "runtime_dir": "/home/alice/.mtkit/mssiot_user/runtime",

  "daemon": {
    "addr": "127.0.0.1:0",
    "idle_ttl_seconds": 36
  },

  "client": {
    "http_timeout_seconds": 60
  },

  "sut": {
    "enabled": true,
    "binary_path": "",
    "source_dir": "/home/alice/work/mssiot_user/cmd",
    "idle_ttl_seconds": 2,
    "lifecycle": {
      "build_timeout_seconds": 120,
      "ready_timeout_seconds": 45,
      "grace_period_seconds": 5
    }
  },

  "faked": {
    "enabled": true,
    "grpc": {
      "endpoints": [ { "id": "forum", "listen": "127.0.0.1:0" } ]
    },
    "http": {
      "proxy_listen": "127.0.0.1:0",
      "ca_cert_file": "",
      "ca_key_file": ""
    }
  }
}
```

**顶层 / daemon / client / sut 字段：**

| 字段                          | 必填     | 默认值        | 说明                                                                           |
| ----------------------------- | -------- | ------------- | ------------------------------------------------------------------------------ |
| `runtime_dir`                 | **必填** | —             | mtkit 产物目录：`mtkit.runtime.json`、daemon / SUT 日志、SUT 构建产物、faked 调用日志与生成 CA（`<runtime_dir>/faked/`）|
| `daemon.addr`                 | 可选     | `127.0.0.1:0` | daemon 监听地址（lease + fixtured + **faked control** 共用此端口）              |
| `daemon.idle_ttl_seconds`     | 可选     | `36`          | 无活跃租约后 daemon 自动退出时间                                               |
| `client.http_timeout_seconds` | 可选     | `60`          | client → daemon 的 HTTP 超时                                                   |
| `sut.enabled`                 | 可选     | `false`       | 是否启用 SUT                                                                  |
| `sut.binary_path` / `source_dir` | 条件必填 | — | 二选一（都填则 `binary_path` 优先）；`source_dir` 走 `go build`                |
| `sut.idle_ttl_seconds`        | 可选     | `2`           | SUT 独立空闲回收时长（早于 daemon idle 回收 SUT；见 §7）                       |
| `sut.lifecycle.*`             | 可选     | `120/45/5`    | build / ready / grace 超时秒数                                                |

**`faked` 块（引擎参数；全部可选，缺省即「按注册的 endpoint 全部以随机端口启用」）：**

| 字段                      | 必填 | 默认值        | 说明                                                                 |
| ------------------------- | ---- | ------------- | -------------------------------------------------------------------- |
| `faked.enabled`           | 可选 | `false`       | 是否启用 faked 引擎                                                  |
| `faked.grpc.endpoints`    | 可选 | 全部 `init()` 注册的 endpoint | 列表项用于**覆盖某 endpoint 的监听地址**；省略则全部用随机端口 |
| `faked.grpc.endpoints[].id` | 列表项内必填 | — | 必须与某个静态注册的 `Endpoint.ID()` 对齐                            |
| `faked.grpc.endpoints[].listen` | 可选 | `127.0.0.1:0` | 覆盖该 endpoint 监听地址                                       |
| `faked.http.proxy_listen` | 可选 | `127.0.0.1:0` | MITM 代理监听地址                                                    |
| `faked.http.ca_cert_file` | 可选 | `""`          | 与 `ca_key_file` 都空 → 动态 CA 模式                                 |
| `faked.http.ca_key_file`  | 可选 | `""`          | 与 cert 必须共现或共空（半填拒绝启动）                              |

**跨字段校验：**
- `sut.enabled=true`：`binary_path` 与 `source_dir` 至少一非空；且必须已 `RegisterSUT`（§4）。
- `faked.http`：`ca_cert_file` 与 `ca_key_file` 要么都填（静态 CA）要么都空（动态 CA）；半填拒绝启动。

**相对现状的删减：**
- 无 `faked.runtime_dir` / `config_path` / `source_dir` / `binary_path` / `lifecycle`：内嵌后这些全不存在。
- 删 `http.endpoints`（host 列表）：HTTP endpoint 是否启用由 `init()` 自注册唯一决定（§9.3）。
- 删 `grpc.endpoints[].env_key` / `enabled`：`env_key` 移到 `Endpoint.EnvKey()`（§9.2）；启用语义由「是否注册 / 是否在列表覆盖」表达。
- `daemon.token`：每次 daemon 启动自动生成，写 `mtkit.runtime.json`，不进配置。

---

## 3. mtkit.runtime.json（含 faked 地址）

daemon 启动后写出，client / SUT boot plan 读取。faked 内嵌，故其地址作为 mtkit runtime 的字段，**无独立 faked runtime 文件**。

```json
{
  "control_addr": "127.0.0.1:53412",
  "token": "…",
  "faked": {
    "grpc_endpoints": { "MSSIOT_IT_RPC_FORUM_ADDR": "127.0.0.1:53420" },
    "proxy_addr": "127.0.0.1:53421",
    "ca_cert_file": "/…/runtime/faked/generated-ca.crt"
  }
}
```

- `control_addr`：daemon 主 control 端口；**faked rule 注册也打到这里**（`/v1/grpc/rules`、`/v1/http/rules`），与 lease / fixtured 共用（设计 §4.3）。`mtkit/client/fake` 读此地址即可，无独立 faked 发现。
- `faked.grpc_endpoints`：map 的 **key 即 `env_key`**（由 `Endpoint.EnvKey()` 提供，§9.2），value 是该 fake gRPC 服务监听地址；boot plan 直接遍历设 SUT env。
- `faked.proxy_addr` / `ca_cert_file`：供 SUT env 注入（§8）。
- `faked` 段在 `faked.enabled=false` 时省略。

---

## 4. mtkit 门面 API

`SUTBootPlan` 是 Go 接口，无法序列化进 JSON，从 `Config` 移出、改注册式。

### 4.1 目标 API

```go
// New 创建 MTKit 实例。
//   project：应用身份标识，用于推导默认配置/runtime 路径（写死在调用方代码）。
//   cfg：为 nil 时按 §1.2 优先级从文件加载；非 nil 时直接使用。
func New(project string, cfg *Config) (*MTKit, error)

// LoadConfig 暴露配置加载逻辑（MTKIT_CONFIG_PATH → 默认路径），供调用方自定义后传给 New。
func LoadConfig(project string) (*Config, error)

// RegisterContainers 注册容器定义（daemon 模式装配容器集）。
func (t *MTKit) RegisterContainers(fn RegisterContainersFunc) *MTKit

// RegisterSUT 注册 SUT 启动计划（sut.enabled=true 时必需）。
func (t *MTKit) RegisterSUT(plan SUTBootPlan) *MTKit

// Run 是 TestMain 的统一执行入口。
func (t *MTKit) Run(m Runnable) int
```

`SUTBootPlan` 瘦身——生命周期参数（enabled / 各 timeout / idle_ttl / 构建源）全部移入配置（§2 `sut` 块），接口只保留无法序列化的「代码事实」：

```go
type SUTBootPlan interface {
    // Command 构造 SUT 启动命令；binaryPath 为引擎构建产物，
    // in 提供容器映射端口 + faked 地址，用于动态注入 env（DSN、fake 地址等）。
    Command(ctx context.Context, in StartSUTInput, binaryPath string) (*exec.Cmd, error)
    // ProbeAddrs 返回 TCP 就绪探测地址（与 Command 注入的 SUT 监听地址一致，故同处代码避免漂移）。
    ProbeAddrs() []string
}
```

> 现状接口的 `IsEnable` / `GetIdleTTL` / `GetReadyTimeout` / `GetGracePeriod` 全部删除——环境相关调优归配置；`GetCommand` 内的 `go build` 拆为配置声明的构建步骤（§5.3），`Command` 只构造「运行」命令。

### 4.2 调用侧形态

```go
// test/infra/run.go（改动后）
import (
    _ "mssiot_user/test/fake/grpc" // endpoint 自注册（§0.3.2）
    _ "mssiot_user/test/fake/http"
)

func Run(m *testing.M) int {
    kit, err := mtkit.New("mssiot_user", nil) // nil → 从 MTKIT_CONFIG_PATH / 默认路径加载
    if err != nil {
        log.Printf("mtkit.New failed: %v", err)
        return 1
    }
    return kit.
        RegisterContainers(registerDefaultContainers).
        RegisterSUT(newSUTBootPlan()).
        Run(m)
}
```

`Run` 前必须已 `RegisterContainers`（daemon 模式需要）；`sut.enabled=true` 时必须已 `RegisterSUT`，否则启动校验报错。**faked 无需注册**——endpoint 经 blank-import 的 `init()` 自注册，daemon 启动 faked 引擎时从 registry 取。

---

## 5. faked 引擎在 daemon 内的装配（内嵌）

faked 不再走 `proc`。daemon（orchestrator）在进程内直接持有 faked 引擎实例，与容器、fixtured 同级。

### 5.1 引擎入口

迁入的 `mtkit/faked` 包对外暴露（由现 `daemon.go` 改造而来）：

```go
// pkg/mtkit/faked
type Config struct {
    GRPCEndpoints []EndpointListen // 可选 listen 覆盖；空 = 全部随机端口
    ProxyListen   string
    CACertFile    string
    CAKeyFile     string
    RuntimeDir    string // = <mtkit.runtime_dir>/faked，落调用日志与动态 CA
}

// Engine 是 daemon 进程内的 faked 实例。
type Engine struct { /* grpc set + http proxy + ca bundle + call loggers */ }

// New 据配置与已 init() 注册的 endpoint 构造引擎（尚未开 listener）。
func New(cfg Config) (*Engine, error)

// Start 开 gRPC server set + HTTP MITM proxy（control 不在此开，见 §5.2），返回地址。
func (e *Engine) Start(ctx context.Context) (Addrs, error)

// RegisterControl 把 rule 注册 handler 挂到 daemon 主 mux（/v1/grpc/rules、/v1/http/rules）。
func (e *Engine) RegisterControl(mux *http.ServeMux)

// Stop 关闭所有 listener、刷盘并关调用日志。
func (e *Engine) Stop(ctx context.Context) error

type Addrs struct {
    GRPCEndpoints map[string]string // env_key → addr
    ProxyAddr     string
    CACertFile    string
}
```

相比现状 `Daemon.Start`：去掉「开 control listener」「写 faked.runtime.json」两步——control 改挂 daemon 主 mux（§5.2），地址改由 orchestrator 写入 `mtkit.runtime.json`（§3）。`Addrs.GRPCEndpoints` 的 key 用 `Endpoint.EnvKey()`（§9.2）。

### 5.2 control 并入 daemon 主 mux

faked 的 rule 注册 handler（现 `grpc/control_api.go`、`http/control_api.go` 的 `Register(mux)`）不再开独立 control listener，而是挂到 **daemon 主 control mux**（与 lease / fixtured 同端口，设计 §4.3）。client（`mtkit/client/fake`）POST 到 `mtkit.runtime.json.control_addr` 的对应 path。

> 数据面仍是独立 listener：每个 fake gRPC 服务一个 TCP listener、HTTP MITM 代理一个 listener——SUT 要连它们，必须真端口。只有**控制面**并入主 mux。

### 5.3 proc 引擎只服务 SUT

`proc` 包（`ManagedProcess`）保留，承载 SUT 的「(可选)构建 → 注入 env 启动 → TCP 就绪 → 优雅关停（兜底杀进程树）」。faked 不再是它的实例。

```go
// proc.Spec：被监督进程的可插拔点
type Spec struct {
    Name         string
    Build        *BuildSpec     // nil = 直接用现成二进制（binary_path 模式）
    Command      CommandFunc    // 构造启动命令（注入 env / workdir）
    Ready        ReadyFunc      // 就绪判定（SUT 用 ReadyTCP）
    IdleTTL      time.Duration
    BuildTimeout, ReadyTimeout, GracePeriod time.Duration
}

func New(spec Spec) *Process
func (p *Process) EnsureStarted(ctx context.Context) error // build → start(注入 env) → ready；幂等
func (p *Process) Stop(ctx context.Context) error          // Signal → GracePeriod → 进程树终止
func (p *Process) Snapshot() State
func (p *Process) IdleTTL() time.Duration

// 就绪策略
func ReadyTCP(addrs []string) ReadyFunc  // SUT：探测一组地址全部可连
```

SUT 装配：

```text
Build   ← BuildSpec{ SourceDir: cfg.sut.source_dir, OutputPath: <runtime_dir>/sut.bin }
          （cfg.sut.binary_path 非空则 Build=nil）
Command ← SUTBootPlan.Command（注入容器 DSN + faked 地址；动态，必须代码）
Ready   ← ReadyTCP(SUTBootPlan.ProbeAddrs())
IdleTTL ← cfg.sut.idle_ttl_seconds
*Timeout ← cfg.sut.lifecycle.*
```

> 现状 `ReadyFileThenTCP`（等 faked runtime 文件出现再 TCP）**不再需要**——faked 内嵌，`Engine.Start` 返回即就绪，无跨进程文件等待。该策略从 proc 移除（无消费者）。
> SUT 构建路径不再硬编码（消除现状 `sut.go` 的 `E:\…\cmd` bug）——`source_dir` 进配置。

---

## 6. 启动 / 关停顺序（全在 daemon 进程内）

现状 `test/infra/run.go` 是「测试进程先 build+fork faked → 等 runtime → 再交容器+SUT」的跨进程舞蹈。内嵌后全部坍缩为 daemon 进程内顺序：

```text
启动（Acquire 触发）：
  ensureInfraStarted（容器 + 全局 Init）
  → faked.Engine.New + Start（进程内开 gRPC/proxy listener）+ RegisterControl(主 mux)
  → 把 Addrs 写入 mtkit.runtime.json
  → SUT（proc.EnsureStarted，经 StartSUTInput 拿到 faked 地址注入 env）—— 最后启动

关停（shutdownServer，逆序）：
  SUT.Stop（proc）→ faked.Engine.Stop → stopContainersOnly
```

- **SUT 最后启动**：确保容器、Init 数据、faked 地址都已就位。
- **faked 无独立 idle**：它是 daemon 进程内对象，生命周期随 daemon；只有 SUT 有 `idle_ttl`（`IdleTTL>0` 无租约先回收）。reaper 只管 SUT。
- `acquireInFlight` 计数覆盖整个启动窗口，避免就绪前被 reaper 误回收。
- 设计 §4.2「容器 Init 与 faked 启动可并行」：本期**先串行**（实现简单、正确性优先）。

涉及改动的 mtkit 文件：

- `faked/`：迁入的引擎；`daemon.go`→`engine.go` 改造（去 control listener / runtime 文件，加 `RegisterControl` + 返回 `Addrs`）。
- `orchestrator/server.go`：`Daemon` 持 `*faked.Engine`（非 `proc.Process`）；`handleAcquire` 启动 faked → 写 runtime → 起 SUT；`shutdownServer` 逆序。
- `orchestrator/lifecycle.go`：`ensure*Started` / `stop*Only` 编排 faked 引擎 + SUT(proc) + 容器。
- `orchestrator/run.go` / `mtkit.go`：据 `faked` 配置块构造 `faked.Config`、装配 SUT 的 `proc.Spec`。
- 删除：现 `pkg/mtkit/faked/manager.go`（独立子进程监督装配器，内嵌后作废）。

---

## 7. SUT 环境注入与外部依赖接管

faked 启动后，daemon 把 faked 地址经 `StartSUTInput` 交给项目侧 boot plan 注入 SUT。SUT 外部依赖分两类：**gRPC 直连注入地址**、**HTTP(S) 经代理劫持**。

### 7.1 StartSUTInput（mtkit 把组装好的 env 直接给 boot plan）

mtkit 在容器与 faked 就绪后**统一组装**要注入 SUT 的全部环境变量（容器连接信息 + faked 连接信息），经 `StartSUTInput.Env` 交给 boot plan；boot plan 只叠加 SUT 特有 env。

```go
// pkg/mtkit/sut
type StartSUTInput struct {
    Project     string
    RuntimePath string
    RuntimeDir  string
    Env         map[string]string // mtkit 注入：容器连接信息 + faked 连接信息
}

// MergedEnv 返回 os.Environ() + in.Env + extra（后者覆盖前者），boot plan 一行拿到完整 env。
func (in StartSUTInput) MergedEnv(extra map[string]string) []string
```

> 两类 env 各归其位：**容器连接信息由各容器注册的 `SUTEnv` 回调产出**（启动后用容器句柄取 host:mappedPort 拼连接串，见 §7.5），**faked 连接信息由 daemon 从引擎地址产出**。daemon 合并两者填进 `Env`，并落进 `mtkit.runtime.json` 的 `sut_env` 段（排查用）。boot plan 不读任何文件、不查容器端口、不感知 faked。

### 7.2 注入变量集（mtkit 组装两类，boot plan 只加 SUT 特有）

mtkit 注入的两类（都在 `in.Env`）：

```text
# 容器连接（各容器 SUTEnv 回调产出）：
MSSIOT_IT_MYSQL_*_DSN=...   MSSIOT_IT_REDIS_ADDR=...   MSSIOT_IT_MONGO_URI=...   MSSIOT_IT_PULSAR_URL=...
# faked 连接（daemon 从引擎地址产出）：
<env_key>=<grpc_addr>              # 每个 gRPC endpoint 一个（key=Endpoint.EnvKey()，§9.2）；SUT 直连，不经代理
HTTP_PROXY / HTTPS_PROXY=http://<proxy_addr>
NO_PROXY=127.0.0.1,localhost      # 回环不经代理（见 §7.3）
MTKIT_CA_CERT_FILE=<ca_cert_file> # faked 根 CA 公钥路径（见 §7.4）
```

boot plan（`test/infra/sut.go`）只叠加 SUT 特有 env：`cmd.Env = in.MergedEnv({MSSIOT_IT_ENABLED, MSSIOT_ENV, MSSIOT_REGION})`。

### 7.3 HTTP_PROXY / NO_PROXY 语义（澄清「127.0.0.1 矛盾」）

faked 代理监听在 `127.0.0.1:<proxy_port>`，`NO_PROXY` 里也含 `127.0.0.1`——这**不矛盾**，两者是不同维度：

- `HTTP_PROXY` / `HTTPS_PROXY` 指定**代理自身地址**。
- `NO_PROXY` 过滤**请求目标地址（target host）**，不是代理地址。Go 的 `http.ProxyFromEnvironment` 按 target 判断是否绕过代理。

| SUT 出站请求 | target | 是否走代理 | 结果 |
|---|---|---|---|
| `https://accounts.google.com/...` | `accounts.google.com` | 不在 NO_PROXY → **走代理** | faked 劫持 ✓ |
| `http://127.0.0.1:xxxx/...`（自身健康检查 / 内部组件） | `127.0.0.1` | 命中 NO_PROXY → **不走代理** | 回环直连 ✓ |

所以 `NO_PROXY=127.0.0.1,localhost` **必要**：排除 SUT 自身回环流量被无意义塞进代理。

**由此引出一条硬约束（写入测试约定）：被 fake 的 HTTP(S) 外部依赖必须用真实域名访问，不能配成 `127.0.0.1`**——否则命中 NO_PROXY 绕过代理，fake 失效。

> gRPC fake 不受此影响：走「直连注入地址」（`MSSIOT_IT_RPC_FORUM_ADDR=127.0.0.1:<port>`），不经 HTTP 代理、不涉 NO_PROXY / CA。两条接管路径机制独立。

### 7.4 HTTPS 信任：动态根 CA + SUT 程序级 RootCAs（跨平台统一）

faked 对 HTTPS 外部依赖做 MITM：握手瞬间用**根 CA 私钥**现场签发对应域名的**叶证书**冒充服务端。SUT 需信任这张根 CA。

**机制要点：**

- **一张根 CA 通吃所有域名**：叶证书由 faked 握手时 per-host 动态签发（`faked/http/proxy.go` + `faked/http/ca/ca.go`），SUT 从不需预先知道任何叶证书——只验证「叶证书能否链到它信任的根 CA」。新增 HTTPS 依赖只需注册 endpoint，CA 不变。
- **SUT 只拿根 CA 公钥**（`MTKIT_CA_CERT_FILE` 指向 `.crt`）；私钥只在 daemon 进程内用于签发，从不出 daemon、从不进仓库。

**信任装填方式：SUT 程序级装入 `RootCAs`，跨平台一致、零部署、零系统库污染。**

- 排除「静态 CA 跟项目走」（共享私钥+系统信任 = MITM 后门）。
- 排除「每次启动写系统信任库」（所有平台都需管理员/交互授权，CI 不可行）。
- 选定「程序级 `RootCAs`」：用 faked **动态 CA**（每次跑临时生成、用完即弃），SUT 启动时从 `MTKIT_CA_CERT_FILE` 读公钥装入进程内存。Win/Linux/mac 一致，无系统库操作、无平台分支、无 `SSL_CERT_FILE`（仅 Linux 生效，已弃用）。

**SUT 侧落地：mtkit 把装填逻辑做成模板文件，SUT 按需拷贝到自己的 main 包（生产零足迹）。**

mtkit 在 `pkg/mtkit/addons/mtkit_ca_addon.go` 提供一个模板插件（`package main` + `//go:build mtkit_it`，`init` 读 `MTKIT_CA_CERT_FILE` → 把 CA 追加进 `http.DefaultTransport.RootCAs`，从 `SystemCertPool` 起步不丢系统根）。**SUT 直接把该文件拷贝到 `cmd/<sut>/` 下**即可——它本就是 main 包文件，`init()` 随 SUT 编入、在 `main()` 前自动执行，无需任何 import 触发。

- boot plan 构建 SUT 时加 `-tags mtkit_it`（boot plan 是测试代码、控制 `go build`，加 tag 免费）；生产构建无此 tag、逐字节不含该文件。
- 不走「import mtkit 的 addons 包」——这些模板用 `//go:build mtkit_it` 排除在 mtkit 自身构建外，只供拷贝。
- env 名 `MTKIT_CA_CERT_FILE`：注入方（boot plan，§7.2）与读取方（拷入的插件）须用同一字面量。
- 模板与用法见 `pkg/mtkit/addons/README.md`。

**两条通用性限制：**

1. **覆盖面**：只作用于用 `http.DefaultTransport` 的 client。本项目所有出站 HTTP（`httpcli` / `util` / `http.DefaultClient`）均走 DefaultTransport，一处全覆盖。若某 SUT 自建 `http.Transport` 并显式设 `TLSClientConfig`，该 client 绕过 override，需自行装 `RootCAs`。
2. **时序**：`init` 在 `main()` 前、服务起来前执行，改 `DefaultTransport` 无并发风险。但 **SUT 不应在 init 阶段发起需被 fake 的外部 HTTPS 请求**。

**适用前提**：CA / 代理整套仅在 SUT 有 **HTTPS 外部依赖**要 fake 时才需落地。当前若只 fake gRPC（forum，直连不涉 TLS）与明文 HTTP，则 §7.4 可记录在案、推后实现；§7.2/§7.3 的代理与注入在有任何 HTTP fake 时即需要。

### 7.5 容器连接信息归属容器注册（`SUTEnv` 回调）

容器连接串由**最懂如何拼的容器注册代码**就近组装，不让 boot plan 去猜：

```go
// pkg/mtkit/container
type ContainerRegistration struct {
    Name   string
    Start  func(ctx) (testcontainers.Container, error)
    Init   func(ctx) error
    // SUTEnv 在容器启动后由 daemon 调用，返回该容器注入 SUT 的 env（连接串等）。
    SUTEnv func(ctx, c testcontainers.Container) (map[string]string, error)
}
```

daemon 在容器就绪后**按 `Name` 取出该容器句柄**传给它自己的 `SUTEnv` 回调，回调用 `c.Host()` / `c.MappedPort("3306/tcp")` 取宿主映射地址拼连接串、设好 env 名返回。daemon 合并所有容器产物 + faked env，统一注入 SUT 并落盘 `sut_env`。

**为什么是「启动后回调」而非「注册时给值」**：testcontainers 把端口发布到随机宿主端口（为并发不冲突），**映射端口是容器启动前唯一不可知的量**（凭据 / 库名 / DSN 模板 / env 名启动前都已知）。故注册时只能给「构建闭包」，由 daemon 在启动后调用填入 `host:mappedPort`——既保住随机端口的并发隔离，又把连接串领域知识留在注册代码。若未来某容器需要启动后才知的更多信息（如随机生成密码打日志），同一回调也能经句柄读取，不破坏模型。

---

## 8. proc/ 包：进程引擎 + 进程树终止

`proc` 是通用进程包，承载 SUT 监督引擎与跨平台进程树终止：

```text
pkg/mtkit/proc/
├── process.go         # Process 引擎：Spec / EnsureStarted / Stop / Snapshot（泛化自 sut.Manager）
├── ready.go           # ReadyTCP 就绪策略（§5.3）
├── control.go         # 进程树终止平台无关签名
├── control_unix.go    # Setpgid + 杀负 pgid（平移自 sut/process_control_unix.go）
└── control_windows.go # JobObject + taskkill（平移自 sut/process_control_windows.go）
```

- `sut.Manager` 的 build/start/探活/grace/快照/重启计数整体上移到 `proc.Process`；`sut` 包瘦身为「`SUTBootPlan` + 据其和配置装配 `proc.Spec`」。
- **faked 不用 proc**（内嵌）。proc 的进程树终止只为 SUT（及其 `go build` 派生孙进程）服务。
- 注意 Windows 上 `Process.Signal(os.Interrupt)` 常无效须用 Kill、孙进程须经 JobObject 整树终止（`test-harness-design.md` §4.1 平台注意点）。

---

## 9. 配置面收敛决策

### 9.1 【已定】环境变量收窄到 2 个

见 §1.5。faked 内嵌后 `FAKED_*` 全删；全集 `MTKIT_CONFIG_PATH` + `MTKIT_RUN_MODE`。

### 9.2 【已定·并入本次】`env_key` 从配置移到 `Endpoint.EnvKey()` 接口

`env_key`（SUT 期望的环境变量名）是项目领域知识，不是运行时配置。移到接口方法后：

- `faked.grpc.endpoints` 项只剩 `id` + 可选 `listen`。
- 与 faked「endpoint 全靠 `init()` 静态注册」风格一致。

涉及改动：`faked/grpc/registry.go` 的 `Endpoint` 接口加 `EnvKey() string`；引擎据接口取值写 `Addrs.GRPCEndpoints` / `mtkit.runtime.json`；项目侧 `test/fake/grpc/forum.go` 实现该方法。

#### env 名的全链路（澄清「名字在哪定义、谁注入」）

`MSSIOT_IT_RPC_FORUM_ADDR` 这类 env 名**不在任何配置文件里**——它是项目领域知识，定义在项目 endpoint 的 `EnvKey()`，mtkit 全程只搬运、从不定义：

```text
定义  项目 test/fake/grpc/forum.go: EnvKey() 返回 "MSSIOT_IT_RPC_FORUM_ADDR"
        （命名约定 MSSIOT_IT_RPC_<SERVICE>_ADDR）
  ↓   daemon 启动 faked 引擎，用 endpoint.EnvKey() 作 key、监听地址作 value
写入  mtkit.runtime.json.faked.grpc_endpoints = { "MSSIOT_IT_RPC_FORUM_ADDR": "127.0.0.1:xxxxx" }
  ↓   经 StartSUTInput.Faked.GRPCEndpoints（§7.1）交给 boot plan
注入  test/infra/sut.go 遍历 map：cmd.Env += "MSSIOT_IT_RPC_FORUM_ADDR=127.0.0.1:xxxxx"
  ↓
消费  SUT 读 os.Getenv("MSSIOT_IT_RPC_FORUM_ADDR") 作 forum 拨号地址（消费侧改造，当前暂未启用）
```

**关键耦合**：这个名字同时出现在**两处项目代码**且必须一致——① fake endpoint 的 `EnvKey()`（决定 daemon 发布成什么名）；② SUT 消费代码的 `os.Getenv(...)`（决定读什么名覆盖依赖地址）。建议项目侧用**一个共享常量**让二者同源，避免字符串漂移（现状 `forum.go` 是「硬编码 + 注释对齐」，够用但脆）。mtkit 侧（config / runtime / `StartSUTInput`）只见到「`EnvKey()` 值 → 地址」的映射，**不持有也不校验名字本身**。

### 9.3 【已定·并入本次】删除 faked 配置中的 `http.endpoints`

HTTP endpoint 启用与否由 `init()` 自注册唯一决定，配置里的 host 列表冗余，且现状 `resolveHTTPEndpoints` 的「config 声明的必须在注册表找到」校验方向反了。删除该配置段与过滤逻辑，改「注册即启用」。

---

## 10. 文件命名与目录布局

| 文件                                    | 位置                       | 写者         | 读者                          |
| --------------------------------------- | -------------------------- | ------------ | ----------------------------- |
| `mtkit.config.json`                     | `~/.mtkit/<project>/` 或 `MTKIT_CONFIG_PATH` 指定 | 人 | mtkit |
| `mtkit.runtime.json`（含 faked 地址）   | `<runtime_dir>/`           | mtkit daemon | mtkit client、SUT boot plan   |
| `grpc_calls.jsonl` / `http_calls.jsonl` | `<runtime_dir>/faked/`     | faked 引擎   | 排查                          |
| `generated-ca.{crt,key}`                | `<runtime_dir>/faked/`     | faked（动态 CA 模式） | SUT / 调试            |
| daemon 日志 / SUT 日志 / SUT 构建产物    | `<runtime_dir>/`           | mtkit        | 排查                          |

> 不再有 `faked.config.json` / `faked.runtime.json`。

### 10.1 多人多环境示例

```text
开发者 A（Windows）  runtime_dir = C:\Users\a\.mtkit\mssiot_user\runtime
开发者 B（macOS）    runtime_dir = /Users/b/.mtkit/mssiot_user/runtime
CI（Linux）          runtime_dir = /tmp/mt/mssiot_user/runtime
```

三方各自维护 `~/.mtkit/<project>/mtkit.config.json`，**不提交版本库**；版本库提供 `mtkit.config.json.example` 模板。差异完全落在各自配置文件。

---

## 11. 实施步骤（自底向上，每步可编译）

1. **`proc/` 进程引擎**：把 `sut.Manager` 泛化为 `proc.Process` + `Spec`（§5.3）；实现 `ReadyTCP`；平移 sut 平台文件 → `proc/control_*.go`（§8）。先让 sut 改用 proc、行为等价（可编译、原 sut 测试跑绿）。
2. **mtkit 配置层**：定义 `Config` JSON 结构（`sut` 块 + `faked` 引擎块）+ `LoadConfig`（优先级 + 校验 + 默认值）；改 `New(project, cfg)` 签名。（§1、§2、§4）
3. **门面 API + env 改名**：`RegisterContainers` / `RegisterSUT` 注册式，`SUTBootPlan` 瘦身为 `Command`+`ProbeAddrs`；`runtime` 文件名 `mtkit.runtime.json`；`MT_MODE`→`MTKIT_RUN_MODE`、删 `MT_RUNTIME`（`constant/env.go` + `orchestrator/run.go` + `orchestrator/client/autostart.go`）。（§4、§9.1、§10、§1.5）
4. **faked 引擎迁入 mtkit**（§0.3）：按 §0.3.1 映射迁 `mtkit/faked` + `mtkit/client/fake` + `faked/{grpc,http,test}`；改写 import 路径；参数化 CA Subject；删原 `envvar`/`bootstrap`/`cmd`；`go mod tidy`。项目侧 `test/fake/{grpc,http}` 仅改 import。
5. **引擎改造为内嵌入口**（§5）：`daemon.go`→`engine.go`：去 control listener + 去写 faked.runtime.json，加 `RegisterControl(mux)` + `Start` 返回 `Addrs`；`grpc.endpoints` 解析改读 `mtkit.config.faked`；`env_key` 移 `Endpoint.EnvKey()`（§9.2）；删 `http.endpoints`（§9.3）；call log / 生成 CA 落 `<runtime_dir>/faked/`；删 `pkg/mtkit/faked/manager.go`。
6. **orchestrator 接入**（§6）：`Daemon` 持 `*faked.Engine`；`handleAcquire` 顺序 `容器+Init → faked.Start + RegisterControl → 写 runtime → SUT(proc)`；`shutdownServer` 逆序；reaper 只管 SUT。
7. **`StartSUTInput` + boot plan**（§7）：`StartSUTInput.Faked *FakedInfo`（结构化地址）；boot plan 据此注入 `<env_key>` / `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` / `MTKIT_CA_CERT_FILE`。
8. **项目 bootstrap 重写**（`test/infra/run.go` / `sut.go`）：用新 mtkit API；删旧 fork/build/wait faked 逻辑；`run.go` blank-import endpoint 包；删 `sut.go` 硬编码绝对路径与 `go build`（改 `BuildSpec` + `source_dir` 配置）；`mtkit/client/fake` 经 `mtkit.runtime.json.control_addr` 注册 rule（无发现链）。
9. **SUT 侧 CA 装填**（仅当有 HTTPS 外部依赖要 fake）：从 `pkg/mtkit/addons/mtkit_ca_addon.go` **拷贝**模板到 SUT 的 `cmd/<sut>/`（`//go:build mtkit_it` 的 main 包文件，§7.4）；boot plan 构建 SUT 时加 `-tags mtkit_it`，注入 `MTKIT_CA_CERT_FILE`（与拷入文件读取的名字一致）。
10. **回写设计文档**：`test-harness-design.md` §4.1（faked 由独立子进程改内嵌 + 理由）/ §8（部署形态表）/ §4.2（启动顺序）/ §4.3（control 合并）/ §10（P1 范围去 reload）；`faked-design.md` 配置章节（折进 mtkit 配置）与 CA 章节（动态 CA + 程序级 RootCAs）。

---

## 12. 验收

- `go test ./...` 在本地（Windows）与 CI（Linux）各自配置文件下跑绿，无任何 `MTKIT_CONFIG_PATH` / `MTKIT_RUN_MODE` 之外的环境依赖。
- faked 随 daemon 内嵌存在：多测试包并发只有一个 daemon、即一个 faked，无重复实例、无独立 faked 进程。
- daemon 关停后无 faked 残留（内嵌，随 daemon 退出即释放 listener）；SUT 及其孙进程（go build 派生）经 proc 进程树终止无残留。
- `mtkit/client/fake` 经 `mtkit.runtime.json.control_addr` 注册 rule 成功；无 `faked.config.json` / `faked.runtime.json` / `FAKED_*` env 依赖。
- 改 fake endpoint 后重跑 `go test`（重编测试二进制）即生效；无需手动管 faked 启停。
- （若有 HTTPS 外部依赖）SUT 在 Windows 与 Linux 上均能信任 faked 动态 CA 完成 HTTPS MITM——无系统信任库导入、无 `SSL_CERT_FILE`；生产构建（无 `-tags mtkit_it`）不含 CA 装填代码。

---

## 13. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 把可用的 `sut.Manager` 泛化为 `proc.Process` 引入回归 | 步骤 1 先做引擎泛化、让 sut 改用、行为等价、原 sut 测试跑绿，再继续 |
| 引擎迁入 mtkit 改 import 面广、易漏 | 引擎内部自引用、零业务耦合（已核实）；机械改写 + `go build ./...` 兜底；自测随迁验证行为 |
| faked 引擎链入测试二进制致体积 / 链接时间上升 | 内嵌的固有代价，re-exec 已认账（设计 §2）；仅构建层面、不影响正确性；如成实测痛点再议 |
| 控制面并入 daemon 主 mux 与 lease/fixtured 路由冲突 | path 前缀隔离（`/v1/grpc/rules`、`/v1/http/rules` vs lease/fixtured 路径）；mux 注册期校验无重复 |
| 改 fake endpoint 在 `test-env-up` 长活环境不自动生效 | 已知取舍（§0.4）：本期不做 reload；长活环境改 endpoint 需 `test-env-down/up`，文档约定 |
| 被 fake 的 HTTP(S) 依赖误配成 `127.0.0.1` → 命中 NO_PROXY 绕过代理 | 测试约定：HTTP(S) 外部依赖必须用真实域名（§7.3）；调用日志 `matched:false` 暴露漏接管 |
| SUT 自建 `http.Transport` 绕过 DefaultTransport 的 RootCAs override | §7.4 限制 1 已记；该 SUT 需自行装 RootCAs 或统一走共享 helper |

---

## 14. 高价值测试计划

原则：**只测本期改动引入/改变的机制**，优先回归风险高、跨平台易错的点；不重复 faked 既有 acceptance/smoke 覆盖（dispatch / stateful / keepalive / CA 生成等已在 `faked/test/`）。P0=必须、P1=应有。

### 14.1 proc 进程引擎（回归风险最高，P0）

泛化自 `sut.Manager`。用一个**会派生孙进程**的测试辅助程序驱动（复用 `faked/test/smoke` 已有孙进程模式）：

| 用例 | 验证点 | 层级 | 平台 |
|---|---|---|---|
| Build→Start→Ready happy path | `BuildSpec` 编译 → 启动 → 就绪 | 单元 | 全 |
| `binary_path` 模式 | `Build=nil` 跳过编译直接启动 | 单元 | 全 |
| Ready 超时 | 超 `ReadyTimeout` 返回错误**且进程被杀** | 单元 | 全 |
| 进程提前退出 | 就绪前退出 → 错误如实上抛 | 单元 | 全 |
| Stop 优雅 | grace 内正常退出，不强杀 | 单元 | 全 |
| Stop 超 grace 强杀 | 超 grace → 进程树终止，**孙进程一并消失** | 单元 | **Win+Linux 各跑** |
| `EnsureStarted` 幂等 | 已运行再调 → no-op | 单元 | 全 |
| `ReadyTCP` | 任一地址不可连即判未就绪 | 单元 | 全 |

### 14.2 配置加载与校验（便宜高收益，P0）

| 用例 | 验证点 | 层级 |
|---|---|---|
| 优先级 | 传入 cfg > `MTKIT_CONFIG_PATH` > 默认路径 > 报错 | 单元 |
| 路径解析但读取/解析失败 | fail fast，错误指明文件 | 单元 |
| 必填缺失 | `runtime_dir` 为空 → 报错 | 单元 |
| 默认值 | 可选项留空 → 填默认（`addr` / timeout / idle_ttl / faked listen） | 单元 |
| 跨字段 | `sut.enabled` 缺 `binary_path`+`source_dir` → 报错；`sut.enabled` 未 `RegisterSUT` → 报错；faked CA 半填 → 报错 | 单元 |

### 14.3 faked 内嵌启动 / control 合并（P0）

| 用例 | 验证点 | 层级 |
|---|---|---|
| 引擎启动返回地址 | `Engine.Start` 开 grpc/proxy listener 并返回 `Addrs`（key=env_key） | 单元 |
| control 挂主 mux | rule 注册打 daemon control 端口 `/v1/grpc/rules` 成功；与 lease/fixtured 路由不冲突 | 集成 |
| `env_key` 来自接口 | `Endpoint.EnvKey()` 值出现在 `Addrs` / runtime json 的 key | 单元 |
| 单实例 | 多测试包并发只有一个 daemon → 一个 faked，无重复 listener | 集成 |

### 14.4 启动/关停编排与 reaper（P1）

| 用例 | 验证点 | 层级 |
|---|---|---|
| 启动序 | `容器+Init → faked → SUT`；SUT 启动时能拿到 faked 地址 | 集成 |
| 关停逆序 | SUT → faked → 容器 | 集成 |
| reaper 只管 SUT | 无租约后 `idle_ttl>0` 的 SUT 先回收，faked 随 daemon 整体退出才释放 | 集成 |
| `acquireInFlight` | 启动窗口内不被 reaper 误回收 | 集成 |

### 14.5 SUT env 注入 / 发现（P0）

| 用例 | 验证点 | 层级 |
|---|---|---|
| 结构化注入 | boot plan 据 `StartSUTInput.Faked` 设 `<env_key>` / `HTTP(S)_PROXY` / `NO_PROXY` / CA env | 集成 |
| client 注册 rule | `mtkit/client/fake` 经 `mtkit.runtime.json.control_addr` 注册成功 | 集成 |

### 14.6 代理接管语义（微妙、P0）

锁住 §7.3 行为，防回归：

| 用例 | 验证点 | 层级 |
|---|---|---|
| 真实域名走代理 | 设 `HTTP(S)_PROXY` 后请求真实域名 → faked 命中（`matched:true`） | 集成 |
| 回环绕过代理 | 请求 `127.0.0.1` → 不经 faked | 集成 |
| 误配 127.0.0.1（负向） | fake 配了但 SUT 打 `127.0.0.1` → 未命中，**文档化反例** | 集成 |
| gRPC 直连不经代理 | gRPC fake 走注入地址直连，与代理/NO_PROXY 无关 | 集成 |

### 14.7 HTTPS 信任 / CA 注入（跨平台关键，P0——仅当有 HTTPS 依赖）

用一个**独立子进程**的最小客户端（`-tags mtkit_it` 构建，含 §7.4 `init`）经代理访问 fake HTTPS host：

| 用例 | 验证点 | 层级 | 平台 |
|---|---|---|---|
| 动态 CA 信任成功 | 注入 `MTKIT_CA_CERT_FILE` → 子进程 HTTPS MITM 握手通过、拿到 fake 响应 | 集成 | **Win+Linux 各跑** |
| 单 CA 通吃多域名 | 同一子进程访问两个 fake HTTPS host，均被同一根 CA 信任 | 集成 | 全 |
| 系统根不丢 | 从 `SystemCertPool` 起步追加，断言追加后仍含系统根 | 单元 | 全 |
| 生产零足迹 | **不带** `-tags mtkit_it` 构建：二进制不含 CA 装填代码，设 env 也不生效 | 单元/构建 | 全 |
| 缺 CA fail loud | 带 tag 但 CA 文件不可读 → `panic` | 单元 | 全 |

### 14.8 env 改名（P1）

| 用例 | 验证点 | 层级 |
|---|---|---|
| `MTKIT_RUN_MODE=daemon` 进 daemon 路径 | re-exec 分流正确；缺省/其他值走 client | 单元 |
| 无残留旧 env | 代码与测试不再读 `MT_MODE` / `MT_RUNTIME` / `FAKED_*` | 静态/grep |

> 落地顺序与 §11 对齐：每步配套写它引入机制的用例（步骤 1 配 §14.1，步骤 2/3 配 §14.2/14.8，步骤 5/6 配 §14.3/14.4，步骤 7/8 配 §14.5/14.6，步骤 9 配 §14.7），避免最后集中补测。
