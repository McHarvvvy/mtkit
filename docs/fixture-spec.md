# Fixture 落地规格（mtkit）

> **本文定位**：fixture 层（`client/fixture` + 服务端 `fixtured`）的**权威落地规格**——把技术细节细化到可直接照着实现的程度。它随讨论**逐步积累**：每敲定一个要点就并入本文一章。
>
> **与旧文档的关系**：`docs/fixture-design.md`、`docs/fixture-impl-plan.md` 是更早的**方向性**设计，目前**保留作背景参考、不再改动**。待本文把它们的要点也并入后，旧文档将被删除——**实现时只需读本文一份**。
>
> **命名基线**（与代码一致）：module `github.com/McHarvvvy/mtkit`；daemon = 测试二进制经 re-exec 以 `MTKIT_RUN_MODE=daemon` 进入的 daemon 模式（`orchestrator.Daemon`，无独立 `mtkitd` 二进制）；客户端包 `client/fixture`、`client/fake`；服务端 `fixtured`（与 lease 共用 daemon 主端口）；runtime 发现文件 `mtkit.runtime.json`。

## 章节 roadmap

- **§1 唯一性引擎** — 已定稿（本轮）。
- **§2 L0 接线：`fixtured` 服务端原语 + `client/fixture` 客户端发现** — 已定稿（本轮）。
- **§3 provision** — 已定稿（本轮）。
- §4 seeder / recipe 原语 —（待讨论）
- §5 request / observe 底座 —（待讨论）
- §6 CaseScope / PrimaryScope / `AssertBound` 隔离守卫 — **不做**（隔离守卫过度设计，已确认取消）

---

# §1 唯一性引擎

测试数据的"唯一性"是并发隔离的根：SUT 调下游带的是下游协议字段（device UUID / token / merchant ref），这些字段唯一 → route_key 跨用例不同 → faked 不串。本章规定**怎样生成跨进程、跨用例、跨字段都唯一的数据**。

核心思想：**把"唯一性"与"格式"彻底拆开**。唯一性只在**一处**保证（L0 发一个全局唯一的 int64 token）；每种格式是从 token 到目标格式的一个**单射函数**（L1/L2）——碰撞免于逐格式证明。

```text
L0  唯一性原语   ── daemon 串行发块 → 进程本地区块缓存 → 全局唯一、单调的 int64 token
L1  单射载荷     ── token →(Feistel 保形置换)→ base 编码 → 恰好 n 字符 / digits 位
L2  塑形         ── 在 L1 载荷上加纯格式：UUID / email / dashed …（不引入新唯一性）
```

## 1.1 定位与边界：唯一性的"宇宙"

唯一性需要覆盖的范围 = **一个 daemon 的生命周期**。论据：

- 测试进程拓扑：`go test ./...` 每个包是一个 client 进程，经文件锁 + autostart **共享同一个 daemon**；包内 `t.Parallel()` 用例是同进程内的并发 goroutine。
- daemon 退出（IdleTTL reaper）时**连容器一起回收** → DB/Redis 数据销毁；下一个 daemon 启动先 `CleanupOrphans`（删同名容器）→ 数据从零开始。
- 于是**"计数器清零"与"数据清零"绑定在同一个 daemon 生命周期上**：新 daemon 从 0 发号，旧数据已不存在，不可能撞存量。

**前提（写死）**：存储是 daemon 托管的 **ephemeral 容器**（testcontainers）。本项目仅用于 CI 与本地验证、无长期持久化需求，故**不做**"持久化外部库 + epoch 播种"的逃生口。若将来真要指向持久库，需另行给 daemon 计数器播种一个 run-epoch（代价是吃掉值空间预算）。

## 1.2 唯一性保证（先讲结论）

**算法是确定性双射，不是哈希。** `permute(token, cap)` 是值空间 `[0, cap)` 上的一个**排列**（permutation），两个不同输入必然映射到两个不同输出——这是数学结构保证，零概率掺杂。

由此得到**逐格式的确定性保证**：

> 设 token 经 daemon 串行发块、全局唯一且单调。对形态 S（值空间 `cap = |cs|^n`），只要喂给 `permute` 的 token 都 `< cap`，S 的产出两两不同。

**证明**：S 被调用时取到的 token 构成全局序列的一个子集 `T_S`；`T_S` 内元素两两不同（全局单调）；所有元素 `< cap`（在 `permute` 定义域内）；`permute(·, cap)` 是 `[0,cap)` 上的双射，**双射限制到子集仍是单射** ⇒ S 的产出两两不同。∎

注意这条**对单一全局序列即成立**——多个形态共用一条全局序列，不影响"任一形态在其 cap 内不重复"。因此**不需要为每个形态分配独立序列**（per-shape kind）。

### 为什么否决 "hash 然后 mod"

