# mtkit 架构

本文给出 mtkit 的**宏观结构**：模块如何划分、进程如何分流、控制链路与数据流如何流转。它只到模块边界为止，不深入单个模块的实现——各模块的内部设计见 `docs/modules/` 下的专题文档，逐节有链接。

- 术语以 [`CONTEXT.md`](../CONTEXT.md) 为权威来源（daemon、CaseScope、fixtured、faked、provision 等）。
- 怎么安装、配置、调用见 [`README.md`](../README.md)。
- 历史实施方案与迁移记录已归档至 [`docs/archive/`](archive/)，架构决策见 [`docs/adr/`](adr/)。

> 范围约定：本文出现的 HTTP 路由（`/v1/acquire`、`/v1/fixture/alloc`、`/v1/grpc/rules` 等）都是 daemon 与测试进程之间的**内部 wire 协议，不是对外稳定契约**。使用者通过 Go API（`mtkit.*`、`fixture.*`、`fake.*`）间接驱动它们，不应直接编码依赖这些路由。

---

## 1. 定位

mtkit 是 Go 应用的**集成测试基础设施框架**。它在 `TestMain` 一个入口里，为被测服务（SUT）准备好一整套真实依赖环境并保证就绪后再跑测试：

- **容器编排**：按声明启动 MySQL / Redis / MongoDB 等测试容器，管理生命周期与清理。
- **被测服务管理**：编译、启动、就绪探测、优雅停止 SUT 进程，并把依赖连接信息注入它。
- **外部依赖假冒（faked）**：用内嵌的 fake gRPC server 与 HTTP(S) MITM 代理拦截 SUT 的出站调用，按规则返回响应。
- **唯一测试数据（fixture）**：跨进程、跨用例生成无碰撞的 UUID / Email / Phone 等数据。
- **前置数据幂等装配（provision）**：跨测试进程只执行一次建表 / 索引 / 种子数据。

跨多个测试进程时，这些基础设施由**一个共享 daemon** 持有、靠租约复用，避免每个测试包重复拉起容器。

---

## 2. 宏观模块图

```
                       测试进程（client 模式）
  TestMain ─► mtkit.New(project,cfg)
                .RegisterContainers(fn)
                .RegisterSUT(plan)
                .WithSchema(resources)
                .Run(m)                        ← 统一入口
                    │
       ┌────────────┴─────────────┐  按 MTKIT_RUN_MODE 分流
       │ orchestrator.Run         │
       ▼                          ▼
   client 模式                 daemon 模式（re-exec 同一二进制）
   ┌─────────────────┐         ┌──────────────────────────────────────┐
   │ orchestrator/   │  HTTP   │ orchestrator/  Daemon                 │
   │   client        │◄───────►│   server  租约面 + 懒就绪编排          │
   │ 发现/自启 daemon │ 单端口  │   lifecycle  infra→faked→env→SUT      │
   │ 持租约+心跳      │         ├──────────────────────────────────────┤
   │ 跑 m.Run()      │         │ container  容器启停/清理（testcontainers）│
   └────────┬────────┘         │ faked      gRPC fake + HTTP MITM 代理   │
            │                  │ sut/proc   SUT 编译/启动/探活/停止       │
   测试代码内调用：            │ fixtured   token 发号 + provision 账本   │
   ┌────────▼────────┐         │ runtime    mtkit.runtime.json 读写/发现 │
   │ client/fixture  │  HTTP   └──────────────────────────────────────┘
   │ client/fake     │◄──── 经 daemon 单端口的 /v1/* 内部协议
   └─────────────────┘
                                   ▼ faked 数据面（独立监听）
                             SUT ─► fake gRPC endpoints（直连）
                                 ─► HTTP(S) 经 MITM 代理
```

模块职责一句话：

| 模块 | 职责 | 详见 |
| --- | --- | --- |
| `mtkit`（顶层门面） | 配置归一化、装配校验、模式分流入口 | 本文 §3–§4 |
| `orchestrator` | daemon/client 分流、租约、懒就绪编排、re-exec 自启 | [modules/orchestration.md](modules/orchestration.md) |
| `proc` / `sut` | 通用进程生命周期原语 / SUT 语义边界 | [modules/orchestration.md](modules/orchestration.md) |
| `runtime` | `mtkit.runtime.json` 序列化、路径计算、daemon 发现 | [modules/orchestration.md](modules/orchestration.md) |
| `container` | 容器并发启动、幂等、孤儿清理 | [modules/container.md](modules/container.md) |
| `client/fixture` + `fixtured` | 唯一数据生成（HiLo + Feistel FPE）+ provision 账本 | [modules/fixture.md](modules/fixture.md) |
| `faked` + `client/fake` | fake gRPC / HTTP(S) 引擎 + 规则注册客户端 | [modules/faked.md](modules/faked.md) |

