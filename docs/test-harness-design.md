# 集成测试平台总模块设计（顶层架构）

> 模块正式命名为 **`mtkit`**（mt = mcharvvvy test，作者名首字母缩写；daemon 二进制叫 `mtkitd`）。由 `testcontainerd` 演进而来，容器管理已只是其中一个子模块。本文用「**mtkit**」指代整个模块。

本文记录承载 API 集成测试能力的「总模块 + 共享 daemon」的**顶层架构**：模块边界、daemon 职责、消费面 API、faked 部署形态、启动编排。三份文档分工：

- `api-test-framework-design.md`：**测试作者视角**——测试组织、fixture 分层、数据隔离原则。
- `faked-design.md`：**faked**（fake 外部依赖）的实现细节。
- 本文：**模块 / daemon 视角**——上述能力靠什么样的模块结构与共享服务承载。

本文**不**展开 fixture 层落地细节（`CaseScope` 原语 / `Requirement` / `Registry` / ID 分配 / seeder base）——那部分另起一份。

---

## 1. 为什么「一个总模块 + 一个共享 daemon」

硬约束：`go test ./...` 默认按包并发，**每个测试包是独立进程，彼此无共同父进程**。容器、fake server、资源 provision、唯一 ID 分配——这些跨进程共享的东西都需要一个**中央权威**协调，否则无人负责「建一次、复用、谁清理」。

`testcontainerd` 已具备这个 daemon-client 基座（跨包共享容器、租约、双层 IdleTTL、按 project 隔离的 runtime 文件）。因此不另起炉灶，而是：

- **把 `testcontainerd` 提升为 `mtkit`**，容器管理**降为一个内部子模块**；
- SUT、faked、协调内核、fixture base 作为**同级内部子模块**；
- 一个 daemon 二进制承载它们。

**关键区分**：「一个 daemon 进程」（部署层，必需）≠「一个扁平 Go 包」（代码层，反对）。关注点分离作为**内部包边界**保留，只是这些包共享同一个 daemon、同一个模块。

### 1.1 `mtkit` 如何解除 `testcontainerd` 的 `t.Parallel()` 限制（核心增值）

裸 `testcontainerd` 禁止 `t.Parallel()`：它不做外部状态隔离，同一组容器 / SUT / DB schema 被所有测试包共用，并发改数据会互相覆盖。

而 fixture 层（`CaseScope` + 数据分桶 + 跨进程全局唯一 PrimaryScope + `BindToScope`）**恰是裸 `testcontainerd` 假设缺失的那层隔离**。所以 `mtkit` 相对裸库的核心增值是：

> 把「共享可变状态、不许并发」升级为「逻辑分桶状态、可放心并发」——对**遵守分桶纪律**的用例解除 `t.Parallel()` 限制，触碰全局状态的用例仍走独占（exclusive case）。

fixture 层因此有一项**硬职责**：交付这层隔离。两级并发都靠它兜底：

- **包级并发**（`go test ./...` 多进程）：靠跨进程全局唯一 PrimaryScope（协调内核的 ID 分配，见 §4.3）。
- **用例级并发**（`t.Parallel()`）：靠 CaseScope 分桶。

---

## 2. daemon 形态：re-exec 模型（**已定**）

`testcontainerd` 的 daemon **不是独立二进制**：client 发现无可用 daemon 时 **re-exec 当前测试二进制**、注入 `TCD_MODE=daemon`，子进程走**同一个 `TestMain` → `tcd.Run(m)`** 进 daemon 路径。推论：**测试二进制 == daemon 二进制**（同一二进制、运行时模式开关）。

**好处**：daemon 天然拥有测试二进制编译进来的项目内容——容器注册、schema 声明、**以及经 `init()` 自注册的 fake endpoint**（faked 内嵌后随测试二进制编译进来，见 §4.1 已修订）。无需为每个项目单独构建任何二进制。