一个看似更简单的方案是 `value = hash(token) mod cap`。它是**概率性**的：两个不同 token 经哈希后 mod cap 可能相同（生日碰撞）。以 10 位 uid（`cap = 10^10`）、一次运行生成 100 万个为例，期望碰撞数 ≈ `k²/(2·cap) = (10⁶)²/(2·10¹⁰) ≈ 50` 次——不可接受。**Feistel 双射把这个概率压到 0**（只要 token < cap），这正是选它而非哈希的原因。

### 唯一性 = 三档契约（关键，必须让调用方知晓）

底层 token 是 **`int64`**，从 0 单调递增，上界 ≈ **`2⁶³` ≈ 9.2×10¹⁸**。一个形态会不会重复，**只取决于它的值空间 `cap` 与运行期实际生成量的关系**：

| 档               | 条件                                     | 含义                                                  | 落档示例                                                                 |
| ---------------- | ---------------------------------------- | ----------------------------------------------------- | ------------------------------------------------------------------------ |
| **① 无条件唯一** | `cap ≥ 2⁶³`                              | token 永远 `< cap`，**任何运行量都不重复**            | UUID(32 hex)、hex `n≥16`、alnum `n≥11`、lower/upper `n≥14`、digit `n≥19` |
| **② 实践安全**   | `运行生成量 ≪ cap < 2⁶³`                 | 现实测试量远小于 cap，不会重复；除非真生成超过 cap 个 | 10 位 uid(`10¹⁰≈2³³`)、12 位 lower(`26¹²≈2⁵⁶`)、email local-part(足够长) |
| **③ 有概率重复** | `cap ≲ 运行生成量`（≤~6 字符的定宽窄串） | token 可能溢出 cap、`mod` 折叠后**静默别名**          | digit `n≤6`、hex `n≤5`、短验证码                                         |

> **重要**：10 位 uid 属 **②** 而非 **①**——它不是"无条件唯一"，而是"运行量远小于 10¹⁰ 所以现实安全"。文档与注释中**不得**把 ② 含糊表述为"保证唯一"。

`cap ≥ 2⁶³` 各字符集的最小 n（落入 ①）：

| 字符集（size）     | 最小 n |
| ------------------ | ------ |
| Digit (10)         | 19     |
| Hex (16)           | 16     |
| Lower / Upper (26) | 14     |
| AlphaNum (62)      | 11     |

## 1.3 L0 区块发号

唯一性来源是一个**全局唯一、单调的 int64 token 序列**，由 daemon 串行发块、进程本地缓存消费（HiLo 模式）。

### 服务端（`fixtured`，挂 daemon 主端口）

```go
// fixtured 内部：单一全局计数器（kind 维度预留，v1 只用一个 kind）。
// AllocIDBlock 串行返回 [start, start+size) 半开区间，跨调用者天然不相交。
func (a *Allocator) AllocIDBlock(kind string, size int64) (start, end int64)
```

- 实现 = 一个 `atomic.Int64`（按 kind 存于 map，v1 单 kind）。FetchAdd 串行化 ⇒ **不同进程拿到的区块不相交**。
- 路由挂 daemon 主 mux——复用 `orchestrator/server.go` 已预留的接缝：

  ```go
  // orchestrator/server.go Start() 内，与 registerLeaseRoutes 并列：
  d.fixtured.RegisterRoutes(mux, d.withAuth)  // 与 lease 共用端口 + 同款 token 鉴权
  ```

- 鉴权复用 lease 的 `withAuth`（`mtkit.runtime.json` 的 `token`），仅面向本机测试进程。

### 进程侧（`client/fixture` 内部 `tokenSource`）

```go
// blockSource：一个进程一个（单一全局 kind），所有 CaseScope/goroutine 共享。
type blockSource struct {
    mu        sync.Mutex
    next, end int64                                   // 当前本地区块游标
    size      int64                                   // 区块大小 = 65536
    fetch     func(size int64) (start, end int64, error) // 经 fixtured client 远调 AllocIDBlock
}

func (b *blockSource) next() int64 {
    b.mu.Lock(); defer b.mu.Unlock()
    if b.next >= b.end {                  // 冷路径：补块（每 65536 次一次远调）
        s, e, err := b.fetch(b.size)
        if err != nil { /* 基建故障 → 见 §1.7：内部 t.Fatalf */ }
        b.next, b.end = s, e
    }
    v := b.next; b.next++
    return v
}
```

- **块大小 65536**、**base 0**、**单一全局 kind**。锁只在补块时短暂持有；区块内（65535 次）是纯本地自增、热路径几乎无成本。
- **进程内单例**、跨 `CaseScope` 共享 ⇒ 同进程跨用例 / 跨字段唯一；daemon 串行 ⇒ 跨进程全局唯一。
- `tokenSource` 是**内部小写接缝**，不进任何公共签名（见 §1.6 的 API 形态）。生产实现 = thin fixtured client；纯逻辑单测喂 trivial fake（确定序列）。

## 1.4 L1 单射：Feistel FPE + base 编码

把一个 token 单射成"恰好 n 字符、全宽随机外观"的串，用**保形加密（FPE）的标准构造：平衡 Feistel 网络 + cycle-walking**。它对任意基数 / 长度都可证双射，且充分扩散。