---

## 3. 运行模式与 re-exec 模型

mtkit **不另起独立守护程序**，而是把同一个测试二进制 re-exec 成 daemon。`orchestrator.Run` 按环境变量 `MTKIT_RUN_MODE` 分流：

- 未设 → **client 模式**：发现 daemon，没有就自启，持租约跑 `m.Run()`。
- `=daemon` → **daemon 模式**：作为基础设施宿主常驻，直到空闲超时或收到信号。

**自启 daemon**（`orchestrator/client/autostart.go`）的关键点：
- 用 `os.Executable()` 重启**当前测试二进制**并设 `MTKIT_RUN_MODE=daemon`，配 `-test.run=^$`（不跑任何用例）。选择 re-exec 自身而非按源码目录重建，是为了避开 `runtime.Caller`/工作目录在不同编译参数与执行器下的不稳定。
- 并发 `go test ./...` 会有多个进程同时尝试自启，靠 `runtime 文件 + 旁路 `.start.lock`` 单飞：同一时刻只有一个进程真正 Start，其余等 runtime 就绪后复用。
- Windows 上先把二进制**复制一份**再启动，避免 `go test` 临时二进制被文件锁住导致父进程结束时无法回收。

为什么是单二进制 re-exec：避免多进程 / 多端口 / 多版本与构建路径推导的复杂度，daemon 与 client 共享完全相同的代码与注册逻辑。

---

## 4. 模块集成与生命周期协调

各基础设施模块（容器 / faked / SUT / fixtured / 租约）并不互相认识——它们由 daemon **统一拥有与调度**。这一节讲它们如何被组装、按什么顺序启停、并发与回滚边界划在哪。

### 4.1 Daemon 是组合根

`orchestrator.Daemon`（`server.go`）在 `New()` 里一次性构造全部模块，运行期只持有它们的句柄并按序调度，不侵入各模块内部。每个模块对外都收敛成一致的**「启动 / 停止 / 状态快照」三件套**，调度逻辑因此极薄：

| 模块 | 构造（`Daemon.New`） | 启动 | 停止 | 状态 |
| --- | --- | --- | --- | --- |
| `container.Bundle` | `NewBundle(regs)`（构造即校验注册） | `StartAll` | `StopAll` | `Started` / `Containers` |
| `faked.Server` | `mtfaked.New(cfg)`，**仅启用时**，否则 `nil` | `Start`→数据面地址 | `Stop` | `Snapshot` |
| `sut.Manager` | `sut.NewManager(cfg, plan, input)` | `EnsureStarted` | `Stop` | `Snapshot` |
| `fixtured.Server` | `fixtured.New()` | 路由随 daemon 挂载 | 随 daemon 退出 | — |
| `leaseStore` | `newLeaseStore()` | — | `gc` 回收过期 | `activeCount` |

faked 未启用时句柄为 `nil`，所有调度点以 `if d.faked != nil` 兜底，零开销跳过。

### 4.2 client 侧租约生命周期（`orchestrator/client/run.go: RunUnderLease`）

```
New(发现/自启 daemon) ─► Acquire(租约) ─► StartHeartbeat ─► m.Run() ─► stopHB ─► Release(code)
```

- 租约固定 `LeaseTTL = 2s`，心跳间隔 `HeartbeatInterval = 1s`（恒为 TTL 的一半，见 `constant/lease.go`）。
- 先停心跳再 Release：避免 Release 阻塞期间还在续租，导致 daemon 迟迟不回收 SUT。
- Release 携带 `m.Run()` 的退出码，最终被 `Run` 如实透传为进程退出码。

### 4.3 daemon 启动：只做轻量装配（`Daemon.Start`）

`Start` 本身**不启动任何容器 / SUT / faked**，只把控制面立起来：

```
清理同名孤儿容器 ─► 写空 provision 账本 ─► 挂路由(lease + fixtured alloc + faked control，共用一把 withAuth)
  ─► 绑端口 ─► 写 runtime.json 基线(addr/token/pid) ─► 起 reaper 协程 ─► Serve
```

> 时序约束：账本必须在「对外可发现」（绑端口 / 写 runtime）之前写空，否则 client 拿到租约后可能读到上一代残留账本、误判已 applied 而跳过建表。

### 4.4 首次 Acquire：跨模块就绪编排（`lifecycle.go: ensureEnvReady`）

真正的环境启动是**懒触发**的——在首个 `Acquire` 时才发生，整段持 `readyMu` 串行，后续 Acquire 在锁内走幂等空过。四步严格有序，因为后一步依赖前一步的产物：