**代价**：re-exec 把 docker SDK 等经 `testcontainerd` 链进测试二进制，所以**拿不到瘦测试二进制**。但这只是**构建层面**代价（`go test ./...` 链接时间 / 二进制体积），**不影响正确性或能力**；运行时仍解耦——client 模式的测试代码只经「网线」（control API + runtime 文件）触达 daemon，只持 thin client + DTO。

**结论**：走 re-exec。更简单、daemon **永远等于最新代码**（改代码只需 `go test`，无独立 daemon 构建、无版本不同步）。备选「独立 daemon 二进制」能拿回瘦二进制，但要重写 re-exec 核心 + 按项目编译 daemon，**仅当链接时间在大规模下成为实测痛点再议**（先不提前优化）。

---

## 3. 模块边界与依赖方向

```text
mtkit（按 客户端 / 服务端 / plumbing 分层）
├── mtkit.go        # 门面：Config/New + 极薄 Run 转发（→ orchestrator，不依赖 client）
# ── 客户端区（测试进程 import，thin）──────────
├── client/
│   ├── client.go   # lease 客户端 + 合一门面（test runtime）
│   ├── fixture/    # ← 消费端 import：CaseScope 原语 / 生成器 / Requirement / Resource / Registry（原 fixturebase，包名 fixture）
│   └── fake/       # ← 消费端 import：faked rule 注册（原 faked client）
# ── 服务端区（daemon = mtkitd 进程内）────────
├── orchestrator/   # daemon 编排：生命周期、启动顺序、模式分流、子进程监督；持 daemon 主 HTTP mux
├── container/      # 容器管理（原 testcontainerd 核心，降为子模块）+ lease handler
├── sut/            # SUT 生命周期引擎 + boot plan 接口（从 container 拆出；段1 临时含进程监督，段2 抽 proc/）
├── fixtured/       # 协调内核：锁 / 内存 registry / ID 分配；handler 与 lease 共用 daemon 主端口（原 coord，服务端）
├── faked/          # fake engine + protocol + 服务端；**内嵌 daemon 主进程**（见 §4.1 已修订），由 daemon 进程内直接调用
# ── plumbing（leaf 共享）────────────────────
├── runtime/        # runtime 文件 + RuntimeInfo 发现 schema
└── constant/       # MT_MODE / MT_RUNTIME 等基座常量
```

**依赖方向**（re-exec 下是「运行时 + 代码组织」纪律）：

- 消费端测试**代码**只调 `client`（含 `client/fixture`、`client/fake`）的 thin API；它们只依赖各服务端模块的 leaf protocol DTO + `runtime` + 标准库 + HTTP control client + gRPC **客户端** stub。
- **【已修订】faked engine 内嵌 daemon 主进程**：faked 引擎随测试二进制编译进来（由项目 `TestMain` blank-import fake endpoint 触发 `init()` 自注册），daemon 进程内直接构造调用。原「faked 只在 cmd/faked 独立二进制、不进测试二进制」的依赖隔离目标**作废**——理由见 §4.1 已修订、`faked-integration-plan.md` §0.4。
- 容器 / `fixtured` / faked 引擎实现 + docker SDK 经 `orchestrator`（daemon-mode 代码路径）链进测试二进制（re-exec 所需）；client 模式不碰这些实现，只经 control API 触达。`mtkit.go` 不依赖 client——模式分流是 orchestrator 职责；**client-mode（re-exec 持租约）是 orchestrator 的内部子包 `orchestrator/client`**，顶层 `client/` 纯为消费端。
- 测试进程与 daemon 间只走 **control API（HTTP）+ runtime 文件**这条「网线」，不在两侧共享 Go 调用——这是隔离的真正边界。

---

## 4. 中心 daemon 职责

**做什么**：

