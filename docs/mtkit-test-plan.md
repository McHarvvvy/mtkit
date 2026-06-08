# mtkit 功能测试计划

本文是 **mtkit 已集成功能**的功能测试重构计划，供评审同步用。定稿后再进入编码（先落骨架：空实现 + Given/When/Then 注释）。

读者：实现者与评审者。设计依据：`test-harness-design.md`（模块/daemon 视角）、`faked-integration-plan.md`（faked 内嵌 daemon 基线）、`api-test-framework-design.md`（测试作者视角）。

> 本文**只定义测什么、怎么测的用例逻辑**，不含测试代码。断言一律用业务/行为语言描述。

---

## 0. 两条硬原则（贯穿全文）

1. **测行为，不测实现**：断言只咬对外契约与可观察行为，绝不咬调用次数、内部 cache key、私有字段、内部分支、内部错误字符串等机制。业务行为不变、纯重构内部时，用例不应挂。
2. **每个重要方向至少一个真实环境 smoke**：可偏向进程内技术承载主体覆盖，但每个重要方向必须有 ≥1 个用例验证「真实环境下整条链路能跑通」。

---

## 1. 现状评估（重构起点）

| 测试 | 层级 | 状态 | 处置 |
| --- | --- | --- | --- |
| `test/arch_test.go` | 架构守护 | ✅ 活+绿 | 迁入 `test/arch/` |
| `test/integration_plan_*_test.go`（TC-01~17） | 框架 E2E | ❌ **已腐烂**（绑定旧 API，`integration_plan` tag 下编译失败） | **删除**，意图按新计划重写 |
| `faked/test/acceptance/*` | faked acceptance | ✅ 活+绿，质量高 | **整体迁入** `test/faked/`，按 §6 补缺 |
| `faked/http/ca/ca_test.go` | 单元 | ✅ 活+绿 | 保留（CA 生成单元） |

**主要偏移点**（旧 → 新）：`New(cfg,fn)` → `New(project,*Config)` + 链式 `RegisterContainers/RegisterSUT`；`Config{Global,Daemon,Client}`+`Duration` → `Config{RuntimeDir,RuntimeFile,DaemonMode,ClientMode,SUT,Faked}`+`*Seconds`；`SUTBootPlan` 6 方法 → `Command(ctx,in,binaryPath)`+`ProbeAddrs()`；SUT env 由"自行查 Docker"→"容器 `SUTEnv` 回调 + `prepareSUTEnv` 合并 + `MergedEnv` 注入"；自建 `MT_SCENARIO` re-exec → 框架内置 `MTKIT_RUN_MODE=daemon` re-exec 同一 test 二进制。

**当前覆盖盲区**：SUT 进程管理（`proc`/`sut`）零测试；CA 注入进**真 SUT** 的链路无 E2E；gRPC 真实注入链路（直连地址，区别于 HTTP 代理）无 E2E。

---

## 2. 业务事实清单（mtkit 对使用者的承诺）

- **F1** 用 `New(project,cfg).RegisterContainers(...).RegisterSUT(...).Run(m)` 后，进入 `m.Run()` 时容器已就绪、SUT 已就绪、faked 已就绪；`m` 的退出码被如实透传。
- **F2** 配置/装配非法时拒绝启动：`runtime_dir` 必填、faked CA 成对、SUT 启用需 `binary_path`/`source_dir`、容器注册非空且名唯一且 `Start` 非 nil、`RegisterContainers` 未设或 SUT 启用却未 `RegisterSUT`。
- **F3** 任一容器启动失败或 `Init` 失败 → 回滚所有已起容器，`m.Run()` 不被调用，`Run` 返回非 0。
- **F4** 多个测试进程/包连续或并发运行 → 复用同一 daemon、同一组容器，不重复拉起。
- **F5** 租约全部释放且空闲超 `IdleTTL` → daemon 自动退出，清理 runtime 文件与容器（不留孤儿）。
- **F6** 容器连接信息 + faked 连接信息（gRPC 直连地址 / `HTTP_PROXY`+`HTTPS_PROXY` / `MTKIT_CA_CERT_FILE`）被组装注入 SUT 进程；同名 key 冲突报错。
- **F7** 注册 fake 规则后，SUT 对 fake gRPC/HTTP(S) 的调用按规则返回（static/once/sequence/error），未命中 fail-loud，调用记入调用日志。