1. **容器**（`Bundle.StartAll`）：以最多 4 个 worker（`maxCreateConcurrency`）并发创建；任一容器 `Start` 失败立即 cancel 其余并逆序 `Terminate` 已建容器。全部起好后**串行**执行各注册项的 `Init`（此刻闭包可访问任意已启动容器，用于建库 / 写种子），`Init` 失败同样整体回滚。容器被假定相互独立，**不保证注册顺序即启动顺序**——有跨容器初始化关系的必须放进 `Init`。
2. **faked**（`ensureFakedStarted`）：`Start` 返回数据面地址，转成「待注入 env」暂存，并写入 runtime 基线的 `Faked` 段。
3. **env 组装**（`prepareSUTEnv`）：把各容器 `SUTEnv` 回调产物与 faked env 合并，**同名 key 冲突立即报错**（拒绝静默覆盖），落盘到 `runtime.json` 的 `SUTEnv` 段，并经 `Manager.SetInjectEnv` 交给 SUT。
4. **SUT**（`Manager.EnsureStarted`）：若配了 `source_dir` 先 `go build` 到 `.mtkit-build/<project>`，再按 boot plan 的 `Command`（执行时读最新注入 env）启动，`ReadyTCP(ProbeAddrs)` 探活直到端口可连才算就绪。

四步全部就绪才发放租约——用例拿到的保证是可用环境，而非半初始化状态。

### 4.5 停止编排与并发分层

- **停止逆序**（`shutdownServer`，`shutdownOnce` 保证只跑一次）：先停 SUT、再停 faked、最后停容器——避免容器先下线后 SUT 刷出大量无意义错误。
- **谁锁什么**（分层而非一把大锁）：
  - `orchestrator.readyMu`：串行化**跨模块**就绪准备，因为 §4.4 各步共写 daemon 共享态（`runtimeInfo` / `fakedEnv` / `sutEnvReady`）并重写同一 runtime 文件。
  - `container.Bundle.mu` + `started` 标志：容器自身启停互斥与幂等（`StartAll`/`StopAll` 共用），不与 `readyMu` 叠加。
  - `acquireInFlight` 原子计数：Acquire 执行中（还没落租约）也算「忙」，防止 reaper 在 infra 刚起好的窗口抢先回收。
- **回滚边界**：容器层失败由 Bundle 自身回滚到干净态（下次 Acquire 重来）；SUT / faked 启动失败则 Acquire 返回 5xx，已起部分保留，等下次重试或空闲回收。

### 4.6 空闲回收（`loopReaper`，tick = 2s）

双层空闲策略：活跃租约为 0 且无 in-flight Acquire 时，先按 **SUT IdleTTL**（默认 2s）停 SUT，再按 **daemon IdleTTL**（门面默认 36s，orchestrator 兜底 15s）关闭整个 daemon 并清理 runtime 文件与容器。持租约 + 持续心跳可无限抑制回收；这正是多个测试包复用同一组容器的机制。

---

## 5. 数据流

### 5.1 依赖连接注入链（容器 + faked → SUT）

这是框架最核心的数据流：SUT 不自己去查 Docker，所有连接信息由 daemon 组装后经环境变量注入。

```
容器 SUTEnv 回调 ┐
                ├─► prepareSUTEnv 合并(同名 key 冲突报错)
faked 数据面地址 ┘        │
                         ├─► 写入 runtime.json 的 SUTEnv 段（发布产物）
                         └─► sut.Manager.SetInjectEnv → SUT 进程环境变量
```

faked 注入的 env（`lifecycle.go: fakedInjectEnv`）：
- 每个 gRPC endpoint：`<env_key>=<host:port>`，SUT **直连** fake gRPC，不经代理。
- HTTP(S)：`HTTP_PROXY` / `HTTPS_PROXY` 指向 MITM 代理，`NO_PROXY=127.0.0.1,localhost`。
- `MTKIT_CA_CERT_FILE` 指向动态 CA 公钥，供 SUT 侧 CA 插件（[`addons/`](../addons/)）装填信任。

### 5.2 fixture token 流（唯一数据）

```
CaseScope.UUID()/Email()/... ─► blockSource.Next()
   命中本地区块 → 0 锁自增返回
   区块耗尽   → POST /v1/fixture/alloc 向 daemon 补块(HiLo)
                ─► fixtured.Allocator 原子发不相交区块
```

唯一性的单一真相源是 daemon 侧的原子计数器；client 用 HiLo 缓存把绝大多数取号变成本地无锁自增。详见 [modules/fixture.md](modules/fixture.md)。

### 5.3 fake 规则流（控制面 vs 数据面分离）

```
控制面：fake.Client.GRPC()/HTTP().On(...).Return(...)
        ─► POST /v1/grpc/rules | /v1/http/rules（daemon 主端口）─► 注入 faked 引擎规则库
数据面：SUT 出站调用 ─► fake gRPC endpoint / HTTP 代理 ─► 引擎按规则匹配返回 ─► 记调用日志
```