- 容器 / SUT 生命周期（租约、双层 IdleTTL、孤儿清理）。
- 容器内一次性**全局引导**（`Init` hook：建数据库本身 / pulsar tenant+namespace / **SUT 启动必需的表与配置**；所有容器就绪后串行、失败全量回滚）。**测试用表 / topic 的按包 provision 不在这里**（见 §6）。
- **进程内托管 faked 引擎**（构造 + 开 listener + control 挂主 mux + 关停，见 §4.1 已修订）。
- **协调内核 `fixtured`**（服务端，handler 与 lease 共用 daemon 主端口）：跨进程锁、内存 registry、唯一 ID 分配（runtime 文件本身归 leaf `runtime` 包，见 §4.3）。
- **启动顺序编排**。

**不做什么**：业务表 / topic / seed 的**实际写入逻辑**不内嵌成 daemon 的 per-resource 代码——由测试进程经 client 在 `fixtured` 锁保护下直连写（见 §6）。`fixtured` 只提供「锁 + 账本 + 发号」这类资源无关的机制。

### 4.1 进程模型（**已修订**：faked 内嵌、只有 SUT 是被监督子进程）

> **修订说明**：本节原定 faked 为「daemon 监督的独立子进程」。经评估改为 **faked 内嵌 daemon 主进程**——详细论证见 `faked-integration-plan.md` §0.4。要点：daemon 是 re-exec 的唯一进程，「daemon 进程内跑 faked」已满足单实例（原「独立子进程」想解的 N-实例问题，靠 daemon 拥有即可，不必独立进程）；「独立子进程」更强的两点收益（依赖隔离、廉价 reload）经评估不抵其整层机械复杂度，故放弃。

**faked 内嵌**：daemon 进程内直接构造 faked 引擎（开各 fake gRPC listener + HTTP MITM 代理 listener，control 面挂 daemon 主 mux），生命周期随 daemon。endpoint 由项目 `TestMain` blank-import 经 `init()` 自注册进引擎 registry（daemon == 测试二进制，故天然可见）。**不 build、不 fork、无独立 faked 配置 / runtime 文件 / 发现链**。本期**不做 reload**：改 endpoint 后重跑 `go test`（重编测试二进制）即生效。

**SUT 仍是被监督子进程**：SUT 是被测的生产二进制，必须独立进程。把 `testcontainerd` 现有的「单 SUT 托管」泛化为 **`ManagedProcess` 引擎**（`proc` 包：`Build / Start / Ready / Stop` + IdleTTL），SUT 是其实例；就绪靠 TCP 探活、有自己的短 IdleTTL、按需重启。`ManagedProcess` 通用化本应在 P2，本期为 SUT 提前抽出。

**实现取向**：`proc` 薄薄一层、建在 `os/exec` 上、泛化现有 SUT 监督（已含 build / start / 探活 / grace / IdleTTL），不引入重型监督库。

> **平台注意点**（针对 SUT 子进程）：跨平台进程终止有坑，须封进 `proc`。① Windows 下 `Process.Signal(os.Interrupt)` 常无效，须用 `Kill`；② SUT 若派生孙进程（如先 `go build` 再 fork），只杀父进程会留僵尸，需平台特定地杀**进程组 / 进程树**（Unix `SysProcAttr.Setpgid` + 杀进程组；Windows `CREATE_NEW_PROCESS_GROUP` + Job Object）。faked 内嵌，无此问题。

### 4.2 启动顺序（合并的收益：跨进程编排坍缩为内部顺序）

现状是「`run.go` 先 fork faked → 等握手 → 再把 SUT 交给 testcontainerd」的跨进程舞蹈。合并进一个 daemon 后变成内部顺序：

```text
容器就绪 → 全局引导 Init
→ 进程内启动 faked 引擎（开 listener + control 挂主 mux）
→ 注入 SUT env（容器地址 + faked 地址）→ 起 SUT（最后）
→ 服务测试租约 → 逆序回收（SUT idle → daemon idle → 停 faked + 容器 → 退出）
```

（faked 内嵌，「启动」即进程内调用、无 build/fork；故无独立 faked idle，随 daemon 退出而释放。）