```text
cap   = |cs|^n
split = ⌈√cap⌉                         // 把值空间近似拆成两半，domain = [0, split²) ⊇ [0, cap)

feistelOnce(v):                        // [0, split²) 上的一次置换
    L, R = v / split, v % split        // 各在 [0, split)
    for i in 0..3:                      // 4 轮
        F = SHA256(key ‖ i ‖ R) mod split
        L, R = R, (L + F) % split
    return L*split + R

permute(token):                        // [0, cap) 上的双射 + 全函数
    y = token mod cap                  // 折叠进定义域（cap ≥ 2⁶³ 时即恒等）
    loop:
        y = feistelOnce(y)
        if y < cap: return y            // cycle-walking：期望迭代 < 2 次

UniqueString = encode(permute(token), n, cs)   // base |cs| 编码到恰好 n 字符，低位在右
```

- **轮函数 = keyed SHA-256，固定 key** ⇒ 映射确定、可复现；4 轮取充分扩散（Feistel ≥3 轮即双射）。
- **cycle-walking**：`split² ≥ cap` 仅略大于 cap，故反复施加的期望次数 < 2；`feistelOnce` 是 `[0, split²)` 的置换 ⇒ 该过程是 `[0, cap)` 的双射。
- **全宽扩散**（避免固定前缀 / 可预测尾）：每个字符位都铺开整个字符集——见 §1.10 实证。

> **spike 教训（写进文档备忘）**：第一版用"单次仿射乘法 `y=(token·M+B) mod cap`、M=梅森素数 2¹²⁷−1"，在 2 的幂模数下退化为 `y≈B−token`（M ≡ −1），产生 `0000…` 长前缀——注入性还在但全宽外观垮了。压力测试当场暴露，遂改 Feistel。**单次模乘不是可靠的扩散器，勿用。**

**实现注记（落代码阶段定，不影响算法）**：

- `cap < 2⁶⁴` 可用纯 `uint64` 跑 Feistel（无需 big.Int），更快；
- `cap ≥ 2⁶⁴`（hex `n≥16`、UUID）属 ① 档、token 必 `< cap`，可用 splitmix64 双 limb 注入替代 Feistel，更省；
- spike 为"全基数统一可证"用了 `math/big`，是验证取向，非性能取向。

## 1.5 charset 定义

`cs`（`Charset`）是**字母表**，不是单纯的"进制"——它的 **size 充当基数**、**符号集决定输出字符**（两个同 size 的字母表，如大写 hex 与小写 hex，是不同 charset）。初始集合：

```go
type Charset int
const ( AlphaNum Charset = iota /* 62 */; Lower /* 26 */; Upper /* 26 */; Hex /* 16 */; Digit /* 10 */ )
```

可后续扩充（如 base32）。每个 charset 固定其符号序列，`encode` 据此把 `permute` 的输出映成字符。

## 1.6 L2 载荷 / 塑形分层

**心智模型（锁定）**：唯一性只在第 1 层产生；第 2 层只做格式、不引入新唯一性。

```text
第 1 层  唯一载荷   UniqueString / UniqueHex / UniqueInt        ← 携带全部唯一性（§1.4）
第 2 层  塑形       UUID / UUIDNoDash / Email / Phone …          ← 纯格式，复用第 1 层载荷
```

```go
// 第 1 层（唯一载荷）：均为 CaseScope 方法，token 内部自给、不进签名
func (s *CaseScope) UniqueString(n int, cs Charset) string   // 恰好 n 字符，全宽
func (s *CaseScope) UniqueHex(n int) string                  // = UniqueString(n, Hex)
func (s *CaseScope) UniqueInt(digits int) int64              // [0,10^digits) 内单射，返回数值

// 第 2 层（塑形）
func (s *CaseScope) UUID() string                                      // RFC 4122 v4 标准带短线格式（36字符）；内部消耗 UniqueHex(30)，每次调用产出独立标识符
func (s *CaseScope) UUIDNoDash() string                                // RFC 4122 v4 紧凑无短线格式（32字符）；内部消耗 UniqueHex(30)，与 UUID() 相互独立
func (s *CaseScope) Email() string                                     // "fixture+" + 载荷 + "@mtkit.test"（.test 为 RFC 2606 测试保留 TLD）
func (s *CaseScope) EmailFrom(seed string) string                      // seed 须为合法 email；输出 {local}+{载荷}@{domain}，local part 原样保留；非法格式 panic
func (s *CaseScope) Phone(country string) string                       // E.164 格式（如 +8613812345678）；country 为 ISO 3166-1 alpha-2；初始支持 CN/US/GB/DE/FR/JP/IT/CA；不支持则 panic

// 确定性变换 / 时钟：无唯一性、无 token → 自由函数
func Sha256Hex(s string) string
func EpochSeconds() int64 ; func EpochMillis() int64
```

