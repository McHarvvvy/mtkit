# mtkit 迁移实施方案（段1：testcontainerd → mtkit）

本文是**段1 的具体实施方案**：把现有 `testcontainerd` 的能力迁移进新包 `mtkit`，并按目标架构落成分层结构。读者：实现者与方案评审者。设计依据：`test-harness-design.md`（doc A）、`fixture-design.md`（doc B）、`fixture-impl-plan.md`。

定位：本文只覆盖**段1（testcontainerd 功能迁移）**。faked 接入（段2）、fixture 协调原语 + 客户端 fixture API（段3）只在布局里**预留占位**，不在本文展开。落地顺序依据 `fixture-impl-plan.md` §4。

---

## 0. 目标与意义

### 0.1 核心意义

不是「把 testcontainerd 的 `daemon` 包切碎重组」，而是：

> **把整个 testcontainerd 作为「容器管理能力」整体下沉为 mtkit 的服务端子模块；mtkit 在其之上新立 `orchestrator/` 编排层，与 sut / faked / fixtured 等同级，由一个 daemon 承载；消费端代码统一收进 `client/`。**

关键认识：

1. **容器管理 ≠ 旧 `container/` 包**：要纳入散在 `daemon/` 里的容器编排（`lifecycle.go` infra 起停、`cleanup.go` 孤儿清理、Init 引导），整体归 `container/`。
2. **`orchestrator/` 是相对 testcontainerd 新增的最高层**，承接 doc A §4.2「跨进程编排坍缩为内部顺序」，编排 container + sut（+ 段2 faked + 段3 fixtured）。
3. **`fixturebase` 不是独立子模块，是客户端代码**：CaseScope / 生成器 / Requirement / Resource 都是测试进程里调用的方法 → 归 `client/fixture`；faked 的客户端调用归 `client/fake`（与 doc A §3「client 与 fixturebase 都是消费端 import」、§5「合一门面合并 faked client + coord client」吻合）。
4. **`fixtured`（原 coord）是服务端、不是独立服务**：它是 daemon 里「配合 fixture 的 HTTP 协调端点」（锁 / 区段 / 账本），**不独立监听、不独立端口**，handler 与 container 的 lease 接口**共用 daemon 主进程同一个 listener**。

### 0.2 为什么另起 mtkit 而非原地改 testcontainerd

testcontainerd 在用、API 改动大，原地改会破坏现有测试。新包没有「不能破坏现有 API」的包袱，正好按目标布局立骨架。这是对 doc A §2/§10「提升 testcontainerd + 择机改名」路线的调整——改为「另起 mtkit、迁移」。

`mtkit` 是单独的 git repo，module path 为 `github.com/McHarvvvy/mtkit`。开发模式保持现有 `testcontainerd` 的方式：在 `mssiot_user` 工作区下放一个本地 checkout（建议路径 `pkg/mtkit`），业务仓通过 `replace github.com/McHarvvvy/mtkit => ./pkg/mtkit` 指向它；该目录本身归 `mtkit` repo 管理，不纳入 `mssiot_user` 主仓提交。**该决策需回写 doc A §2/§10 与 `fixture-impl-plan.md` §1。**

### 0.3 段1 不变量

> **段1 只做「迁移 + 按目标布局切骨架」，行为不变、容器+SUT 能力等价、监督逻辑不泛化（ManagedProcess 留段2）。**

---

## 1. testcontainerd 现状盘点

现有结构与职责（已逐文件核实）：

| 包 / 文件 | 职责 |
|---|---|
| `testcontainerd.go` | 顶层门面 `Config`/`New`/`Run`；`runDaemonMode`/`runClientMode` 模式分流 |
| `container/bundle.go`、`options.go` | 容器生命周期（并发启动 ≤4、Init、回滚、逆序停）+ `ContainerRegistration` |
| `daemon/server.go` | **daemon 基座**：HTTP control（Acquire/Heartbeat/Release/State）+ `withAuth` + listener + 写 runtime + `loopReaper`（双层 IdleTTL）+ shutdown |
| `daemon/lease_store.go` | 租约存储（纯内存 + mutex） |
| `daemon/lifecycle.go` | 编排：infra 起停（→ Bundle）、sut 起停（→ sutManager） |
| `daemon/sut_manager.go` | **SUT 监督引擎**：`SUTBootPlan` 接口、build+start+TCP 探活+grace+快照+重启计数 |
| `daemon/cleanup.go` | docker 孤儿容器清理 |
| `daemon/process_control_{unix,windows}.go` | 跨平台进程树终止（Unix Setpgid+负 pgid；Windows JobObject+taskkill） |
| `client/client.go`、`autostart.go`、`heartbeat.go` | thin client + re-exec 自启（文件锁防并发、Windows runner 复制） |
| `protocol/*` | control API 的 DTO / 路径 / header / 错误码 |
| `constant/*` | `TCD_MODE`/`TCD_RUNTIME` 等基座常量 |
| `tcdruntime/*` | runtime 文件 Read/Wait/Write/Remove、默认路径、日志落盘 |

