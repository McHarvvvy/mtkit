# Fixture 落地规格（mtkit）

> **本文定位**：fixture 层（`client/fixture` + 服务端 `fixtured`）的**权威落地规格**——把技术细节细化到可直接照着实现的程度。它随讨论**逐步积累**：每敲定一个要点就并入本文一章。
>
> **与旧文档的关系**：`docs/fixture-design.md`、`docs/fixture-impl-plan.md` 是更早的**方向性**设计，目前**保留作背景参考、不再改动**。待本文把它们的要点也并入后，旧文档将被删除——**实现时只需读本文一份**。
>
> **命名基线**（与代码一致）：module `github.com/McHarvvvy/mtkit`；daemon = 测试二进制经 re-exec 以 `MTKIT_RUN_MODE=daemon` 进入的 daemon 模式（`orchestrator.Daemon`，无独立 `mtkitd` 二进制）；客户端包 `client/fixture`、`client/fake`；服务端 `fixtured`（与 lease 共用 daemon 主端口）；runtime 发现文件 `mtkit.runtime.json`。

## 章节 roadmap

- **§1 唯一性引擎** — 已定稿（本轮）。
- §2 L0 接线：`fixtured` 服务端原语 + `client/fixture` 客户端发现 —（待讨论）
- §3 provision：schema 声明 + `Registry`（锁/账本）+ 入口接缝 —（待讨论）
- §4 seeder / recipe 原语 —（待讨论）
- §5 request / observe 底座 —（待讨论）
- §6 CaseScope / PrimaryScope / `AssertBound` 隔离守卫 —（待讨论）

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

| 档 | 条件 | 含义 | 落档示例 |
|---|---|---|---|
| **① 无条件唯一** | `cap ≥ 2⁶³` | token 永远 `< cap`，**任何运行量都不重复** | UUID(32 hex)、hex `n≥16`、alnum `n≥11`、lower/upper `n≥14`、digit `n≥19` |
| **② 实践安全** | `运行生成量 ≪ cap < 2⁶³` | 现实测试量远小于 cap，不会重复；除非真生成超过 cap 个 | 10 位 uid(`10¹⁰≈2³³`)、12 位 lower(`26¹²≈2⁵⁶`)、email local-part(足够长) |
| **③ 有概率重复** | `cap ≲ 运行生成量`（≤~6 字符的定宽窄串） | token 可能溢出 cap、`mod` 折叠后**静默别名** | digit `n≤6`、hex `n≤5`、短验证码 |

> **重要**：10 位 uid 属 **②** 而非 **①**——它不是"无条件唯一"，而是"运行量远小于 10¹⁰ 所以现实安全"。文档与注释中**不得**把 ② 含糊表述为"保证唯一"。

`cap ≥ 2⁶³` 各字符集的最小 n（落入 ①）：

| 字符集（size） | 最小 n |
|---|---|
| Digit (10) | 19 |
| Hex (16) | 16 |
| Lower / Upper (26) | 14 |
| AlphaNum (62) | 11 |

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
第 2 层  塑形       UUID / UUIDDashed / Email / Phone …          ← 纯格式，复用第 1 层载荷
```

```go
// 第 1 层（唯一载荷）：均为 CaseScope 方法，token 内部自给、不进签名
func (s *CaseScope) UniqueString(n int, cs Charset) string   // 恰好 n 字符，全宽
func (s *CaseScope) UniqueHex(n int) string                  // = UniqueString(n, Hex)
func (s *CaseScope) UniqueInt(digits int) int64              // [0,10^digits) 内单射，返回数值

// 第 2 层（塑形）
func (s *CaseScope) UUID() string        { return s.UniqueHex(32) }   // 32 hex，无 v4 语义（meross 风格）
func (s *CaseScope) UUIDDashed() string                                // 同一 32-hex 载荷插 8-4-4-4-12
func (s *CaseScope) Email() string                                     // "u" + 载荷 + "@mtkit.test"
func (s *CaseScope) EmailFrom(seed string) string                      // seed + "+" + 载荷 + "@..."
func (s *CaseScope) Phone(country string) string                       // 载荷塑形进国内号段

// 确定性变换 / 时钟：无唯一性、无 token → 自由函数
func Sha256Hex(s string) string
func EpochSeconds() int64 ; func EpochMillis() int64
```

- **UUID 锁定为 32 hex（无 v4 version/variant 语义）**；dashed 仅插短线 ⇒ 36 字符。
- **复合字段一律"单载荷 + 塑形"**，不做分段独立发号（如 dashed UUID = 1 个 32-hex 载荷 + 插线，而非 5 段各自发号）。
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

- **进程内 `tokenSource` 单例 + `mu`**：覆盖 `t.Parallel()` 并发用例 / goroutine 抢号与补块边界。
- **`CaseScope` per-case**（`New(t)`）：持有进程级 `tokenSource` 句柄 + 存 `t`（供基建错误 fail-loud + 自动追踪生成值，见 §6 守卫）。
- **可复现性**：token→值的映射是确定的；但 token 的**分配顺序依赖调度**（并行），故跨运行不保证同一字段拿到同一值。**断言用对象字段、不用字面值**。

## 1.9 落地接缝（供 §2 实现，本章不实现）

- **服务端**：`fixtured` 包承载 `Allocator` + handler；经 `d.fixtured.RegisterRoutes(mux, d.withAuth)` 挂 daemon 主端口。
- **客户端发现**：`client/fixture` 内部 `tokenSource` 的生产实现 = thin fixtured client，经 `runtime.RuntimeInfo` 的 `addr` / `token`（读 `mtkit.runtime.json`）连 daemon 主端口——发现逻辑参照 `client/fake`。
- **单测隔离**：`tokenSource`、`Registry` 等内部接缝留给纯逻辑单测写 trivial fake，不起 daemon。

## 1.10 验证证据

算法由可运行 spike `test/spike/unique/unique_test.go` 实测背书（`-race` 干净、`-count=3` 无 flaky）：

| 测试 | 验证 | 结果 |
|---|---|---|
| `Token_Concurrent_SingleProcess` | 同进程跨 goroutine + 小块频繁补块的边界竞态 | 128 万 token 零重复 |
| `Token_MultiProcess_GlobalUnique` | 共享 allocator + 多进程多协程 | 128 万 token 全局零重复 |
| `Permute_IsBijection_Exhaustive` | 置换双射（hex/decimal/base62 穷举铺满） | distinct == cap |
| `UniqueString_EndToEnd` | 完整管线多进程多协程 | 100 万串零碰撞、长度/字符集合法 |
| `UniqueHex32_UUID` | UUID=32hex + dashed 格式 | 50 万零碰撞，样例 `1e39809231bb183197009a69842ad572` |
| `FirstCharSpread` / `AllPositionsSpread` | 全宽扩散（首字符 + 全 16 位） | 16/16、每位 ≥8/16 符号 |

> 注：spike 早期还含按形态命名 kind 与 budget tripwire 测试，对应讨论早期的 per-shape / fail-loud 取向；最终设计已改为**单一全局 kind + 静默全函数**，落代码阶段统一对齐。