规则注册走 daemon 控制端口，实际拦截走 faked 独立数据面监听。详见 [modules/faked.md](modules/faked.md)。

### 5.4 两层前置数据装配

前置数据分两层，落在生命周期的不同时点，**别混用**：

| 层 | 放哪 | 时点 | 装什么 |
| --- | --- | --- | --- |
| **全局引导** | 容器注册的 `Init` 回调 | 容器就绪后、**SUT 启动前**（daemon 侧，见 §4.4） | SUT 启动**必需**且全场一致的：建库本身、Pulsar tenant/namespace、SUT boot 必需的表 |
| **按包 provision** | `WithSchema([]fixture.Resource)` | 持租约后、`m.Run()` 前（**client 侧**，按包幂等） | 各测试包自己声明的测试用表 / 索引 / 种子数据 |

判据：**SUT 启动就依赖的** → 放容器 `Init`（否则 SUT 起不来）；**只有测试用例才需要的** → 放 `WithSchema`（按包最小集、跨进程幂等，见 [modules/fixture.md B](modules/fixture.md#b-前置数据装配provision)）。

---

## 6. 模块边界与依赖方向

最重要的解耦设计是 **client 侧与 daemon 侧通过 protocol 包通信，互不 import**：

- `client/fixture` ↔ `fixtured`：共享 `fixtured/protocol`，互不依赖。
- `client/fake` ↔ `faked/{grpc,http}`：共享各自 `protocol`，互不依赖。
- `orchestrator/client` ↔ `orchestrator`(daemon)：共享 `orchestrator/protocol`。

底层通用抽象（`proc` / `runtime` / `constant`）不依赖上层，可被各模块复用。这条「protocol 接缝」既是进程边界，也是测试时进程内构造的注入点。

---

## 7. 内部 wire 协议清单

全部挂在 daemon **单一主端口**，统一经 token 鉴权（`X-Mt-Token` 头，见 `orchestrator/protocol`）。**均为内部协议，非对外契约。**

| 路由 | 方法 | 用途 | 定义位置 |
| --- | --- | --- | --- |
| `/v1/acquire` | POST | 获取租约（触发首次懒就绪） | `orchestrator/protocol` |
| `/v1/heartbeat` | POST | 续租 | `orchestrator/protocol` |
| `/v1/release` | POST | 释放租约（带退出码） | `orchestrator/protocol` |
| `/v1/state` | GET | daemon 状态快照（infra/SUT/faked 就绪等） | `orchestrator/protocol` |
| `/v1/fixture/alloc` | POST | 分配 token 区块（HiLo 补块） | `fixtured/protocol` |
| `/v1/grpc/rules` | POST | 注册 gRPC fake 规则 | `client/fake` ↔ `faked/grpc` |
| `/v1/http/rules` | POST | 注册 HTTP fake 规则 | `client/fake` ↔ `faked/http` |

faked **数据面**不在此端口：gRPC 各 endpoint 独立监听、HTTP 经 MITM 代理监听，地址经 `runtime.json` 与注入 env 发布。

---

## 8. 关键技术决策（指针）

- **单二进制 re-exec daemon**：见 §3。替代「独立守护程序 / 按源码重建」。
- **Daemon 作组合根 + 统一三件套**：见 §4.1。模块互不认识，由 daemon 按序调度，调度层极薄。
- **懒就绪 + 双层空闲回收**：见 §4.4、§4.6。基础设施按需在首次 Acquire 启动，空闲自动回收降低资源泄漏。
- **provision 用文本账本而非 MySQL**：见 [ADR-0001](adr/0001-provision-registry-text-ledger.md)，接口坍缩为单一 `EnsureOnce`。
- **HiLo + Feistel FPE 的唯一性**：唯一性源单一化到 daemon 原子计数，client 本地无锁取号；保形加密保证唯一值同时满足 Email/Phone 等格式。见 [modules/fixture.md](modules/fixture.md)。
- **gRPC 与 HTTP 两套独立 faked 引擎**：协议语义差异大（gRPC status code vs HTTP 状态码、直连 vs MITM 代理），不强行统一。见 [modules/faked.md](modules/faked.md)。

---

## 9. 渐进式阅读指引

| 你想了解 | 去哪 |
| --- | --- |
| 怎么装、怎么配、最小可用例子 | [README.md](../README.md) |
| daemon/client、租约、SUT/进程管理 | [modules/orchestration.md](modules/orchestration.md) |
| 容器声明与编排 | [modules/container.md](modules/container.md) |
| 唯一数据与前置数据装配 | [modules/fixture.md](modules/fixture.md) |
| 假冒 gRPC / HTTP(S) 外部依赖 | [modules/faked.md](modules/faked.md) |
| 某术语到底指什么 | [CONTEXT.md](../CONTEXT.md) |