**核心观察**：`daemon/` 包把**三股缠在一起**——容器编排（lifecycle infra + cleanup + bundle）、SUT 监督（sut_manager + process_control）、daemon 基座（server control + lease_store + reaper）。段1 的本质就是解开三股、归到正确层。

---

## 2. 目标结构（按 客户端 / 服务端 / plumbing 分层）

`mtkit` 为 **`github.com/McHarvvvy/mtkit` 独立 git repo / 独立 Go module**。开发期保持当前 `testcontainerd` 模式：本地 checkout 放在 `mssiot_user/pkg/mtkit`，业务仓用 `replace` 指向该路径。

```text
mtkit/
├── mtkit.go              # 门面:Config/New(注册项校验复用 container.ValidateRegistrations)+ 极薄 Run 转发(→ orchestrator)
# ── 客户端区(测试进程 import,thin;面向测试作者的消费端 API)──────
├── client/
│   ├── fixture/          # 〔段3〕原 fixturebase:CaseScope/生成器/Requirement/Resource/Registry/provision/persist
│   └── fake/             # 〔段2〕原 faked client:rule 注册链式 builder
#   注:段1 顶层 client 仅占位;合一门面(段2/3)合并 fixture+fake,不含 lease(lease 是框架内部机制)
# ── 服务端区(daemon 进程内)──────────────────────────────
├── orchestrator/         # daemon 编排:模式分流、启动顺序、reaper、lease store、子进程监督;持 daemon 主 HTTP mux
│   ├── client/           # client-mode 实现(re-exec:发现/自启 daemon、持租约、心跳);仅供分流,非消费端
│   └── protocol/         # lease 控制面协议 DTO(Acquire/HB/Release/State);server 与 client-mode 共用
├── container/            # 容器管理(bundle/options/cleanup/infra 起停);lease handler 在 orchestrator
├── sut/                  # SUT 监督引擎 + SUTBootPlan(段1 临时含 process_control)
├── fixtured/             # 〔段3〕fixture 协调内核(WithLock/registry/AllocIDBlock)+ handler(挂 daemon 主端口)+ fixtured/protocol
├── faked/                # 〔段2〕fake engine + 服务端,编 cmd/faked 独立子进程,自有端口
# ── plumbing(leaf 共享)──────────────────────────────
├── runtime/              # runtime 文件 Read/Wait/Write + RuntimeInfo(发现 schema)+ 日志落盘(原 tcdruntime)
└── constant/             # MT_MODE/MT_RUNTIME/LeaseTTL/ProtocolTCP 等基座通用常量(leaf;不含项目领域常量)
```

### 2.1 对外 HTTP API 分散各服务端模块

每个服务端模块**自带「control handler + 它自己的 leaf 协议子包」**，不设顶层大 protocol 包：

| API | handler 归属 | listener | 协议 entity |
|---|---|---|---|
| lease 管理（Acquire/HB/Release/State） | orchestrator（reaper / lease store / handler / client-mode 全在此） | daemon 主进程 | `orchestrator/protocol` |
| 区段申请 / 锁 / 账本（AllocIDBlock·WithLock·MarkApplied） | fixtured | **daemon 主进程，与 lease 共用同一 listener** | `fixtured/protocol` |
| fake rule 注册 | faked | **faked 子进程自有端口**（独立子进程，理应如此） | `faked/protocol`（现状已有） |