---

## 3. 测试策略

### 3.1 三档真实度（"真实依赖" ≠ "Docker/e2e"）

| 档 | 真实的是什么 | 承载层 | 需 Docker | 需跨进程 |
| --- | --- | --- | --- | --- |
| 真协议/真网络 | 真 gRPC dial、真 TLS、真 HTTP transport / 真 daemon HTTP 服务 | `faked`、`orchestrator` | 否 | 否（同进程） |
| 真 OS 进程 | 真 `os/exec`、真进程构建/探活/停止 | `proc` | 否 | 是（子进程） |
| 真容器 + 真编排 | 真 Docker 容器 + re-exec daemon + 注入真 SUT | `e2e` | 是 | 是 |

`e2e` 只装"只有那里才存在的真实依赖"，避免把所有真实测试堆进最重的层。

### 3.2 依赖处理

| 依赖 | 策略 | 理由 |
| --- | --- | --- |
| 容器 / Docker | 对编排逻辑用 **fake 容器**（实现 `testcontainers.Container`，仅记录是否被 `Terminate`）/ nil stub；仅 F4/F5/F1 全栈用**真实 Docker**（少量 e2e） | mock 内部 IO 会假绿；编排逻辑与 Docker 无关 |
| daemon | 默认**进程内构造 `orchestrator.New` + 真实 HTTP 服务**（goroutine 跑 `Start`，经真实端口打 lease API）；仅 F4 跨进程复用走真 re-exec | lease API 本质是 HTTP 服务，真协议可在进程内验证 |
| SUT 进程 | 主体打 `sut.Manager` 边界（经测试 `SUTBootPlan` 驱动最小 helper 二进制，真子进程）；e2e 用源码构建的真 SUT | 进程管理必须对真进程验证；`sut.Manager` 是 SUT 语义稳定边界 |
| 时间 / TTL | 短 TTL + 轮询容忍窗口（reaper tick 固定 2s，用例容忍 ≥ 该粒度） | 不咬实现时序 |

### 3.3 数据与隔离

- 每用例独立 `RuntimeDir = t.TempDir()` → 各自独立 daemon/runtime，天然隔离、可并行。
- faked 沿用共享 engine + `caseKey(t)` 派生唯一 `route_key`；调用日志按唯一 key 扫描归因。

### 3.4 风险地图

变更点 = 迁移后整个 facade + orchestrator + SUT 装配（近乎全新）；回归区 = 腐烂用例所暗示的契约（rollback/reuse/idle/env/探活）；横切 = daemon token 鉴权。

---

## 4. 目录重组

全部归到顶层 `test/`，按 altitude 分子目录：

```
test/
  arch/        # 现 arch_test.go 迁入（依赖方向守护）
  facade/      # New + Config 规范化/校验 + Run 装配守卫（纯函数/早返回，无 tag，秒级）
  orchestrator/# 进程内 Daemon + 真实 HTTP：lease/鉴权/回滚/env装配/reaper（fake 容器，无 Docker）
  sut/         # sut.Manager 边界 + 真子进程：SUT 构建/启动/探活/注入env/停止/复用（helper 二进制）
    internal/suthelper/   # 测试用最小 helper 二进制源码
  faked/       # 由 faked/test/acceptance 整体迁入
    acceptance/
    internal/{testendpoint,testpb}
  e2e/         # 真 Docker + re-exec smoke（build tag: e2e）
    internal/sut/         # e2e 用真 SUT helper 源码（被框架源码构建）
```

faked 迁移为机械改 import path（`faked/test/...` → `test/faked/...`），整体一起搬、保 internal 可见性，迁移后 `go test ./test/faked/...` 须绿。

---

## 5. 进程内用例清单