- **UUID 为 RFC 4122 v4**：version nibble 固定 `4`、variant nibble 固定 `a`，两者插入 `UniqueHex(30)` 的固定位置，剩余 30 个 hex 位承载唯一性（属 ① 档，2^120 > 2^63）。`UUID()` 与 `UUIDNoDash()` 各消耗独立 token，产出两个不同标识符；不存在"同一标识符两种格式"语义。
- **项目自有 32-hex 标识符（如 meross 设备 ID）不命名为 UUID**：若只需"与 UUID 等长的唯一 hex 串"且不要求 v4 合规，用 L1 的 `UniqueHex(32)` 直接生成，不经 L2 UUID 入口。
- **复合字段一律"单载荷 + 塑形"**，不做分段独立发号（如 UUID = UniqueHex(30) 输出 + 插入固定 version/variant 位）。
- 项目自有格式（`guid` 排布、`order_id` 方案、加密对象）在项目层用第 1 层载荷自行塑形，不进 `client/fixture`。

## 1.7 唯一性代价契约（静默全函数）

**`UniqueString` 是 total 函数：任何输入都返回值，唯一性不设运行期守卫、不 fail-loud。**

理由：单一全局序列下，无法由调用次数廉价判定某形态是否已重复（那需要 per-shape 计数 = per-shape kind，已否决）；而会触发守卫的只有 ③ 档窄形态，那本就不属本层保证范围。故让函数保持 total + 契约清晰，把"是否需要短串唯一"的决策推给调用方。

- **行为**：`encode(permute(token mod cap, cap))`。`cap ≥ 2⁶³`（① 档）时 `mod` 即恒等、token 永不溢出；`cap` 较小时按 cap 折叠，超过 cap 个后**静默别名**（可能重复）。
- **契约**：调用方据自己的 `(n, cs)` 算出 cap、对照 §1.2 三档表，自行决定是否加**外部排重**——或意识到"被测程序依赖短串全局唯一"本身可能是设计缺陷。
- **已知代价（如实声明）**：完全静默 ⇒ 若误把 ③ 档窄形态当唯一字段用，碰撞会在**下游**（如 DB 唯一键冲突）才暴露、不指向源头。缓解 = 把本契约写在函数文档最显眼处、责任在调用侧。
- **明确不做**：fail-loud 唯一性守卫、预算拉伸 / 重试、per-shape 序列隔离。

> **基建错误仍 fail-loud（与唯一性正交）**：补块时连不上 daemon 等**基础设施故障**，由生成器内部 `t.Fatalf`（用 `CaseScope` 在 `New(t)` 时存下的 `t`）。这不是唯一性问题，而是环境不可用。
>
> 因此唯一性生成器签名无 `error`、无 per-call `t`：即 `fx.UniqueString(n, cs) string`。

## 1.8 并发与生命周期

### 两层并发保证

- **第一层 · 跨进程（daemon `Allocator`）**：多个 test 包进程（`go test ./...` 并发跑各包）同时向 daemon 请求区块；daemon 内 `Allocator` 用 `atomic.Int64` FetchAdd 串行化，每个进程拿到的 `[start, end)` 不相交，**跨进程 token 全局唯一**。
- **第二层 · 进程内（`blockSource.mu`）**：同进程内多个 `t.Parallel()` 用例是并发 goroutine，同时调 `tokenSource.Next()`；`blockSource.mu`（`sync.Mutex`）保护 `next/end` 游标。正常路径（块内自增）持锁极短；块用完才补块（每 65536 次一次远调）⇒ **进程内 token 唯一**，热路径几乎无锁竞争。

**`CaseScope` per-case**：每个测试用例 `New(t)` 创建一个 `CaseScope`，持有进程级 `tokenSource` 句柄 + 存 `t`（供基建错误 fail-loud 与后续 §6 守卫）。`CaseScope` 不持有独立计数器，直接共享进程单例 `tokenSource`。

### 可复现性（reproducibility）

两件事需要区分：

**算法是确定性的**：Feistel 用固定 key，`permute(42, cap)` 在任何机器任何次运行都产出同一字符串。这个属性是**可以依赖**的。

**token 分配是非确定性的**：并行用例抢号谁先谁后由 goroutine 调度器决定，跨运行不保证同一字段拿到同一 token，因而不保证同一输出值。

实际规则：**断言用生成时拿到的值对比系统返回值，不写字面值**。

```go
// 错误——字面值跨运行 flaky
assert.Equal(t, "1e39809231bb183197009a69842ad572", resp.DeviceUUID)

// 正确——存下生成值，断言 SUT 回写了同一个值
deviceUUID := scope.UUID()
createDevice(t, deviceUUID, ...)
assert.Equal(t, deviceUUID, getDevice(t).UUID)
```

这条规则与并发无关，顺序运行也适用——一旦 token 序列因任何原因变化（新增用例、重排顺序），字面断言都会失效。

## 1.9 落地接缝（供 §2 实现，本章不实现）