唯一全局共享的 `RuntimeInfo`（发现文件 schema）归 `runtime` 包，不归任何 protocol 子包。**lease 协议归 `orchestrator/protocol`**——它的 handler 与 client-mode 都在 orchestrator 内部，不再寄居 container（详见 §4）。段3 fixtured 经 daemon 主 mux 的注册接缝（与 `registerLeaseRoutes` 同款）把协调路由挂到同一端口。测试作者侧的合一门面（段2/3）合并 `client/fixture`+`client/fake`，**不含 lease**——lease 是框架在 `Run(m)` 内部自动管的。

### 2.2 命名

- 客户端（顶层 `client/`，面向测试作者）：`client/fixture`（fixture API）、`client/fake`（fake rule 客户端）；合一门面段2/3 出现，**不含 lease**。
- 服务端内部：`orchestrator/client`（re-exec client-mode：发现/自启 daemon、持租约、心跳，仅供分流）、`orchestrator/protocol`（lease 协议）。与顶层 `client` 性质完全不同——一个是 daemon 编排的内部实现，一个是消费端 API。
- 服务端 fixture 协调内核：`fixtured`（`d` = daemon 驻留、非独立服务；与客户端 `client/fixture` 形成 client/server 对称，避免同名混淆）。

---

## 3. 迁移映射（testcontainerd → mtkit，段1 实际搬运）

| testcontainerd 现有 | → mtkit |
|---|---|
| `testcontainerd.go` Config/New | `mtkit.go`（门面） |
| `testcontainerd.go` Run/runDaemonMode/runClientMode（模式分流） | `orchestrator/`（分流逻辑，import `orchestrator/client`）+ `mtkit.go` 薄转发 |
| `container/bundle.go`、`options.go`、`daemon/cleanup.go`、`daemon/lifecycle.go` infra 部分 | `container/` |
| `daemon/server.go`（HTTP control/withAuth/reaper/shutdown/写 runtime）、`daemon/lease_store.go` | `orchestrator/`（lease handler 与 fixtured handler 共用主端口 mux） |
| `daemon/lifecycle.go` sut 部分 | 编排在 `orchestrator/`，执行在 `sut/` |
| `daemon/sut_manager.go` + `SUTBootPlan` + `StartSUTInput` | `sut/`（不泛化） |
| `daemon/process_control_{unix,windows}.go` | `sut/`（段1 临时）→ 段2 抽 `proc/` |
| `client/client.go`、`autostart.go`、`heartbeat.go` | **`orchestrator/client/`**（re-exec client-mode 内部实现，**非顶层 client**；autostart 注入 `MT_MODE=daemon`，runner 前缀 `mt_`）+ `RunUnderLease` 内聚生命周期 |
| `protocol/*` | **`orchestrator/protocol`**（lease DTO）+ `runtime`（RuntimeInfo） |
| `constant/*` | `constant/`（`MT_*`/LeaseTTL/ProtocolTCP，**不兼容 `TCD_*`、不双读；剔除项目领域容器常量**） |
| `tcdruntime/*` | `runtime/` |

迁移后 `daemon/` 包消失，内容三分到 container/sut/orchestrator。

---

## 4. 依赖方向不变式（评审主线）

```text
mtkit.go ──────────→ orchestrator, container(仅注册项校验), sut, runtime   （门面合成根）
orchestrator ──────→ orchestrator/client, orchestrator/protocol, container, sut, runtime, constant  (段2:+faked 段3:+fixtured)
orchestrator/client → orchestrator/protocol, runtime, constant   （re-exec client-mode 内部）
container ─────────→ (testcontainers/docker SDK)
sut ───────────────→ runtime, (os/exec, 平台 syscall)
顶层 client/{fixture,fake} → (段2/3) fixtured/protocol, faked/protocol, runtime   〔消费端,不被服务端 import〕
orchestrator/protocol, fixtured/protocol, faked/protocol, runtime, constant ─→ 标准库   〔leaf〕
```

**铁律**：`container`/`sut`/`fixtured` 不得 import `orchestrator`；**顶层 `client`（消费端）不被任何服务端 import**，只走各 leaf `protocol`+`runtime`+`constant`；**client-mode 是 orchestrator 的内部子包 `orchestrator/client`**，故 orchestrator 不依赖外部 client 包（消除「daemon 侧 import 消费端」坏味道）。`mtkit.go` 是门面合成根、不依赖顶层 client。**该不变式由 `test/arch_test.go` 架构守护测试主动回归校验（评审 C2）。**