**SUT 最后启动**，确保其依赖的容器、全局引导数据、faked 地址都已就位，避免启动失败。测试用表 / topic 的**按包 provision 在各包 `TestMain` 的 client 侧完成**（SUT 起来之后、该包发请求之前），不在这条 daemon 顺序里——前提是 SUT 懒访问这些表（本项目 SUT 不做 boot 期 schema 校验）；SUT boot 真正必需的数据归 §4 全局引导。

### 4.3 协调内核 `fixtured`（资源无关，服务端；handler 与 lease 共用 daemon 主端口）

经 control API 暴露给 client：

```go
WithLock(ctx, key, fn)              // 跨进程互斥临界区
GetApplied() / MarkApplied(key)     // 已应用资源账本（可选附 checksum）
AllocIDBlock(kind, size) -> [start, end)   // 唯一 ID 区块分配（PrimaryScope 等）
```

- **registry / 锁 = daemon 内存**：daemon 是单进程，天然串行化，用内存 map + mutex 即可。**不用文件锁 / 文件 registry / generation**——这套是无 daemon 协调模型（框架文档 §5.4）才需要的，被中心 daemon 取代。registry 生命周期 == daemon == 容器（三者同生死）：新 daemon = 新容器 = 空 registry → 第一个包重新 provision、后续包去重。**不会出现「账本陈旧于容器」，故无需 generation。** 容器跑一半挂掉 → 测试失败 → 看日志重跑即可（小概率、不为它造机制）。
- **ID 分配**：单调 high-water mark，按区块发给各进程、本地用完再续；daemon 串行保证跨进程唯一。它编号的数据也随 daemon/容器同生死，故内存计数即可、无碰撞。字段的**格式塑形**在 `client/fixture` / 项目侧做，唯一性只在此处保证。
- **runtime 文件**：作为测试进程的统一发现入口与就绪信号；含容器地址、faked 地址 / 代理 / CA、daemon control 地址（lease、`fixtured`、**faked control** 共用同一端口）。**物理读写归 leaf `runtime` 包**，各服务端模块只往 `RuntimeInfo` 追加字段。faked 内嵌后**无独立 runtime 文件**——其地址由 daemon 直接写入 `mtkit.runtime.json` 的 `faked` 段（见 `faked-integration-plan.md` §3）。
- checksum 仅作可选的「复用期间 schema 陈旧」廉价告警，本期不做真实性校验（见 §11）。

---

## 5. 消费面 API

- 对业务方暴露**一个 test runtime 门面**（`client` 合一），内部合并：`client/fake`（注册 rule）+ `client/fixture`（内部调 `fixtured`：锁 / registry / ID）。
- 项目侧一个薄 `bootstrap` 包：配 `mtkit`（容器集 + SUT boot plan）、装配 daemon-mode 的 faked 监督 + `fixtured`，暴露 `Run(m)`。各测试包 `TestMain` 单行调 `bootstrap.Run(m)`。
- client 模式测试体：`scope.New` → seeder（**直连写数据**，地址来自 runtime 文件，跨进程协调来自 `client/fixture`）→ request → observe。
- 按包 provision / 写数据的数据流（测试进程侧）：

```text
读地址（runtime 文件）→ client/fixture 锁（fixtured.WithLock）→ 查 registry → 缺则直连写（DDL / topic / seed）→ MarkApplied → 放锁
```

---

## 6. schema provisioning（**已定：按包初始化**）

两类资源分两层：

- **全局引导**（pre-SUT，放 daemon `Init`）：建数据库本身、pulsar tenant+namespace、**SUT 启动必需的表与配置**。这些全场一致、必须在 SUT 之前就位（见 §4.2）。容器集同属此层 = SUT 启动地板（项目级，非包级变量）。「Init 由先启动者决定」因只做全场一致的事而不构成问题。
- **测试用表 / topic**（post-SUT，client 侧按包）：每个包在自己的 `TestMain`（任何测试函数之前）经 `fixtured`（锁 + registry + 幂等 DDL）**只创建本包声明的那一份**。与 `api-test-framework-design.md` §5.1「只初始化该包声明的最小集合」一致；声明层面与 provision 层面都按包。