- **服务端**：`fixtured` 包承载 `Allocator` + handler；经 `d.fixtured.RegisterRoutes(mux, d.withAuth)` 挂 daemon 主端口。
- **客户端发现**：`client/fixture` 内部 `tokenSource` 的生产实现 = thin fixtured client，经 `runtime.RuntimeInfo` 的 `addr` / `token`（读 `mtkit.runtime.json`）连 daemon 主端口——发现逻辑参照 `client/fake`。
- **单测隔离**：`tokenSource`、`Registry` 等内部接缝留给纯逻辑单测写 trivial fake，不起 daemon。

## 1.10 验证证据

算法由可运行 spike `test/spike/unique/unique_test.go` 实测背书（`-race` 干净、`-count=3` 无 flaky）：

| 测试                                     | 验证                                        | 结果                                                 |
| ---------------------------------------- | ------------------------------------------- | ---------------------------------------------------- |
| `Token_Concurrent_SingleProcess`         | 同进程跨 goroutine + 小块频繁补块的边界竞态 | 128 万 token 零重复                                  |
| `Token_MultiProcess_GlobalUnique`        | 共享 allocator + 多进程多协程               | 128 万 token 全局零重复                              |
| `Permute_IsBijection_Exhaustive`         | 置换双射（hex/decimal/base62 穷举铺满）     | distinct == cap                                      |
| `UniqueString_EndToEnd`                  | 完整管线多进程多协程                        | 100 万串零碰撞、长度/字符集合法                      |
| `UniqueHex32_UUID`                       | UUID=32hex + dashed 格式                    | 50 万零碰撞，样例 `1e39809231bb183197009a69842ad572` |
| `FirstCharSpread` / `AllPositionsSpread` | 全宽扩散（首字符 + 全 16 位）               | 16/16、每位 ≥8/16 符号                               |

> 注：spike 早期还含按形态命名 kind 与 budget tripwire 测试，对应讨论早期的 per-shape / fail-loud 取向；最终设计已改为**单一全局 kind + 静默全函数**，落代码阶段统一对齐。

---

# §2 L0 接线

本章规定唯一性引擎（§1）与真实 daemon 之间的**接线**：服务端如何暴露 `AllocIDBlock` 原语、客户端如何发现 daemon 并管理本地区块缓存。

核心原则：
- 服务端挂 daemon 主端口，复用已有 `withAuth` 鉴权，**无需新端口、无需额外配置**。
- 客户端发现模式与 `client/fake` 完全一致：调用方读 `mtkit.runtime.json`，显式传 `addr+token`。
- 进程级 `blockSource` 单例隐藏在 `Client` 内部；`CaseScope` per-test，只暴露生成函数。

## 2.1 服务端（`fixtured` 包）

### HTTP 接口

```
POST /v1/fixture/alloc
Content-Type: application/json
Authorization: X-Mtkit-Token: <token>

请求体：{"kind": "token", "size": 65536}
成功响应：{"start": 0, "end": 65536}
错误响应：{"ok": false, "error": "invalid_size", "message": "size must be positive"}
```

- **路由前缀**：`/v1/fixture/`，与 lease（`/v1/`）和 faked（`/v1/grpc/`、`/v1/http/`）各自不冲突。
- **鉴权**：复用 daemon 的 `withAuth` 中间件（`X-Mtkit-Token` 对比 `cfg.Token`），与 lease / faked 同款。
- **v1 只有 `"token"` 一个 kind**；kind 字段为后续扩展预留（格式稳定，语义不变）。

### Allocator

```go
// v1 单一全局计数器（kind 字段在 HTTP 协议中预留，代码暂不分 kind）。
type Allocator struct {
    counter atomic.Int64
}

// Alloc 串行返回 [start, start+size) 半开区间，跨调用者天然不相交。
// atomic.Add(size) 得到 newEnd，start = newEnd - size，无锁。
func (a *Allocator) Alloc(kind string, size int64) (start, end int64)
```

### Server

```go
// Server 是 fixtured 的服务端，由 daemon 在启动时无条件创建（无 enable flag）。
type Server struct {
    alloc *Allocator
}

func New() *Server

// RegisterRoutes 把 handler 挂到 daemon 主 mux，复用 wrap（即 d.withAuth）鉴权。
func (s *Server) RegisterRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
    mux.HandleFunc("/v1/fixture/alloc", wrap(s.handleAlloc))
}
```

### 挂载点

```go
// orchestrator/lifecycle.go — Daemon.New() 中
d.fixtured = fixtured.New()   // 无条件，无需 FakedEnabled 类似的 flag

// orchestrator/server.go — Start() mux 注册段（取消注释已预留接缝）
d.fixtured.RegisterRoutes(mux, d.withAuth)
```

## 2.2 客户端（`client/fixture` 包）

### 架构分层

```text
CaseScope          (per-test)   — 持 t + src；暴露 UniqueString / UUID 等生成函数
    └── blockSource (进程级单例) — 持 mu + next/end；热路径本地自增，冷路径补块
            └── HTTP client     (thin)        — POST /v1/fixture/alloc，持 addr + token
```

### 内部 tokenSource 接口

