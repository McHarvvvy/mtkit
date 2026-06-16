# 模块设计：编排与生命周期

本文覆盖 mtkit 的「编排与生命周期」一线，由四个相互配合的包组成：

| 包 | 角色 |
| --- | --- |
| `orchestrator` | daemon/client 模式分流；daemon 侧租约面与就绪编排 |
| `orchestrator/client` | client 侧：发现 / 自启 daemon、持租约、心跳 |
| `proc` | 通用进程生命周期原语（构建 / 启动 / 探活 / 停止） |
| `sut` | 被测服务的语义边界，建在 `proc` 之上 |
| `runtime` | `mtkit.runtime.json` 读写、daemon 发现、产物路径布局 |

宏观的模块拥有关系、跨模块启停顺序与并发分层见 [architecture.md §4](../architecture.md#4-模块集成与生命周期协调)。本文聚焦各子区的**核心设计、对外行为与注意事项**，不重复架构文档的全局编排。

> ⚠️ **命名陷阱**：本线的 `orchestrator/client` 与面向测试作者的顶层 `client/`（`client/fixture`、`client/fake`）是**两个不同的 client**。前者是 re-exec 进程管理的内部实现，后者是用例里直接调用的消费端 API。本文只讲前者。

---

## 1. 运行模式分流

`orchestrator.Run`（`run.go`）按环境变量 `MTKIT_RUN_MODE` 分流，是整个框架唯一的入口岔路：

- 未设 → **client 模式**：`runClientMode` → `client.RunUnderLease`。
- `=daemon` → **daemon 模式**：`runDaemonMode` → `Daemon.New(...).Start(ctx)`。

分流层刻意保持极薄：client 模式的完整生命周期内聚在 `orchestrator/client`，daemon 模式的编排内聚在 `orchestrator` 自身。re-exec 自身成 daemon 的原因见 [architecture.md §3](../architecture.md#3-运行模式与-re-exec-模型)。

`runClientMode` 还负责一件事：若用例注册了 `WithSchema` 资源，就用 `provisionRunnable` 包住 `m`，在持租约后、`m.Run()` 前执行一次幂等 provision（详见 [fixture.md](fixture.md)）。

---

## 2. client 侧：发现、自启、租约

### 2.1 daemon 发现与自启（`client.go: connectOrStart` + `autostart.go`）

`client.New` 不直接假设 daemon 存在，而是走「发现优先、自启兜底」：

```
connectOrStart（最多 2 轮）:
  读 runtime.json ─► TCP 探活(1.2s) ─┬─ 活 → 复用 addr/token，返回
                                     └─ 不活 → 删陈旧 runtime
  autoStartDaemon ─► Wait runtime 就绪(12s) ─► 再探活 ─► 复用
```

**并发自启单飞**是这里的关键设计。并发 `go test ./...` 会有多个测试进程同时发现 daemon 缺失，靠 `runtime 文件 + 旁路 `<runtime>.start.lock`` 协调：

- 用 `O_CREATE|O_EXCL` 抢锁，抢到的进程才真正 re-exec 启动 daemon，其余进程只 `Wait` runtime 就绪后复用。
- 只有「锁明显陈旧（>90s）**且** runtime 不存在」才判定为死锁并抢占——避免正常慢启动期间误删锁导致并发拉起多个 daemon。
- 自启用 `os.Executable()` 重启当前测试二进制并设 `MTKIT_RUN_MODE=daemon`、配 `-test.run=^$`（不跑任何用例）。
- **Windows 特例**：先把二进制复制到 `runner/` 再启动，否则 `go test` 临时二进制被子进程占用，父进程结束时删不掉。

### 2.2 租约与心跳（`run.go: RunUnderLease` + `heartbeat.go`）

```
New ─► Acquire ─► StartHeartbeat ─► m.Run() ─► stopHB ─► Release(exitCode)
```

- 租约固定 `LeaseTTL = 2s`，心跳 `HeartbeatInterval = 1s`（`constant/lease.go`，恒为 TTL 一半）。
- **先停心跳再 Release**：避免 Release 阻塞期间还在续租，拖延 SUT 回收。
- Release 携带 `m.Run()` 退出码，最终被 `mtkit.Run` 如实透传为进程退出码。

**注意事项**：
- 心跳失败**不**中断测试——租约过期由服务端 reaper 兜底回收（短暂网络抖动不至于杀测试）。
- client 与 daemon 的所有请求都带 `X-Mt-Token` 头；token 来自 runtime 文件，鉴权失败返回 401。

---

## 3. daemon 侧：租约存储与回收

daemon 的就绪编排（首次 Acquire 懒启动容器/faked/SUT、停止逆序、三层锁）已在 [architecture.md §4.4–§4.5](../architecture.md#44-首次-acquire跨模块就绪编排) 讲透，这里只补两个本模块独有的实现点。

### 3.1 租约存储（`lease_store.go`）

纯内存 `map[string]lease`，`sync.Mutex` 保护。设计要点：

- 租约 ID = `"lease-" + randHex(8)`，`ExpireAt = now + TTL`，每次 heartbeat 续。
- **`activeCount` 只统计、不删除**；过期清理交给 `gc` 单独做。这样高频的状态读路径不产生写放大，删除集中在 reaper tick 里一次性完成。

### 3.2 空闲回收（`loopReaper`，tick = 2s）

双层空闲计时：活跃租约为 0 **且** `acquireInFlight == 0` 时，先按 SUT IdleTTL（默认 2s）停 SUT，再按 daemon IdleTTL（门面默认 36s）整体退出并清理 runtime / 容器。`acquireInFlight`（原子计数）保证 Acquire 执行中（infra 刚起好但还没落租约）不被误回收。

---

## 4. proc：通用进程原语

`proc.Process` 把「构建 → 启动 → 探活 → 等待/停止」抽象成与 SUT 无关的通用原语，`sut.Manager` 与（潜在的）其它被管进程共用它。

### 4.1 生命周期（`process.go`）

`EnsureStarted` 是**幂等**的核心方法：

```
未启用 → 空操作
已在运行(isRunningLocked) → 直接返回（复用，不重启）
否则: PreStart(仅新启动前) ─► ensureBinary ─► Command(ctx,bin) ─► 校验 ─► Start ─► Ready 探活
```

- **构建策略**（`ensureBinary`）：给了 `BinaryPath` 直接用；否则用 `go build` 编译 `SourceDir` 到 `OutputName`；两者都空则返回空路径（交给 `Command` 自行决定）。
- **复用判定**（`isRunningLocked`）：非阻塞读 `waitCh`——进程已退出则捕获 `lastErr` 并视为未运行，否则视为存活复用。
- **重启计数**：非首次启动会 `restartCount++`，经 `Snapshot` 暴露。

### 4.2 探活策略（`ready.go`）

`ReadyFunc` 是可插拔的就绪判定，内置三种：

| 策略 | 行为 | 适用 |
| --- | --- | --- |
| `ReadyTCP(addrs)` | 轮询拨号所有地址（每个 500ms 超时，间隔 150ms），全通才就绪 | SUT（默认） |
| `ReadyProcessStable(window)` | 进程存活超过 window 即就绪 | 无监听端口的进程 |
| `ReadyFileThenTCP(path, extract)` | 先等就绪文件、解析出地址再 TCP 探活 | 端口动态分配、写文件公布的进程 |

任一策略在探活期间若发现进程提前退出（`waitCh` 有信号），立即报错而非空等超时。`ReadyTCP` 还会把 `:port` 规整为 `127.0.0.1:port`。

### 4.3 停止（`Stop`）

`SignalGraceful` 发优雅停止信号（Unix `SIGTERM`；Windows `CTRL_BREAK`），然后在 `GracePeriod`（默认 5s）内等 `waitCh`；超时或 ctx 取消则 `TerminateProcess` 强杀进程树。

> **重要**：若 `SignalGraceful` **发不出去**（典型：Windows 无 console），不再空等 `GracePeriod`（进程根本收不到信号、不会优雅退出），直接强杀。这消除了 Windows 上每次停 SUT 都白等满 grace 的死等。

---

## 5. sut：被测服务的语义边界

`sut.Manager`（`manager.go`）是建在 `proc.Process` 之上的薄封装，把「SUT」这个语义固定下来：它是 orchestrator 实际消费的对象，也是用例可依赖的稳定边界（`proc` 内部重构不影响它）。

### 5.1 对外行为

- `NewManager(cfg, plan, input)` 把 `SUTBootPlan` 适配成 `proc.Spec`：`Ready` 固定用 `ReadyTCP(plan.ProbeAddrs())`，构建产物落 `.mtkit-build/<project>`，日志落 `app.log`。
- `SetInjectEnv(env)` 由 daemon 在 SUT 启动前填入注入 env；`Command` 闭包读 `m.input`（而非构造期副本）保证拿到最新注入值——这是「容器/faked 连接信息进 SUT」的落地点。
- `EnsureStarted` / `Stop` / `Snapshot` / `IdleTTL` 都透传给底层 `proc.Process`。

### 5.2 用法主线：实现 `SUTBootPlan`

使用者只需实现两个方法：怎么启动、探哪些端口算就绪。

```go
type SUTBootPlan interface {
    Command(ctx context.Context, in StartSUTInput, binaryPath string) (*exec.Cmd, error)
    ProbeAddrs() []string
}
```

一个最小实现：

```go
func (p myPlan) Command(ctx context.Context, in mtkit.StartSUTInput, binaryPath string) (*exec.Cmd, error) {
    cmd := exec.CommandContext(ctx, binaryPath, "-serve")
    cmd.Dir = p.workDir
    // in.Env 已含 mtkit 注入的容器 + faked 连接信息；MergedEnv = os.Environ() + in.Env + extra
    cmd.Env = in.MergedEnv(map[string]string{"APP_MODE": "test"})
    return cmd, nil
}

func (p myPlan) ProbeAddrs() []string { return []string{":8080"} } // 端口可连即就绪
```

`StartSUTInput.MergedEnv(extra)` 一行合并「基础环境 + mtkit 注入 + SUT 特有」，无需自己遍历 `os.Environ()`。

---

## 6. runtime：发现与产物布局

`runtime` 包是 daemon 与 client 之间的「发现媒介」，也是所有落盘产物的路径中枢。

### 6.1 发现文件（`types.go` / `file.go`）

`mtkit.runtime.json` 即 `RuntimeInfo`：

```jsonc
{
  "addr":  "127.0.0.1:54321",   // daemon control 端口（lease + fixtured + faked control 共用）
  "token": "...",               // 鉴权令牌
  "pid":   12345,
  "started_at": "...",
  "faked": { "grpc_endpoints": {...}, "proxy_addr": "...", "ca_cert_file": "..." },
  "sut_env": { "REDIS_ADDR": "...", "HTTP_PROXY": "..." }
}
```

- `Write` 是**原子写**（先写 `.tmp` 再 `rename`），避免 client 读到半截文件。
- `Read` 要求 `addr` 与 `token` 非空，否则判为非法 runtime。
- **`faked` 与 `sut_env` 两段纯为排查/可见性服务**：client 注册 fake 规则用的是 `addr`（control 端口），SUT 拿连接信息用的是注入的环境变量，二者都**不依赖**这两段。它们只是把信息也落盘方便排查。

### 6.2 路径布局（`path.go`）

所有产物以 runtime 文件所在目录（`ArtifactDir`）为根：

| 路径 | 内容 |
| --- | --- |
| `os.TempDir()/mt/<project>/mtkit.runtime.json` | 默认 runtime 文件（按 project 隔离） |
| `~/.mtkit/<project>/mtkit.config.json` | 默认配置文件 |
| `<ArtifactDir>/daemon.log` / `app.log` | daemon / SUT 日志 |
| `<ArtifactDir>/runner/` | Windows re-exec 复制的 daemon 二进制 |
| `<ArtifactDir>/faked/` | faked 调用日志与动态 CA 产物 |

`<project>` 经 `sanitizeProject` 把 `\ / : 空格` 替换为 `_`，保证跨平台路径安全。`runtime_file` 可配为绝对路径或纯文件名（`ResolveRuntimeFile`），用于把多套环境产物分目录隔离。

---

## 7. 注意事项汇总

- **两个 `client` 包同名不同义**（§开头警告）——读代码时注意 import 路径。
- **daemon 是懒就绪的**：容器/SUT/faked 在首次 Acquire 才起，单看 `Start` 不会启动它们（见 architecture §4.3–§4.4）。
- **心跳/探活失败的容错差异**：心跳失败不杀测试（服务端兜底）；但 SUT 探活失败会让 Acquire 直接失败（用例需要就绪环境）。
- **Windows 进程管理两处特例**：re-exec 复制二进制、停止走 `CTRL_BREAK`，且信号发不出时立即强杀。
- **runtime 的 `faked`/`sut_env` 不是契约**：仅排查用，勿在代码里依赖其结构。