> 格式：**目的**（防什么退化）/ **Given** / **When** / **Then**（行为语言）。括号标注追溯的业务事实。

### 5.1 `test/facade` — 配置与装配（F2，纯函数/早返回）

- **FC-01 runtime_dir 必填**（F2）
  When 用 `RuntimeDir` 为空的 Config `New(project,cfg)` → Then 返回错误。
- **FC-02 faked CA 成对**（F2）
  When faked 启用且只填 `ca_cert_file` 或只填 `ca_key_file` → Then 报错；都填 / 都空 → 通过。
- **FC-03 SUT 启用需产物源**（F2）
  When `SUT.Enabled=true` 且 `binary_path`、`source_dir` 都空 → Then 报错；任一非空 → 通过。
- **FC-04 RuntimeFile 解析**（F2）
  When `runtime_file` 为空/纯文件名/绝对路径 → Then 分别解析为 `<dir>/mtkit.runtime.json` / 落 `RuntimeDir` 下 / 原样；`RuntimePath()` 返回该结果。
- **FC-05 project 缺省**（F2）
  When `project` 给定非空 → Then 用之；New 的 `project` 为空字符串 → 报错（New 要求 project 非空）。
- **FC-06 容器注册校验**（F2，直接测 `container.ValidateRegistrations` 单一真相）
  When 注册列表为空 / 名为空 / `Start` 为 nil / 名重复 → Then 各自报错。
- **FC-07 Run 装配守卫**（F1/F2，触发早返回，`m` 不应运行）
  - 未 `RegisterContainers` → `Run(m)` 返回 1。
  - `RegisterContainers` 产出非法注册（如重名）→ 返回 1。
  - `SUT.Enabled=true` 但未 `RegisterSUT` → 返回 1。
  Then 三种情形 `Run` 均返回 1，且传入的 `m.Run()` 从未被调用。

### 5.2 `test/orchestrator` — daemon lease 面（进程内真实 HTTP）

> 公共 setup：`orchestrator.New(cfg, regs)`；goroutine 跑 `Start(ctx)`，从 runtime 文件读 `Addr`/`Token`，经真实 HTTP + 正确 token 打 lease API。除非特别说明，SUT/faked 关闭以聚焦 lease。
>
> **fake 容器机制（零 Docker 的由来）**：框架自身从不调 testcontainers——`Bundle` 只调用使用者在 `ContainerRegistration.Start` 里提供的闭包拿到 `testcontainers.Container`，回滚/停止时调其 `Terminate`。因此进程内测试给一个**不碰 Docker 的 Start**：成功容器返回手写 `fakeContainer`（内嵌 `testcontainers.Container` 接口、只覆写 `Terminate` 记录终态；OR-09 用 `SUTEnv` 时再覆写 `Host/MappedPort`），失败容器直接返回 error。Bundle 容忍 nil 容器，全程零 Docker。

- **OR-01 acquire 串联就绪**（F1）
  When `POST acquire` → Then 返回非空 `leaseID`；`GET state` 显示 `InfraReady=true`、`Containers` 含注册名。
- **OR-02 token 鉴权**（F2/横切）
  When 不带 token / 带错误 token 调任一 lease 接口 → Then 401；带正确 token → 通过。
- **OR-03 方法约束**（HTTP 契约）
  When `acquire` 用 GET、`state` 用 POST → Then 405。
- **OR-04 请求体非法**（F2）
  When `acquire` body 非合法 JSON → Then 400。
- **OR-05 未知租约**（生命周期）
  When 对不存在的 `leaseID` 调 heartbeat / release → Then 404。
- **OR-06 释放后失效**（生命周期）
  Given 已 acquire 的 lease；When release 后再对该 lease heartbeat → Then 404。
- **OR-07 回滚-Start 失败**（F3）
  Given 两容器注册，其一 `Start` 返回 error；When acquire → Then 返回 5xx；已起的 fake 容器终态为 terminated；`state.InfraReady=false`。
- **OR-08 回滚-Init 失败**（F3）
  Given 容器都起成功，某注册项 `Init` 返回 error；When acquire → Then 返回 5xx；所有 fake 容器 terminated；`InfraReady=false`。