```go
// iTokenSource 仅在包内使用，不进任何公共签名。
// error 仅在基建故障（daemon 不可达、网络超时）时非 nil；正常路径无 error。
type iTokenSource interface {
    Next() (int64, error)
}
```

### blockSource

```go
type blockSource struct {
    mu    sync.Mutex
    next  int64
    end   int64
    size  int64  // 固定 65536
    kind  string // 固定 "token"
    fetch func(kind string, size int64) (start, end int64, err error)
    // fetch 实现须带 5s 超时；超时视同 daemon 不可达，由调用侧 t.Fatalf 处理。
    // 补块失败不重试——本地+CI 均为回环地址，daemon 不可达即基建故障，无重试价值。
}

func (b *blockSource) Next() (int64, error) {
    b.mu.Lock()
    defer b.mu.Unlock()
    if b.next >= b.end {            // 冷路径：每 65536 次一次远调
        s, e, err := b.fetch(b.kind, b.size)
        if err != nil {
            return 0, err           // 立即返回错误，不重试
        }
        b.next, b.end = s, e
    }
    v := b.next
    b.next++
    return v, nil
}
```

### CaseScope

```go
type CaseScope struct {
    t   testing.TB
    src iTokenSource  // 指向进程级 blockSource
}

// next 是 CaseScope 所有生成函数的底层出口。
// 基建故障（daemon 不可达）在此通过 t.Fatalf 终止用例，不向上传播 error。
func (s *CaseScope) next() int64 {
    v, err := s.src.Next()
    if err != nil {
        s.t.Fatalf("fixture: daemon unreachable: %v", err)
    }
    return v
}

// cleanup 是 t.Cleanup 注册的回调，当前为 no-op；按需填充。
func (s *CaseScope) cleanup() {}

// 第 1 层：唯一载荷（§1.6）
func (s *CaseScope) UniqueString(n int, cs Charset) string
func (s *CaseScope) UniqueHex(n int) string   { return s.UniqueString(n, Hex) }
func (s *CaseScope) UniqueInt(digits int) int64

// 第 2 层：塑形（格式细节见 §1.6）
func (s *CaseScope) UUID() string              // RFC 4122 v4 带短线（36字符）
func (s *CaseScope) UUIDNoDash() string        // RFC 4122 v4 无短线（32字符）
func (s *CaseScope) Email() string             // "fixture+{载荷}@mtkit.test"
func (s *CaseScope) EmailFrom(seed string) string // seed 为合法 email，见 §1.6
func (s *CaseScope) Phone(country string) string  // E.164，ISO alpha-2，见 §1.6
```

## 2.3 进程级 Client 与发现

### 发现模式

与 `client/fake` 完全一致：调用方自行读取 `mtkit.runtime.json` 拿到 `RuntimeInfo.Addr` / `RuntimeInfo.Token`，显式传入 `fixture.New`。没有自动发现、没有隐式全局状态。

```go
// client/fixture/client.go

// Client 进程级对象，持有 blockSource 单例。整个测试进程只创建一次（通常在 TestMain）。
type Client struct {
    src *blockSource
}

// New 创建进程级 fixture client。
// addr、token 来自 runtime.Read(runtimePath) 返回的 RuntimeInfo.Addr / .Token。
func New(addr, token string) *Client

// NewScope 为单个测试用例创建 CaseScope，t 用于基建错误 fail-loud。
// 内部注册 t.Cleanup（no-op，按需填充）；每个用例调用一次（通常在 test helper 顶部）。
func (c *Client) NewScope(t testing.TB) *CaseScope {
    s := &CaseScope{t: t, src: c.src}
    t.Cleanup(s.cleanup)
    return s
}
```

### 调用侧示例

```go
// setup_test.go（TestMain 级别）
var fxClient *fixture.Client

func TestMain(m *testing.M) {
    ri, err := runtime.Read(runtimePath)
    if err != nil { log.Fatal(err) }
    fxClient = fixture.New(ri.Addr, ri.Token)
    os.Exit(m.Run())
}

// 每个测试用例
func TestCreateDevice(t *testing.T) {
    scope := fxClient.NewScope(t)
    deviceUUID := scope.UUID()
    // ... 用 deviceUUID 构造请求、断言响应 ...
}
```

## 2.4 单测隔离 seam

`iTokenSource` 是包内接口，允许纯逻辑单测注入 trivial fake、不起 daemon：

```go
// 包内辅助（unexported），供单测使用
type seqSource struct{ n int64 }
func (s *seqSource) Next() (int64, error) { v := s.n; s.n++; return v, nil }

// newScopeWith 包内构造函数，绕过 HTTP 层直接注入 source
func newScopeWith(t testing.TB, src iTokenSource) *CaseScope {
    return &CaseScope{t: t, src: src}
}
```

公共 `New(addr, token)` / `NewScope(t)` 不暴露 source 参数——公共 API 保持最小化，seam 只对包内测试可见。

---

# §3 provision

provision 解决的核心问题：**测试前置数据（建表、种子数据）在多个测试进程并发或先后运行时不冲突、不重复执行**。