---

## 5. 段1 范围

**做**：建 mtkit module；平移 runtime/constant；解开 daemon 三股 → container/sut/orchestrator；orchestrator 串启动顺序（容器→Init→SUT）等价现状 `handleAcquire` 的 `ensureInfra→ensureSUT`；client-mode 内聚到 `orchestrator/client`、lease 协议归 `orchestrator/protocol`；预留顶层 `client/fixture`、`client/fake`、`fixtured/`、`faked/` 空占位；架构守护测试（`test/arch_test.go`）固化依赖方向。

**不做**：监督泛化 ManagedProcess（段2）；抽 `proc/`（段2）；faked 接入、fixtured 协调原语、client/fixture 实现（段2/3）；不动 `mssiot_user/test/infra`；`Serve()` 仅 spike 验证、不落地。

> **进程控制 / 监督的临时位置**：进程控制（杀进程树）+ 监督生命周期（build/start/ready/stop）是**比 sut 更底层的通用 concern**，faked（段2）也要复用。段1 作为权宜**临时寄居 `sut/`**（sut 是唯一使用者、不泛化）；段2 抽成独立 `proc/`（进程控制 + ManagedProcess）供 sut + faked 共用。

---

## 6. 落地步骤（自底向上，每步可编译）

1. **spike**（§8）先行。
2. mtkit module 骨架 + 空占位目录。
3. 平移 leaf：`runtime` → `constant`。
4. 平移 `container`（bundle+options+cleanup+infra 能力 + container/protocol）。
5. 平移 `sut`（sut_manager + process_control + SUTBootPlan）。
6. 建 `orchestrator`（server 基座 + lease_store + reaper + 模式分流 + 启动顺序），接 container/sut/client。
7. 平移 `client`（lease 客户端 + autostart re-exec，注入 `MT_MODE`）。
8. 写 `mtkit.go` 门面。
9. 搬 testcontainerd `examples/` + `test/integration_plan_*_test.go` 进 mtkit，跑绿。

---

## 7. 验收

把 testcontainerd 的 `test/integration_plan_*_test.go` + `examples/` 搬进 mtkit 跑绿即段1 通过。**mssiot_user 完全不参与**；mtkit 与 testcontainerd 作为两个独立 repo 的本地 checkout 在 `pkg/` 下并存，段2/3 成熟后再整体切换、testcontainerd 退役。

---

## 8. spike（开工前 fail-fast）

| # | 验什么 | 结论 / 标准 |
|---|---|---|
| A | fixtured 与 lease 是否共用 daemon 主端口 | **已拍板：共用**（不独立 listener、不独立端口）。无需再验，直接照此实现 |
| B（重点） | daemon fork **非 SUT 子进程**、等 ready、关停连**孙进程**一起可靠杀 | Windows 下起「故意再派生孙进程」的程序，daemon 关停后确认父+孙都没了（JobObject 杀整棵树）。段2 faked 地基 |
| C | 非 re-exec 常驻 `Serve()` 可行性 | 不经 re-exec 进 daemon 路径、SUT disabled、阻塞到 SIGINT；暴露 reaper 在 active=0 时按 IdleTTL 退出 → `Serve()` 须能禁用 idle reaper。仅验证可行性，不落地 |

B 是重点。spike 用最小原型，验完即弃，不并入正式迁移。

---

## 9. 风险

- **spike B 不过** → 段2 faked「daemon 监督独立子进程」假设动摇，故排最前。
- **re-exec runner**：autostart 复制当前测试二进制进 daemon 模式，`MT_MODE=daemon` 须正确进 orchestrator daemon 分支；Windows 文件锁规避逻辑原样保留。
- **并存期**：mtkit 与 testcontainerd 在同一工作区下以两个独立 repo checkout 并存，注意 runtime 文件 / 端口 / 容器名隔离，避免本地手动跑时互相 summon。
- **lease 归属**：reaper + lease store 放 orchestrator（daemon 生命周期），handler 与 fixtured 共用主端口；「container 的 lease 接口」指端口共用与服务对象，非 lease 实现归 container 包。
- **doc 回写**：mtkit 命名 + 「迁移而非提升」需回写 doc A §2/§10、`fixture-impl-plan.md` §1（本文 §0.2）。