**「建表再跑测试进程」没有问题**：① 它在 `TestMain`、测试函数之前，是启动期 provision，不是 `api-test-framework-design.md` §11 禁的「用例里惰性建表」；② `fixtured` 锁 + registry 去重 + DDL `IF NOT EXISTS` 幂等，并发不撞；③ seeder 本就直连 DB 写数据，测试进程早有 DB driver，不新增重依赖；④ 锁只在 `TestMain` 拿一次，测试运行 lock-free；⑤ DDL 失败只挂该包，比放 daemon `Init` 全量回滚更隔离。

---

## 7. 启动编排与工作流

SUT 生命周期**可插拔**：**daemon 托管** 或 **外部托管**（用户手动起）。据此分两种工作流：

| 工作流        | daemon                    | SUT                            | 触发与回收                                                                                     |
| ------------- | ------------------------- | ------------------------------ | ---------------------------------------------------------------------------------------------- |
| **CI 全自动** | 懒触发                    | daemon 托管                    | `go test ./...` 跑完即 IdleTTL 回收                                                            |
| **本地手动**  | 显式 serve 入口拉起并保持 | **外部**（用户挂调试器手动起） | 用户读 runtime 文件配置 SUT；另跑 `go test`（client 连已起 daemon）；显式 down 或 IdleTTL 回收 |

**手动机制**：re-exec daemon 由测试进程懒触发、跑完即回收，没有「拉起晾着」状态。故提供一个**显式 serve-and-hold 入口**——`cmd/testenv`（import 同一 `bootstrap`，调一个**非 re-exec 的 `mtkit.Serve()`**）拉起 容器 + faked + fixtured、跑全局引导、SUT 配 disabled、写 runtime 文件、阻塞到 SIGINT。配 `make test-env-up/down`。约束：**手动流必须先 `test-env-up` 再 `go test`**，否则 `go test` 会用默认配置（SUT 托管）summon 出一个非预期 daemon。若不愿给 `mtkit` 加 `Serve()`，退路是用一个 `-timeout 0` 的 hold-test 阻塞保持（偏 hack）。外部 SUT 时 daemon 不探活它，就绪由用户 / 测试自行确认。

---

## 8. faked 部署形态（**已修订**：内嵌）

faked **内嵌 daemon 主进程**（理由见 §4.1 已修订、`faked-integration-plan.md` §0.4）：

| 形态           | 进程                          | listener           | 用途                                        | 本次 |
| -------------- | ----------------------------- | ------------------ | ------------------------------------------- | ---- |
| **内嵌 daemon** | daemon 进程内构造 faked 引擎 | 真端口（每 fake gRPC 一个）/ MITM 代理；control 挂 daemon 主 mux | **生产集成**（单例 = daemon 单例，独立进程 SUT 经真端口可达） | 落地 |

> 数据面（fake gRPC 服务、HTTP MITM 代理）仍开真 TCP listener 供独立进程 SUT 连接；只有控制面（rule 注册）并入 daemon 主 mux。原「独立 `cmd/faked` 二进制」及其手动启动形态**取消**——隔离调试可写聚焦测试，日后确需再补薄 main。

---

## 9. 通用机制 vs 项目专属内容

总模块只给**机制**，项目给**内容**，全部经注入 / 回调，库不硬编码任何项目内容：

| 总模块提供（通用机制）                                                   | 项目提供（专属内容）                                                              |
| ------------------------------------------------------------------------ | --------------------------------------------------------------------------------- |
| 容器编排引擎、`ManagedProcess` / SUT boot plan **接口**                  | 要哪些容器、SUT 怎么起（启动地板）                                                |
| faked engine + client + endpoint **注册机制**                            | 具体 fake endpoint（`forum` / `googlepay`…，留项目侧、由测试二进制 `import _` 自注册） |
| `fixtured` 内核 + `Requirement` / `Resource` / `Registry` **接口**、ID 分配器 | 具体 schema 声明、seeder / recipe、CaseScope 字段提供者                           |