核心机制三件套：
1. `Resource`——声明"要建什么"
2. `Registry`——分布式互斥锁 + 持久化"已应用"账本
3. `PersistEnv`——进程级连接池，供 Apply 和 Registry 使用

## 3.1 Resource 接口

```go
// Resource 表示一个可 provision 的基础设施资源（表、索引、命名空间、种子数据等）。
// Key() 返回跨进程跨运行的稳定字符串，用作 Registry 账本的条目 key。
// Apply 在 Registry 互斥锁内、且 Applied(key)==false 时被 Provision 调用。
// Apply 应具备重试安全性：多条写入需事务包装，保证"全成功或全回滚"。
type Resource interface {
    Key() string
    Apply(ctx context.Context, env PersistEnv) error
}
```

- `Key()` 推荐命名模式：`"storage.schema.object"`，如 `"mysql.mssiot_user.users"`。
- `Key()` 的返回值**不能变**——它是 Registry 账本的持久化标识，一旦改变，旧账本条目永远不会被命中。
- `Checksum()` 已删除——v1 不做 staleness 检测；daemon 生命周期与容器绑定，数据随容器销毁。

## 3.2 Registry 接口

```go
// Registry 提供跨进程幂等执行保证。
// v1 实现后端为文本账本（见 §3.2.1）；后续可替换为 Redis、MySQL 事务等，不影响 Provision 逻辑。
type Registry interface {
    // EnsureOnce 在互斥保护下幂等执行 fn。
    // - skipped=true：key 已注册，fn 未执行（正常幂等路径）。
    // - skipped=false, err=nil：fn 执行成功，key 已标记。
    // - err!=nil：fn 执行失败或基建故障；key 不被标记，下次可重入。
    // ctx 控制等锁超时；fn 内部应自行处理自身的超时。
    EnsureOnce(ctx context.Context, key string, fn func() error) (skipped bool, err error)
}
```

**接口为何只有一个方法**：早期草案拆为 `WithLock` + `Applied` + `MarkApplied` 三段，但"检查 + 执行 + 标记"的原子性因此分散到调用方——每个后端都要在接口缝隙里重建原子性（MySQL 需在锁外锁内各读一次）。`EnsureOnce` 把原子性下推给后端实现，接口契约更紧。见 ADR-0001。

**`skipped` 不走 `error` channel**：幂等跳过是正常路径，`error != nil` 在 Go 惯例里表示"出了问题"。若用哨兵错误，所有调用方都要过滤这个"非错误的错误"，容易漏掉。`skipped bool` 供审计/监控使用，不关心的调用方直接忽略。

## 3.2.1 v1 实现：文本账本（FileRegistry）

**为什么不用 MySQL**：Registry 必须在任何资源被 provision 之前就可用。MySQL 后端要求 MySQL 连接，造成"用 MySQL 来 provision MySQL"的循环依赖。文本账本无任何基础设施依赖，任何资源类型均可 provision。见 ADR-0001。

**账本文件**：`{RuntimeDir}/mtkit.provision.json`，格式为 JSON 集合：`{"applied":["key1","key2"]}`。

**生命周期**：daemon 在 `Start()` 时写入空文件（`{"applied":[]}`），与 daemon/容器生命周期严格绑定——daemon 重启 → 文件重置 → 容器重建，账本与数据同步清零，不会出现"账本说已 applied 但表不存在"的状态。

**互斥**：整文件一把锁（Linux/Mac 用 `flock`，Windows 用 `LockFileEx`），`EnsureOnce` 持锁期间执行完整的"读文件 → 检查 key → 执行 fn → 写文件"流程。粒度粗但 provision 是低频启动操作，锁竞争窗口极短，并发收益可忽略。

**路径发现**：clients 从已知的 `runtimePath` 推算：`filepath.Join(filepath.Dir(runtimePath), "mtkit.provision.json")`，无需修改 `RuntimeInfo` 结构。

```go
// FileRegistry 是 Registry 的 v1 实现，后端为本地文本账本文件。
type FileRegistry struct { /* ... */ }

// NewFileRegistry 创建文本账本 Registry。ledgerPath 由调用方从 runtimePath 推算。
func NewFileRegistry(ledgerPath string) *FileRegistry
```

## 3.3 PersistEnv 接口

`PersistEnv` 从 `RuntimeInfo.SUTEnv`（`mtkit.runtime.json` 的 `sut_env` 字段）读取连接字符串，按需建立连接并在进程内复用。**不改 `RuntimeInfo` 结构，不改 `ContainerRegistration`。**

```go
// PersistEnv 提供测试基础设施连接，供 Resource.Apply 和 Registry 使用。
// 连接在进程内懒初始化并按 sutEnvKey 缓存，整个测试进程生命周期复用。
type PersistEnv interface {
    // MySQL 返回 sut_env[sutEnvKey] 所指数据库的 *sql.DB。
    // sutEnvKey 是 ContainerRegistration.SUTEnv 回调中定义的 env var 名，如 "MYSQL_DSN"。
    MySQL(sutEnvKey string) *sql.DB
    Redis(sutEnvKey string) redis.UniversalClient
    Mongo(sutEnvKey string) *mongo.Client
}
```