- **OR-09 env 装配落盘**（F6，反向读 runtime 文件这一发布产物）
  Given 容器注册带 `SUTEnv` 回调返回连接 env；When acquire 成功 → Then runtime 文件的 `SUTEnv` 段包含该容器贡献的 key/值。
- **OR-10 env 冲突报错**（F6）
  Given 两容器 `SUTEnv` 贡献同名 key（或容器与 faked 同名）；When acquire → Then 返回错误（拒绝静默覆盖）。
- **OR-11 复用幂等**（F1，避免重复启动语义退化）
  Given 已 acquire 一次；When 同 daemon 再 acquire → Then 仍返回有效 lease，环境保持就绪，`state.Containers` 与首次一致（不报"重复启动"类错误）。
- **OR-12 idle 自动退出**（F5）
  Given daemon `IdleTTL` 设短值；When acquire 后 release 并等待超过 `IdleTTL` → Then `Start` 协程返回（服务关闭），runtime 文件被删除。
- **OR-13 reaper 抑制**（F5 边界）
  Given 短 `IdleTTL`；When 持有一个 lease 并持续 heartbeat → Then 在数倍 `IdleTTL` 时间内 daemon 不退出（runtime 文件仍在、服务仍可 acquire）。

### 5.3 `test/sut` — SUT 进程管理（`sut.Manager` 边界，真子进程）

> **边界选择**：主体测试打 `sut.Manager`（SUT 语义的稳定边界、orchestrator 实际消费的对象），而非裸 `proc.Process`——这样用例同时兼测"使用者实现的 `SUTBootPlan` 被正确驱动"，且对 `proc` 内部重构有韧性。
>
> setup：用测试 `SUTBootPlan`（`Command` 返回指向 helper 二进制的 `exec.Cmd`、`cmd.Env = in.MergedEnv(extra)`；`ProbeAddrs` 按用例返回探测地址）构造 `sut.NewManager(cfg, boot, input)`。helper 二进制按 env 决定行为：监听端口 / 延迟 / 把指定 env 写入文件 / 指定退出码。

- **SM-01 稳定就绪**（F1）
  Given `ProbeAddrs` 返回空，helper 常驻；When `EnsureStarted` → Then 返回 nil，`Snapshot().Ready=true`、`PID>0`。
- **SM-02 TCP 探活门控**（F1，**SUT 探活真实生效**）
  Given helper 延迟 ~300ms 后监听探测端口，`ProbeAddrs` 指向它；When `EnsureStarted` → Then 返回后该端口确实可连（就绪即端口通，而非进程起来就返回）。
- **SM-03 启动失败**（F1 失败语义）
  Given helper 立即退出；When `EnsureStarted` → Then 返回错误，`Snapshot().Ready=false`、`LastError` 非空。
- **SM-04 注入 env 到达进程**（F6，**注入真的进了 SUT**）
  Given `SetInjectEnv({K:V})` 后启动，`Command` 经 `in.MergedEnv` 组装 `cmd.Env`，helper 读 K 写入文件；When 就绪后读该文件 → Then 内容等于 V。（这正是 daemon `prepareSUTEnv → SetInjectEnv` 的真实注入路径。）
- **SM-05 复用幂等**（F4，SUT 进程级复用）
  Given 已启动；When 再 `EnsureStarted` → Then 仍就绪、`PID` 不变（未另起进程）。防"daemon 复用时每次 acquire 重起 SUT"退化；E2E-3 只断言容器/daemon PID，不覆盖 SUT 进程复用，故此条独有。
- **SM-06 优雅停止**（F5）
  Given 已启动；When `Stop` → Then 进程在宽限期内退出，停止后该 PID 不再存活。
- **SM-07 源码构建启动**（构建真实路径）
  Given `cfg.SourceDir` 指向 helper 源码（无预置二进制）；When `EnsureStarted` → Then 框架 `go build` 出二进制并由 `Command(binaryPath)` 启动成功（helper 写出标记）。