注入点：`mtkit.New(cfg, registerContainers)` 的容器注册回调、`SUTBootPlan` 接口、schema 声明函数、**项目 `TestMain` bootstrap 的 `import _` fake endpoint**（触发 `init()` 自注册进内嵌 faked 引擎，见 §4.1 已修订）。

---

## 10. 与现有代码 / 文档的关系 + 演进阶段

**现状**（已核实）：

- `testcontainerd`（`mtkit` 的前身）通过 re-exec daemon 管容器 + SUT。
- faked 当前由 `test/infra/run.go` 在**测试进程侧 fork**（待改为 **daemon 进程内内嵌**，见 §4.1 已修订）；fake↔SUT 的 env 注入当前**关闭**（SUT 尚未消费 fake 地址）。
- `fixtured`、`client/fixture`、`sut` 拆分**尚未构建**。
- 项目**无 schema 真相源**、SUT 不做 migration（本任务不补，见 §11）。

**演进阶段**：

1. **P1**：faked 引擎迁入 mtkit、从「测试进程 fork」改为「**daemon 进程内内嵌**」（§4.1 已修订；**不做 reload**）；启动顺序坍缩进 daemon；SUT env 注入 faked 地址（待 SUT 消费侧就绪）。详见 `faked-integration-plan.md`。
2. **P2**：`fixtured` 协调内核（内存锁 + registry + ID 分配）+ `client/fixture` 内的协调客户端；`sut` 从 `container` 拆出、泛化 `ManagedProcess`（连同进程控制抽出 `proc/`）。
3. **P3**：`client/fixture`（CaseScope 原语 + `Requirement` / `Resource` / `Registry` 接口）——详见后续 fixture 文档。
4. **P-later**：手动 serve 入口；component 内存包。

**迁移而非原地改名**：`testcontainerd` 在用、API 改动大，故另起 `mtkit` 新包、把其能力迁入（段1，见 `mtkit-migration-plan.md`），迁移期两者并存，段2/3 成熟后 testcontainerd 退役。

---

## 11. 未决事项与风险

- **已定**：re-exec daemon（§2）；按包 provision（§6）；**faked 内嵌 daemon 主进程、不独立子进程、不做 reload**（§4.1 已修订，论证见 `faked-integration-plan.md` §0.4）；registry / 锁 / ID 在 daemon 内存、无 generation / 文件 registry（§4.3）。
- **待核实**：`testcontainerd` daemon 内部钩子能否承接「SUT 子进程监督（`proc`）+ 进程内 faked 引擎托管 + 非 re-exec 的 `Serve()`」——SUT 托管是现成范本、照其泛化（薄层建在 `os/exec` 上、不引入第三方监督库，见 §4.1）。SUT 子进程的跨平台进程终止须按 §4.1 平台注意点处理（faked 内嵌无此问题）。
- **reload**：本期**不做**。改 fake endpoint 后重跑 `go test`（重编测试二进制）即生效；`test-env-up` 长活环境改 endpoint 需重启测试环境（取舍见 `faked-integration-plan.md` §0.4）。
- **合并的固有代价**：单点故障（daemon 挂则整轮挂，重跑即可）、范围膨胀、跨项目版本耦合、测试二进制变大（faked 内嵌，re-exec 早已认账瘦二进制不可得）。缓解：内部包边界清晰、provisioning 挡在 daemon 外、**消费面 API 小而稳**。
- **名字**：已定为 `mtkit` / `mtkitd`（mt = mcharvvvy test）。
- **schema 真相源**：本任务**不做**真实性校验，由开发者手工保证；自动检测（dump 签入 + CI diff）留待以后。