**`sutEnvKey` = `sut_env` map 的 key，不是容器名，不是数据库名。**

多容器、多数据库、同类容器多实例，靠不同 env var key 区分：

```text
sut_env 内容示例：
  MYSQL_DSN          →  mysql 容器，mssiot_user 库
  MYSQL_DEVICE_DSN   →  mysql-device 容器，mssiot_device 库
  REDIS_ADDR         →  redis 容器
```

连接复用实现：进程级单例，`sync.Mutex` + map 缓存，一个 `sutEnvKey` 对应一个连接对象。

## 3.4 Resource 工厂辅助函数

```go
// MySQLTable 创建"建表"类 Resource。
// sutEnvKey: sut_env 中数据库连接的 key（如 "MYSQL_DSN"）。
// key:       资源稳定标识（如 "mysql.mssiot_user.users"）。
// ddl:       CREATE TABLE 语句；建议含 IF NOT EXISTS（Apply 幂等，便于手动重试）。
func MySQLTable(sutEnvKey, key, ddl string) Resource

// RedisNS 声明 Redis 命名空间（v1 Apply 为 no-op，文档性资源）。
func RedisNS(sutEnvKey, key, ns string) Resource
```

推荐将稳定 Resource 变量放在 `test/fixture/resources/` 共享包中：

```go
// test/fixture/resources/mysql.go
var UserTable = fixture.MySQLTable(
    "MYSQL_DSN",
    "mysql.mssiot_user.users",
    `CREATE TABLE IF NOT EXISTS users (uid BIGINT PRIMARY KEY, ...) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
)
```

## 3.5 注册机制（混合 A+C 方案）

**约定**：每个测试包有一个本地聚合函数 `requirements()`，返回该包需要 provision 的 `[]Resource`。TestMain 将其传给 `WithSchema`，在 `Run` 内 daemon 就绪后统一 provision。

资源变量来自共享包（稳定 Key 保证），本地 `requirements()` 负责聚合：

```
# 以下路径属于调用方项目，不在 mtkit 仓库内

test/fixture/resources/        ← 共享稳定 Resource 定义（跨测试包复用）
    mysql.go                   → var UserTable, DeviceTable, ...
    redis.go                   → var TokenCache, ...

test/infra/                    ← 集成测试包（与 run.go、containers.go、sut.go 同目录）
    setup_test.go              → TestMain 调 WithSchema(requirements()).Run(m)
    requirements_test.go       → func requirements() []fixture.Resource { return []fixture.Resource{resources.UserTable, ...} }
```

```go
// requirements_test.go
var testResources []fixture.Resource

func init() { testResources = append(testResources, resources.UserTable, resources.TokenCache) }

func requirements() []fixture.Resource { return testResources }
```

## 3.6 Provision 函数

```go
// Provision 遍历 resources，对每个资源调 EnsureOnce；
// 保证：同一 daemon 生命周期内，同一 key 只 Apply 一次（跨进程安全）。
// Apply 失败 → 返回带资源 key 的错误（通常由调用方 Fatal 终止测试包）。
func Provision(ctx context.Context, reg Registry, env PersistEnv, resources []Resource) error
```

内部实现骨架：

```go
func Provision(ctx context.Context, reg Registry, env PersistEnv, resources []Resource) error {
    for _, r := range resources {
        _, err := reg.EnsureOnce(ctx, r.Key(), func() error {
            return r.Apply(ctx, env)
        })
        if err != nil {
            return fmt.Errorf("provision %s: %w", r.Key(), err)
        }
    }
    return nil
}
```

**非幂等 Apply 的安全性**：`EnsureOnce` 提供 run-once 保证，不要求 Apply 本身幂等。凡涉及多条数据写入的 Apply，需用事务包装——若中途失败则回滚，下次重试时 key 未标记可以重入。

## 3.7 接入 TestMain

```go
// TestMain 接入示例（mtkit.New(...).WithSchema(...).Run(m) 内部处理 daemon 就绪 + provision）
func TestMain(m *testing.M) {
    ri, err := runtime.Read(runtimePath)
    if err != nil { log.Fatal(err) }

    fxClient = fixture.New(ri.Addr, ri.Token)

    os.Exit(
        mtkit.New(project, cfg).
            RegisterContainers(myContainers).
            RegisterSUT(mySUT).
            WithSchema(requirements()).   // daemon 就绪后、m.Run() 前执行 provision
            Run(m),
    )
}
```

- `WithSchema([]Resource)` 接收资源列表，`Run` 内部在 daemon 就绪后构造 `PersistEnv`（读 `ri.SUTEnv`）和 `Registry`，调用 `Provision`。
- `Provision` 失败 → `Run` 以非零码退出（`log.Fatal` 级别），不进入 `m.Run()`。