> 维度取舍：`RestartCount`、`Enabled=false` no-op 作为 SM-05/06 子断言顺带覆盖，不单设用例。`proc` 层仅在"确有生产调用方、且 `sut.Manager` 不经过的就绪策略变体"上补测；当前 `sut.Manager` 统一走 `ReadyTCP`，故 `proc` 层暂不单独建用例（不测未使用机制）。

### 5.4 `test/faked` — 迁移 + 补缺（F7）

迁移后保留既有覆盖（static / sequence repeat-last·error / once / 跨规则隔离 / 调用日志 / 并发 / keep-alive / fail-loud / register-invalid / HTTP·HTTPS 数据面）。补三条：

- **FK-01 gRPC 独立 Error action**（F7）
  Given 注册 `GRPC().Error(code,msg)` 规则；When SUT 调用命中 → Then 返回该 gRPC status code 与消息；调用日志记 matched=true。
- **FK-02 路由不匹配归因**（F7）
  Given 注册某 method/path/host 的规则；When 用**不匹配**的 method/path/host 调用 → Then fail-loud（未命中），调用日志记 matched=false（区别于"无任何规则"）。
- **FK-03 静态 CA 模式**（F7）
  Given 用预置 `ca_cert_file`/`ca_key_file` 构造 engine（静态 CA）；When 经代理发 HTTPS 命中 → Then 用该 CA 完成 TLS 握手并返回规则响应。

---

## 6. E2E smoke 用例清单（真 Docker + re-exec）

### 6.1 统一执行模型

每个 e2e 用例**自己驱动 `kit.Run(fakeRunnable)`**：`fakeRunnable` 闭包体 = 持租约窗口内的观察点（等价 `m.Run` 位置），闭包返回后检查返回码与事后清理。daemon 与 client 每次合测（client 经 `MTKIT_RUN_MODE=daemon` re-exec 出 daemon）。

**每用例独立 config**：用各自 `t.TempDir()` 作 RuntimeDir → 各自独立 daemon；需变化的 config 旋钮（如 `IdleTTL`）经**环境变量编码**，re-exec 继承 `os.Environ()` 使 client 与 daemon 子进程读到同一份（这是旧 `MT_SCENARIO` 的简化正确版——只编码 config，不切换整套场景）。

### 6.2 用例

- **E2E-1 全栈双线 smoke**（F1/F6/F7 + 容器/SUT/CA/gRPC/HTTPS 全部真实）
  Given 真 Redis 容器 + 源码构建的真 SUT（启用探活）+ faked 启用；SUT helper 启动后暴露一个触发端口，能按要求各做一次：① 读注入的 Redis 地址 ping Redis；② 读注入的 `<env_key>` 直连地址 dial fake gRPC；③ 读注入的 `HTTP_PROXY`+`MTKIT_CA_CERT_FILE` 发 HTTPS 到 faked。
  When 在闭包内：经 runtime 文件的 control 地址注册 gRPC/HTTPS 两条 fake 规则 → 触发 SUT 执行 ①②③ → 收集 SUT 结果。
  Then SUT 报告：Redis 可达；gRPC 调用返回 fake 规则响应；HTTPS 调用经代理+注入 CA 返回 fake 规则响应；闭包返回 0 → `Run` 返回 0。
  *覆盖：容器真起、SUT 进程真管+探活门控、env 注入真到达 SUT、gRPC 直连注入链、HTTP 代理+CA 注入链、faked 双协议数据面、退出码 0。*
- **E2E-2 退出码透传**（F1）
  When `kit.Run` 的闭包 `return 1` → Then 进程/`Run` 返回 1；闭包 `return 0` → 返回 0。
- **E2E-3 复用**（F4）
  When 同一 RuntimeDir 下连续两次独立 `kit.Run`，及 N 个并发 `kit.Run` → Then 各次在闭包内查得的容器 ID 相同、daemon PID 相同（跨进程复用 + 锁单飞）。
