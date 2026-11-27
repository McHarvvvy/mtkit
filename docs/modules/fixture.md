# 模块设计：Fixture 与唯一性

本文覆盖 mtkit 的测试数据一线，由两端组成：

| 包 | 角色 |
| --- | --- |
| `client/fixture` | 测试侧：唯一数据生成（`CaseScope`）+ 前置数据装配（`Provision` / `PersistEnv`） |
| `fixtured` | daemon 侧：全局唯一 token 发号（`Allocator`），挂在 daemon 主端口 |

两端通过 `fixtured/protocol` 通信、互不 import（见 [architecture.md §6](../architecture.md#6-模块边界与依赖方向)）。本模块解决两个独立但相关的问题：**（A）生成跨进程不碰撞的测试数据**、**（B）跨进程只装配一次前置数据**。

---

## A. 唯一数据生成

### A.1 核心设计：单一真相源 + HiLo 缓存

唯一性只在**一处**保证——daemon 侧 `fixtured.Allocator` 的一个原子计数器：

```go
func (a *Allocator) Alloc(kind string, size int64) (start, end int64) {
    newEnd := a.counter.Add(size)   // 一次 atomic FetchAdd，无锁
    return newEnd - size, newEnd    // 半开区间 [start,end)，跨调用者天然不相交
}
```

每种数据格式（UUID / Email / Phone…）都只是「token → 目标格式」的单射，于是**碰撞免于逐格式证明**：只要 token 不重复，所有派生形态就不重复。

测试侧 `client/fixture` 用 **HiLo 缓存**把绝大多数取号变成本地无锁自增（`blockSource`）：

```
CaseScope.UUID()/Email()/... → blockSource.Next()
   next < end  → 本地自增返回（热路径，0 远调）
   next >= end → POST /v1/fixture/alloc 领一个 65536 的区块（冷路径，每 65536 次一次）
```

- `Client` 是进程级单例，持一个 `blockSource`；`NewScope(t)` 派生的所有 `CaseScope` 共享它。
- 补块是一次同步 HTTP 往返（5s 超时），**全程持锁**。它每 65536 次才走一次、且是本机回环，开销可忽略——但不是「极短临界区」，补块期间会阻塞进程内所有生成。
- 补块失败**不重试**：本机/CI 都是回环地址，daemon 不可达即基建故障，直接上抛。

### A.2 Feistel 保形加密（`fpe.go`）

token 是单调递增的，直接编码会得到 `0001/0002/...` 这种可预测串。`uniqueStringFromToken` 用**保形加密（FPE）**把 token 打散成全宽随机外观，同时保持双射：

```
token →(permute：平衡 Feistel + cycle-walking 的双射)→ [0,cap) 内的值 →(base 编码)→ 恰好 n 个字符
```

- **为什么用 Feistel 而非「hash 然后 mod」**：后者是概率性碰撞；平衡 Feistel 对任意轮函数都可逆，是严格双射，碰撞行为变成**二值阈值**而非概率——token < cap 时碰撞恰为 0。
- 轮函数 `F = SHA256(固定key ‖ round ‖ R) mod split`，固定 key 使映射**确定性可复现**（同 token 在任何机器产出同一串；非确定性只来自 token 分配本身）。
- `cycle-walking`：Feistel 作用在 `[0, split²) ⊇ [0,cap)`，落到 `[cap, split²)` 的值重映射直到回到 `[0,cap)`，期望迭代 < 2 次。

### A.3 三档唯一性模型（**关键注意事项**）

唯一性强度取决于目标形态的值空间 `cap = |字母表|^n`。`permute` 是真双射、碰撞是**二值阈值**（不是概率）：`G < cap` 时严格零碰撞，`G ≥ cap` 时确定性别名。

> **判据是全局计数器 `G`，不是该形态自身的生成量。** `G` ≈ daemon 启动以来**所有形态**的累计生成次数。一个 6 位验证码（`cap=10⁶`）即使整套测试只生成过一次，只要此前全局累计已超 `10⁶`，照样确定性别名。**勿用「这个形态我只生成了几次」论证安全。**

| 档 | 条件 | 行为 | 示例 |
| --- | --- | --- | --- |
| ① 无条件唯一 | `cap ≥ 2⁶³` | `G` 受 int64 上界限制恒 < cap，严格零碰撞 | UUID、`UniqueHex(≥16)`、`UniqueString(≥11, AlphaNum)` |
| ② 实践安全 | `cap < 2⁶³` 且现实 `G ≪ cap` | 现实远撞不到，但**非无条件** | 10 位 uid（`10¹⁰≈2³³`）、email local-part |
| ③ 有概率重复 | `cap ≲` 现实可达的 `G`（窄串） | `mod` 折叠后**静默别名**，不在保证内 | 6 位码、`UniqueHex(≤5)` |

各字符集落入 ① 档的最小 `n`：

| 字符集（size） | Digit(10) | Hex(16) | Lower/Upper(26) | AlphaNum(62) |
| --- | --- | --- | --- | --- |
| 最小 n | 19 | 16 | 14 | 11 |

`UniqueString` 对合法参数是 total 函数，**不设运行期唯一性守卫、不 fail-loud**——窄形态静默别名。**要全局唯一就用 ① 档形态**；让被测程序依赖短串全局唯一本身是调用方设计缺陷。UUID/Email/Phone 内置形态都已选在 ① 档。

### A.4 生成函数总览（`scope.go`）

**消耗 token、保证唯一**（经 `CaseScope`，`t` 绑定用例）：

| 函数 | 产出 |
| --- | --- |
| `UniqueString(n, cs)` / `UniqueHex(n)` | 恰好 n 字符；`cs` ∈ `AlphaNum/Lower/Upper/Hex/Digit` |
| `UniqueInt(digits)` | `[0,10^digits)`，`digits ∈ [1,18]`（int64 上界） |
| `UUID()` / `UUIDNoDash()` | RFC 4122 v4（36 / 32 字符） |
| `Email(domain...)` / `EmailFrom(seed)` | `fixture+<载荷>@domain` / 在 seed 本地名后插 plus tag |
| `Phone(country)` | E.164 形态（支持 CN/US/GB/DE/FR/JP/IT/CA） |

**不消耗 token、可重复**（包级函数，无唯一性）：`SampleCountry/Currency/TimeZone/Language`（从标准枚举随机取）、`Sha256Hex(s)`（确定性摘要）、`EpochSeconds/EpochMillis`。

### A.5 用法主线

```go
var fx *fixture.Client // 进程级，按测试包初始化一次

func TestSignup(t *testing.T) {
    s := fx.NewScope(t)            // per-test 作用域，自动 t.Cleanup
    email := s.Email()             // fixture+ab12...@mtkit.test，全局唯一
    phone := s.Phone("CN")         // +86 开头的 E.164
    id    := s.UUID()
    // ... 用这些不碰撞的数据驱动用例
}
```

`fixture.New(addr, token)` 的 `addr`/`token` 来自 `RuntimeInfo`（读 runtime 文件得到）。

**注意事项**：
- **fail-loud 无 error 签名**：基建故障（daemon 不可达）由底层 `next()` 直接 `t.Fatalf` 终止用例，故所有生成函数签名**无 `error`**——这是有意的，避免每次取号都要判错。
- 参数非法（`UniqueInt` digits 越界、`Phone` 不支持的国家、`Email` 传多个 domain、`EmailFrom` seed 非裸邮箱）是**调用方错误，直接 panic**。
- `CaseScope` 本身无状态，并发调用经 `blockSource` 的锁串行化，可安全跨 goroutine。
- **断言用生成值、不写字面值**：算法确定但 **token 分配非确定**（并行用例抢号顺序由调度器决定，跨运行不保证同一字段拿同一 token）。须存下生成值、再断言 SUT 回写了同一个值；写死字面值会在新增/重排用例时 flaky。

```go
deviceUUID := s.UUID()          // 存下
createDevice(t, deviceUUID)
require.Equal(t, deviceUUID, getDevice(t).UUID)   // ✅ 对生成值
// require.Equal(t, "1e39…", getDevice(t).UUID)   // ❌ 字面值跨运行 flaky
```

---

## B. 前置数据装配（provision）

### B.1 三件套：Resource / Registry / Provision

```go
type Resource interface {
    Key() string                               // 账本中的持久化标识，如 "mysql.mssiot_user.users"
    Apply(ctx context.Context, env *PersistEnv) error  // 真正建表/索引/种子
}

type Registry interface {
    EnsureOnce(ctx, key, fn func() error) (skipped bool, err error)
}

func Provision(ctx, reg Registry, env *PersistEnv, resources []Resource) error
```

`Provision` 遍历资源逐个 `EnsureOnce`，保证**同一 daemon 生命周期内同一 key 只 Apply 一次**（跨进程安全）。接入处通常在 `WithSchema` 注册，由框架在持租约后、`m.Run()` 前执行（见 [orchestration.md §1](orchestration.md#1-运行模式分流)）。

### B.2 文本账本（`registry.go` + ADR-0001）

`FileRegistry` 是 `Registry` 的 v1 实现，用一个 JSON 文件记已 applied 的 key：

- **为什么是文本而非 MySQL**：Registry 必须在任何资源 provision **之前**可用；若后端用 MySQL，就形成「用 MySQL 来 provision MySQL」的循环依赖。文本账本零基建依赖，任何资源类型都能 provision（详见 [ADR-0001](../adr/0001-provision-registry-text-ledger.md)）。
- **整文件一把锁**：`EnsureOnce` 在文件锁内执行完整的「读账本 → 查 key → 跑 fn → 追加 key → 写回」。临界区时长 = 最慢一次 `Apply`（可能含网络 DDL），所有进程、所有 key 串行化。这是为保证「检查+执行+标记」原子性而**有意为之**（否则两进程会同时建表）；provision 低频、DDL 用 `IF NOT EXISTS`，串行成本可接受。
- **账本与 daemon 绑定**：daemon 启动时写空（`fixtured.InitFileLedger`），重启即重置，账本与数据同步清零——不会出现「账本说已 applied 但表不存在」。
- `skipped=true`（已注册、未执行）刻意**不走 error 通道**——幂等跳过是正常路径，用哨兵错误会迫使所有调用方过滤「非错误的错误」。

### B.3 连接池 PersistEnv（`persist_env.go`）

`Apply` 通过 `*PersistEnv` 拿到真实基础设施连接。按 `sutEnvKey` 懒建并进程内缓存复用，支持五类后端：

| 方法 | 后端 | 连通性校验 |
| --- | --- | --- |
| `MySQL(key)` / `TDengine(key)` | MySQL / TDengine（taosWS，无 cgo） | 构造期 `Ping`，失败 panic |
| `Redis(key)` / `Mongo(key)` | Redis / MongoDB | 构造期 `Ping`，失败 panic |
| `PulsarClient(key)` / `PulsarAdmin(key)` | Pulsar broker / admin | **仅校验 URL**，broker 不可达要到首次 produce/consume 才暴露 |

- `sutEnvKey` 是 `RuntimeInfo.SUTEnv` 的 key（如 `"MYSQL_DSN"`），**不是容器名或库名**；多容器/多库靠不同 key 区分。这些 key 由容器注册的 `SUTEnv` 回调产生（见 [container.md](container.md)）。
- 刻意用具体 struct 而非 interface：两类消费者（provision 的 `Apply`、测试期直接读写数据）都是真 I/O，没有可独立单测的纯逻辑；换来新增后端只需加方法、不破坏任何调用方。
- **多角色后端用多 key**（Pulsar）：`PulsarAdmin(key)`（建 tenant/namespace/topic，REST `:8080`）与 `PulsarClient(key)`（produce/consume，broker `:6650`）是两套独立 client、两个端点、**两个 key**，由同一 Pulsar 容器的 `SUTEnv` 回调一并吐出。两个 key 是否指向同一 Pulsar 由**注册方负责**，框架不校验，缺失则首次用到时 fail-loud。

### B.5 PersistEnv 使用契约与生命周期陷阱（**重要**）

- **必须 lazy 在 `m.Run()` 期间初始化，不能在 `mtkit.Run(m)` 之前**：runtime 文件（含 `SUTEnv`）只有 daemon 自启、Acquire 成功后才写好，而这些都发生在 `Run` 内部、`m.Run()` 之前。所以普通 `TestMain` 里 `Run` 调用前读不到 runtime——应在测试函数执行期用包级 `sync.Once` 懒读 runtime 再建 `PersistEnv`（fixture client 同理，见 [§A.5 用法主线](#a5-用法主线) 与 [README 快速开始](../../README.md#快速开始)）。
- **每进程只建一个 `PersistEnv`**：建 N 个 = N 份缓存 = 复用落空。用 `sync.Once` 持单例。
- **不在用例 `cleanup` 里 `Close`**：池是进程级共享物，一个用例关了会拖垮其他并发用例；进程退出自然回收。
- **裸 `sutEnvKey` 不进测试体**：用共享常量，测试现场只调项目语义 helper。

### B.4 Resource 由使用方实现（框架不预设任何类型）

mtkit **刻意不提供任何内置 Resource 类型**，只规定 `Resource` 接口。理由：provision 的形态由使用方业务决定、无法预见，内置类型会带来持续维护负担；且这类实现的语义全在 DDL/建库/建 namespace 等具体动作里，封装成框架类型并无价值（例如一个"建表"类型不过是把一行 `CREATE TABLE` 的 DDL 字符串原样转发）。使用方按需自实现接口即可：

```go
// 一个通用实现：语义全在闭包里（参 test/e2e/fixture_persist_env_e2e_test.go）
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
            "CREATE TABLE IF NOT EXISTS app.users (uid BIGINT PRIMARY KEY)") // IF NOT EXISTS 保证重入幂等
        return err
    }},
})
```

slice 顺序即执行顺序（`Provision` 顺序遍历），故"建库"排"建表"前。Mongo 建集合、Pulsar 建 namespace、TDengine 建库表同理——在 `Apply` 里用 `env.Mongo(key)` / `env.PulsarAdmin(key)` / `env.TDengine(key)` 执行真正动作。

**注意事项**：
- **`Key()` 一旦确定不可再改**——改了等于换标识，旧账本条目永不命中，会触发重复 `Apply`。
- **`Apply` 必须幂等可重入**：用 `IF NOT EXISTS` / 容忍"已存在"错误 / list-then-create；多条写入须用**事务**包装，失败回滚后 key 未标记、下次可安全重入。
- MySQL 首次建立连接时会在 30 秒总超时内按 500 毫秒间隔重试 Ping，用于覆盖 testcontainers 宿主机端口短暂抖动；超时后仍按 fail-loud 契约 panic。
- 其他后端连接失败一律 **fail-loud panic**（Pulsar 除外，见上表）——基建坏了应当尽早炸而非静默。

---

## C. daemon 侧 fixtured

`fixtured.Server` 把发号端点 `/v1/fixture/alloc` 挂在 daemon 主端口（与 lease/faked 共用端口与鉴权），由 daemon 启动时无条件创建（无 enable flag）。

`handleAlloc` 校验：方法须 POST；`kind` 必须是 `token`（v1 唯一 kind，拼错的 kind 一律拒绝而非静默吞）；`size` ∈ `(0, maxAllocSize=1<<20]`。上界防止异常请求一次性吃掉 token 值空间。

> 该端点是**内部 wire 协议**，使用者通过 `fixture.Client` 间接驱动，勿直接编码依赖。

---

## D. 注意事项汇总

- **唯一性是「阈值」不是「概率」**：选够大的形态（① 档）即严格零碰撞；窄形态（③ 档）静默别名，无运行期守卫——见 A.3。
- **生成函数无 error**：基建故障 `t.Fatalf`、参数错误 panic；这是刻意的 fail-loud 设计。
- **provision 串行且持锁跑 Apply**：临界区可能很长（网络 DDL），但 provision 低频，可接受。
- **`Key()` 不可变、`Apply` 要可重入**：这是账本幂等正确性的前提。
- **账本随 daemon 重启清零**：与容器数据生命周期一致，避免账本/数据不同步。