- **E2E-4 idle 清理 + 无孤儿**（F5）
  Given 短 daemon `IdleTTL`（经 env 注入）；When `kit.Run` 返回（租约释放）后轮询（容忍窗口内）→ Then runtime 文件被删除、真 Redis 容器被移除、daemon 进程退出。

### 6.3 E2E 覆盖矩阵（核对完整性）

| 高价值真实环境要点 | 由谁覆盖 |
| --- | --- |
| 真容器经框架真实启动且可达 | E2E-1① |
| 真 SUT 进程构建/启动/探活门控真实生效 | E2E-1 + SM-02/07 |
| env 注入真的进了 SUT 进程 | E2E-1②③ + SM-04 |
| gRPC 直连注入链路 | E2E-1② |
| HTTP 代理 + CA 注入链路（含 addons CA 插件） | E2E-1③ |
| faked gRPC/HTTPS 数据面（真实环境） | E2E-1②③ |
| 退出码端到端透传（0/1） | E2E-1 / E2E-2 |
| 跨进程 daemon + 容器复用、并发单飞 | E2E-3 |
| idle 自动退出 + 容器/runtime 清理、无孤儿 | E2E-4 |

---

## 7. 为什么不写（主动放弃，避免测实现）

- 不测 reaper tick 周期、`.start.lock` 文件内容、runner 复制的临时文件名 → 机制。
- 不测调用日志的 JSON 字段顺序、acquire 内部计时 log、`MergedEnv` 的 map 遍历顺序、`maxCreateConcurrency=4` 的具体并发度 → 机制。
- 不为 orchestrator 每个 `log.Printf` 分支造用例 → 调试信息非契约。
- 不在 e2e 重复 faked 已在进程内覆盖的 static/sequence/once/隔离/keepalive → e2e 只验"被正确编排进 daemon 并注入 SUT"的 wiring。
- 回滚的"逻辑"留在 OR-07/08（fake 容器即可证明终止）；其"真 Docker 无孤儿"facet 由 E2E-4 的容器移除断言顺带兜住，不另设易 flaky 的坏镜像 e2e。

---

## 8. 编码顺序与进度

`facade` → `orchestrator` → `sut` → `faked` 迁移 → `e2e`。

| 层 | 目录 | 用例 | 状态 |
| --- | --- | --- | --- |
| arch | `test/arch/` | 依赖方向守护 | ✅ 迁入并绿 |
| facade | `test/facade/` | FC-01..07 | ✅ 实现并绿 |
| orchestrator | `test/orchestrator/` | OR-01..13 | ✅ 实现并绿（进程内真实 daemon HTTP + fake 容器） |
| sut | `test/sut/` | SM-01..07 | ✅ 实现并绿（sut.Manager 边界 + helper 二进制） |
| faked | `test/faked/` | 迁移 + FK-01..03 | ✅ 迁入并绿 |
| e2e | `test/e2e/`（build tag `e2e`） | E2E-1..4 | ✅ 实现并绿（真 redis + re-exec + 真 SUT） |

e2e 落地要点：
- 真 SUT 的 gRPC 直连线在 `test/e2e/internal/echopb` + `echoendpoint` 自带一份 echo proto/endpoint
  （独立 test 二进制、不与 faked 共链，无 proto 注册冲突），零扰动已绿的 faked 测试。
- 真 SUT（`test/e2e/internal/sut`）由框架以 `-tags mtkit_it` 源码构建，含 CA addon 模板副本；
  一发覆盖三条注入线：REDIS_ADDR ping、`E2E_ECHO_GRPC_ADDR` 直连 dial、`HTTP_PROXY`+CA 发 HTTPS。
- 统一执行模型：用例自驱 `kit.Run(fakeRunnable)`；`buildKitFromEnv` 经 `E2E_*` env 同时构造 client
  与 re-exec daemon 的 kit，保证两者一致。client HTTP 超时放宽到 180s，避免 acquire 期间现场构建 SUT 超时。

**全部 5 层 + arch 共 ~40 个用例，全绿。** 运行：非 e2e `go test ./test/... ./faked/...`；e2e `go test -tags e2e ./test/e2e/`（需本地 Docker）。
```
