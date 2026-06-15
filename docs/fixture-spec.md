# Fixture 落地规格（mtkit）

> **本文定位**：`client/fixture` 与服务端 `fixtured` 的落地规格，细化到可直接照此实现的程度。旧文档 `fixture-design.md`、`fixture-impl-plan.md` 保留作背景参考，**实现以本文为准**。
>
> **命名基线**：module `github.com/McHarvvvy/mtkit`；daemon = 测试二进制经 re-exec 以 `MTKIT_RUN_MODE=daemon` 进入的 daemon 模式；runtime 发现文件 `mtkit.runtime.json`；客户端 `client/fixture`；服务端 `fixtured`（与 lease 共用 daemon 主端口）。
>
> **进度**：§1–3 已定稿；§4–5 待讨论；§6（CaseScope 隔离守卫）已确认**不做**——并发隔离由唯一生成本身保证，守卫机制收益低。`Primary()`、`AssertBound`、`Gen` 接口、`BindToScope`、生成值自动追踪均不实现。

---

# §1 唯一性引擎

测试数据的唯一性是并发隔离的根：SUT 调下游带的是协议字段（device UUID / token / merchant ref），字段唯一则 route_key 跨用例不同，faked 不串。本章规定**怎样生成跨进程、跨用例、跨字段都唯一的数据**。

核心思想：**把"唯一性"与"格式"彻底拆开**——唯一性只在一处保证（daemon 串行发全局唯一 int64 token）；每种格式是从 token 到目标格式的单射函数，碰撞免于逐格式证明。

```text
L0  唯一性原语   ── daemon 串行发块 → 进程本地区块缓存 → 全局唯一、单调的 int64 token
L1  单射载荷     ── token →(Feistel 保形置换)→ base 编码 → 恰好 n 字符 / digits 位
L2  塑形         ── 在 L1 载荷上加纯格式：UUID / email / dashed…（不引入新唯一性）
```

## 1.1 唯一性的"宇宙"

唯一性覆盖范围 = **一个 daemon 的生命周期**：

- `go test ./...` 每个包是一个 client 进程，经文件锁 + autostart 共享同一 daemon；包内 `t.Parallel()` 用例是同进程并发 goroutine。
- daemon 退出（IdleTTL reaper）时连容器一起回收；下一个 daemon 启动先 `CleanupOrphans`，数据从零开始。
- 因此**"计数器清零"与"数据清零"绑定同一生命周期**：新 daemon 从 0 发号，旧数据已不存在，不可能撞存量。

存储是 daemon 托管的 ephemeral 容器（testcontainers）。本项目无长期持久化需求，**不做**"持久化外部库 + epoch 播种"方案；若将来需要指向持久库，需给 daemon 计数器播种 run-epoch（会吃掉值空间预算）。

## 1.2 唯一性保证

> **实践规则（TL;DR）**：无需先吃透下文"全局 `G` vs 本形态运行量"的微妙之处，记住一条即可正确使用——
>
> - **要跨字段 / 全局唯一**：用 ① 档宽形态——`UUID()` / `UUIDNoDash()` / `UniqueHex(n)`（`n≥16`）/ `UniqueString(n, AlphaNum)`（`n≥11`）。这些无条件唯一，放心跨字段用。
> - **其余形态不要依赖其跨字段唯一**：含 10 位 uid、`UniqueInt` 等 ② 档"现实安全但非无条件"的形态——可做局部标识，但别把"全局不撞"建立在它们之上。窄串（③ 档，如 6 位码）框架**明确不保证**；让被测程序依赖短串全局唯一本身是调用方设计缺陷，请改用 ① 档形态。

**算法是确定性双射，不是哈希。** `permute(token, cap)` 是 `[0, cap)` 上的一个**排列**，不同输入必映射到不同输出。

对形态 S（值空间 `cap = |cs|^n`），只要喂给 `permute` 的 token 都 `< cap`，S 的产出两两不同。证明：token 构成全局序列的子集 `T_S`，元素两两不同（全局单调）；`permute(·, cap)` 是双射，双射限制到子集仍是单射。∎

多个形态共用一条全局序列：对 ① 档（`cap ≥ 2⁶³`）不影响其不重复性（token 恒 `< cap`）；对 `cap < 2⁶³` 的形态，则意味着其别名阈值由**全局**累计生成量、而非自身生成量决定（见三档契约）。v1 据此**不为每个形态分配独立序列**——代价是窄形态的安全边界比"自身生成量"看起来更紧。

### 为什么否决"hash 然后 mod"

`value = hash(token) mod cap` 是概率性的：10 位 uid（`cap = 10^10`）、100 万次生成，期望碰撞 ≈ `(10⁶)²/(2·10¹⁰) ≈ 50` 次。**Feistel 双射把概率压到 0**（只要 token < cap）——这正是选它而非哈希的原因。

### 三档唯一性契约

底层 token 是 int64，从 0 单调递增，上界 ≈ `2⁶³ ≈ 9.2×10¹⁸`。**关键前提：所有形态共用同一条全局 token 序列（§1.3 单一 `blockSource` / 单一 daemon 计数器）。** 因此决定某形态是否别名的，不是它自己生成了多少，而是**生成该形态时全局计数器 `G` 已走到多大**——`G` ≈ daemon 启动以来所有形态的累计生成次数。

`permute` 是真双射（不是 hash），碰撞行为是**二值阈值**而非概率：`G < cap` 时所有 token `< cap`、残差两两不同，碰撞**恰为 0**；`G ≥ cap` 时第 `cap+1` 个 token 经 `mod cap` 与某更早 token 同残差，**确定性撞车**（非小概率）。

| 档               | 条件                           | 含义                                                      | 落档示例                                                    |
| ---------------- | ------------------------------ | --------------------------------------------------------- | ----------------------------------------------------------- |
| **① 无条件唯一** | `cap ≥ 2⁶³`                    | `G` 受 int64 上界限制恒 `< cap`，碰撞**严格为 0**，无条件 | UUID(32 hex)、hex `n≥16`、alnum `n≥11`、digit `n≥19`        |
| **② 实践安全**   | `cap < 2⁶³` 且现实 `G ≪ cap`   | 全局累计生成量远小于 cap 时视为不重复；框架不做运行期排重 | 10 位 uid(`10¹⁰≈2³³`)、12 位 lower(`2⁵⁶`)、email local-part |
| **③ 有概率重复** | `cap ≲` 现实可达的 `G`（窄串） | 全局计数器越过 cap，`mod` 折叠后静默别名                  | digit `n≤6`、hex `n≤5`、短验证码                            |

> **重要 ①**：判据是**全局计数器 `G`，不是该形态自身的生成量**。一个 6 位验证码（`cap=10⁶`）即便整套测试只生成过一次，只要此前全局累计生成量已超 `10⁶`，照样确定性别名。**文档与注释中不得用"该形态运行量"论证安全。**
>
> **重要 ②**：10 位 uid 属 **②** 而非 **①**——是"全局生成量远小于 `10¹⁰` 所以现实安全"，不是"无条件唯一"。
>
> **重要 ③**：**要求全局唯一却用短串（③ 档）本身是调用方设计缺陷**，框架不为此兜底；需要全局唯一标识请用 ① 档形态（hex `n≥16` / alnum `n≥11` / UUID）。

各字符集落入 ① 档的最小长度：

| 字符集（size）     | 最小 n |
| ------------------ | ------ |
| Digit (10)         | 19     |
| Hex (16)           | 16     |
| Lower / Upper (26) | 14     |
| AlphaNum (62)      | 11     |

> **注意**：上表的 Digit `n≥19` 指 `UniqueString(n, Digit)` 这类字符串形态。`UniqueInt(digits)` 返回 `int64`，受 `int64` 可表达范围限制，合法 `digits` 为 `1..18`；需要 19 位及以上数字载荷时用 `UniqueString(n, Digit)`。

### 已否决的备选：per-kind 独立序列

曾考虑"按形态（`(cs, n)` / cap）各开一条独立 token 序列"，把别名阈值从"全局 `G` 跨 cap"改为"本形态自身生成量跨 cap"，以缩小窄形态安全边界。**否决，维持单一全局序列**，理由：

- **实际碰撞零增益**：① 档本就无条件唯一；中高 cap 段（`2³³~2⁶³`）一次 `go test` 的 daemon 寿命里全局 `G` 撑死几百万次生成，够不到那些 cap，单一全局今天也基本不撞。它唯一买到的是"按本形态计数即可推理安全"这一**心智简化**——而这用本节文档讲清即可交付，无须实现。
- **复杂度代价不值**：per-kind 要在 daemon `Allocator` 与进程侧 `blockSource` 两端都引入按 kind 的计数器表（`map[kind]`），换来的只是上述本可由文档交付的收益。
- **裸多序列另有确定性碰撞陷阱**：`permute(token, cap)` 是固定 key 的纯函数、新序列从 `token=0` 重启 → 同一形态的两条独立序列会产出**逐位相同**的流，跨字段必然相撞（"按 `(cs,n)` 派生、同配置共享一条"可绕开此坑，但既然整体不做，仅作记录）。
- **窄形态救不动**：补块按固定块大小（65536）发放，`cap` 接近或小于块大小时（③ 档窄串）单块即越过 cap、当场折叠，独立序列也无济于事——正是本节明确不保证的那段。

与 §1.9 中"spike 早期 per-shape kind / budget tripwire 已弃"的记述同源，最终设计统一为**单一全局 kind + 静默全函数**。

## 1.3 L0 区块发号（HiLo 模式）

唯一性来源是全局唯一、单调的 int64 token 序列：daemon 串行发块，进程本地消费区块内自增——每 65536 次 token 才远调一次。

### 服务端

```go
// Allocator 串行返回 [start, start+size) 半开区间，跨调用者天然不相交。
// v1 单一全局 kind；kind 字段在 HTTP 协议层预留，语义暂不分叉。
func (a *Allocator) Alloc(kind string, size int64) (start, end int64)
```

实现 = 一个 `atomic.Int64` FetchAdd：`newEnd = counter.Add(size)`，`start = newEnd - size`，无锁，跨调用者不相交。

### 进程侧（`blockSource`）

```go
type blockSource struct {
    mu    sync.Mutex
    next  int64
    end   int64
    size  int64  // 固定 65536
    kind  string // 固定 "token"
    fetch func(kind string, size int64) (start, end int64, err error)
    // fetch 须带 5s 超时；失败不重试——本机/CI 均为回环地址，daemon 不可达即基建故障。
}

func (b *blockSource) Next() (int64, error) {
    b.mu.Lock()
    defer b.mu.Unlock()
    if b.next >= b.end {       // 冷路径：每 65536 次一次远调
        s, e, err := b.fetch(b.kind, b.size)
        if err != nil { return 0, err }
        b.next, b.end = s, e
    }
    v := b.next; b.next++
    return v, nil
}
```

- **进程内单例**，所有 `CaseScope`（同进程跨用例）共享。热路径是纯本地自增、几乎无竞争；但**补块（冷路径）会持 `mu` 做一次同步 HTTP 往返（超时 5s），期间阻塞进程内所有生成**——每 65536 次才一次、且为本机回环，实践开销可忽略，但它不是“极短临界区”。
- `blockSource` 是**包内接缝**，不进公共签名；纯逻辑单测可注入 trivial fake（见 §2.4）。

## 1.4 L1 单射：Feistel FPE + base 编码

把 token 单射成"恰好 n 字符、全宽随机外观"的串，用**保形加密（FPE）标准构造：平衡 Feistel + cycle-walking**，对任意基数 / 长度可证双射。

```text
cap   = |cs|^n
split = ⌈√cap⌉                        // 值空间拆成近似两半

feistelOnce(v):                       // [0, split²) 上的一次置换
    L, R = v / split, v % split
    for i in 0..3:                     // 4 轮；任意轮数皆双射，取 4 轮为扩散
        F = SHA256(key ‖ i ‖ R) mod split
        L, R = R, (L + F) % split
    return L*split + R

permute(token):                       // [0, cap) 上的双射
    y = token mod cap                 // cap ≥ 2⁶³ 时即恒等
    loop:
        y = feistelOnce(y)
        if y < cap: return y          // cycle-walking：期望迭代 < 2 次

UniqueString = encode(permute(token), n, cs)  // base |cs| 编码，低位在右
```

轮函数用固定 key 的 keyed SHA-256，映射确定可复现。cycle-walking 期望迭代 < 2 次（`split²` 仅略大于 `cap`）。

**双射性与轮数无关**：平衡 Feistel 单轮即可逆（给定输出可逐轮逆推 `L,R`），取 4 轮是为足够的扩散 / 伪随机外观（规避下文仿射退化那类长前缀），不是双射的前提。

> **spike 教训**：早期用"仿射乘法 `y=(token·M+B) mod cap`，M 取梅森素数 2¹²⁷−1"，在 2 的幂模数下退化为 `y≈B−token`（M ≡ −1），产生 `0000…` 长前缀。压力测试当场暴露，遂改 Feistel。**单次模乘不是可靠的扩散器，勿用。**

**实现选择**：第一版统一使用 `big.Int` Feistel + cycle-walking 覆盖所有值空间，优先保证数学语义一致与可验证性。`uint64` 快路径、`cap ≥ 2⁶⁴` 的双 limb 注入等优化后续按性能证据再做；优化不得改变同一 token 到同一形态的确定性映射。

## 1.5 Charset 定义

`Charset` 是字母表——size 充当基数，符号集决定输出字符；两个同 size 的字母表（如大写 hex 与小写 hex）是不同 charset。

```go
type Charset int
const ( AlphaNum Charset = iota /* 62 */; Lower /* 26 */; Upper /* 26 */; Hex /* 16 */; Digit /* 10 */ )
```

可后续扩充（如 base32）。

## 1.6 L2 API

**心智模型**：唯一性只在第 1 层（L0→L1）产生；第 2 层只做格式塑形，不引入新唯一性。

```text
第 1 层  唯一载荷   UniqueString / UniqueHex / UniqueInt
第 2 层  塑形       UUID / UUIDNoDash / Email / Phone …
```

```go
// 第 1 层：唯一载荷，均为 CaseScope 方法，token 内部自给
func (s *CaseScope) UniqueString(n int, cs Charset) string   // 恰好 n 字符，全宽
func (s *CaseScope) UniqueHex(n int) string                  // = UniqueString(n, Hex)
func (s *CaseScope) UniqueInt(digits int) int64              // digits ∈ [1,18]；[0,10^digits) 内单射

// 第 2 层：塑形
func (s *CaseScope) UUID() string                            // RFC 4122 v4，带短线（36字符）
func (s *CaseScope) UUIDNoDash() string                      // RFC 4122 v4，无短线（32字符）
func (s *CaseScope) Email(domain ...string) string           // "fixture+{载荷}@{domain}"，domain 省略/空 → mtkit.test
func (s *CaseScope) EmailFrom(seed string) string            // seed 须为合法 email；输出 {local}+{载荷}@{domain}；非法格式 panic
func (s *CaseScope) Phone(country string) string             // E.164 语法合法；country 为 ISO 3166-1 alpha-2；初始支持 CN/US/GB/DE/FR/JP/IT/CA；不支持则 panic

// 确定性变换 / 时钟：无 token 消耗
func Sha256Hex(s string) string
func EpochSeconds() int64
func EpochMillis() int64

// 标准枚举样例：挑选非生成，可重复，值可跨用例相同
var ISOCountryCodes []string   // ISO 3166-1 alpha-2
var ISOCurrencies   []string   // ISO 4217
var IANATimeZones   []string   // IANA tz database
var BCP47Languages  []string   // BCP 47 常用集

func SampleCountry()  string
func SampleCurrency() string
func SampleTimeZone() string
func SampleLanguage() string
```

**设计要点**：

- `UUID()` 与 `UUIDNoDash()` 各消耗独立 token，产出两个不同标识符，不存在"同一标识符两种格式"语义。内部消耗 `UniqueHex(30)` + 插入固定 version/variant 位（属 ① 档，`2^120 > 2^63`）。
- **项目自有 32-hex 标识符**（如 meross 设备 ID）若不要求 v4 合规，直接用 `UniqueHex(32)`，不经 UUID 入口。
- `Email(domain ...string)` 的 `domain` 是便利入口：省略/空用默认 `mtkit.test`，固定 `fixture` 本地名只换域名；需要自定义本地名走 `EmailFrom`（`EmailFrom("fixture@"+domain)` 亦可达到同样效果，故 `domain` 不引入新能力，仅省去调用方拼 seed）。
- `Phone` 无 `From` 变体：保留可读种子的场景极少；如确需，用 `UniqueString` 在项目层自行拼接。第一版只保证 E.164 语法与国家码正确，不承诺号码符合各国真实可分配号段规则。
- `Sample*` 随机返回、可重复，不消耗 token。若需"唯一国家码序列"（罕见），用 `UniqueInt` 对 `len(ISOCountryCodes)` 取模索引。
- 项目自有格式（guid 排布、order_id 方案）在项目层用第 1 层载荷自行塑形，不进 `client/fixture`。

## 1.7 全函数契约与错误处理

**`UniqueString` 对合法参数是 total 函数：不设运行期唯一性守卫，不 fail-loud。**

合法参数：`n > 0`，`cs` 为已定义 `Charset`；`UniqueInt` 额外要求 `digits ∈ [1,18]`。非法参数是程序员错误，直接 `panic`。

合法参数下的行为：`encode(permute(token mod cap, cap))`。`cap ≥ 2⁶³` 时 `mod` 即恒等（① 档）；`cap` 较小时按 cap 折叠，超过 cap 个后**静默别名**。框架不做运行期重复检测，调用者按 §1.2 的值空间参考自行判断形态是否足够大。

不设守卫的原因：单一全局序列下，无法廉价判定某形态是否已重复（需 per-shape 计数，已否决）。触发别名的只有 ③ 档窄形态——那本就不在唯一性保证范围内。调用方据 §1.2 三档表自行决定是否加外部排重，或意识到"被测程序依赖短串全局唯一"可能是设计缺陷。

> **基建错误仍 fail-loud**：补块时 daemon 不可达等基础设施故障，由 `CaseScope.next()` 调 `t.Fatalf` 终止用例。因此生成函数签名无 `error`、无 per-call `t`：`fx.UniqueString(n, cs) string`。

## 1.8 并发与可复现性

### 并发

- **跨进程**：daemon `Allocator` 用 `atomic.Int64` FetchAdd 串行化，每个进程拿到的 `[start, end)` 不相交。
- **进程内**：`blockSource.mu` 保护游标；区块内（65535 次）是纯本地自增，锁竞争极低。

### 可复现性

两件事须区分：

- **算法是确定性的**：Feistel 用固定 key，`permute(42, cap)` 在任何机器任何次运行产出同一字符串。
- **token 分配是非确定性的**：并行用例抢号顺序由 goroutine 调度器决定，跨运行不保证同一字段拿到同一 token。

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

## 1.9 验证证据

算法由 `test/spike/unique/unique_test.go` 实测背书，可用 `go test -race -count=3 ./test/spike/unique/...` 复现：

| 测试                                     | 验证                               | 结果                            |
| ---------------------------------------- | ---------------------------------- | ------------------------------- |
| `Token_Concurrent_SingleProcess`         | 同进程跨 goroutine + 频繁补块竞态  | 128 万 token 零重复             |
| `Token_MultiProcess_GlobalUnique`        | 共享 allocator + 多进程多协程      | 128 万 token 全局零重复         |
| `Permute_IsBijection_Exhaustive`         | 置换双射穷举（hex/decimal/base62） | distinct == cap                 |
| `UniqueString_EndToEnd`                  | 完整管线多进程多协程               | 100 万串零碰撞、长度/字符集合法 |
| `UniqueHex32_UUID`                       | UUID 格式 + 32hex                  | 50 万零碰撞                     |
| `FirstCharSpread` / `AllPositionsSpread` | 全宽扩散                           | 每位 ≥8/16 符号                 |

> spike 早期含 per-shape kind 与 budget tripwire 测试，对应早期 per-shape / fail-loud 取向；最终设计已改为单一全局 kind + 静默全函数，落代码阶段统一对齐。

---

# §2 L0 接线

本章规定唯一性引擎与真实 daemon 的接线：服务端如何暴露 `Alloc` 原语、客户端如何发现 daemon 并管理本地区块缓存。

- 服务端挂 daemon 主端口，复用 `withAuth` 鉴权，**无需新端口、无需额外配置**。
- 客户端发现与 `client/fake` 一致：调用方读 `mtkit.runtime.json`，显式传 `addr+token`。
- 进程级 `blockSource` 单例隐藏在 `Client` 内部；`CaseScope` per-test，只暴露生成函数。

## 2.1 服务端（`fixtured` 包）

### HTTP 接口

```
POST /v1/fixture/alloc
Authorization: X-Mtkit-Token: <token>
Content-Type: application/json

请求体：{"kind": "token", "size": 65536}
成功响应：{"start": 0, "end": 65536}
错误响应：{"code": "invalid_size", "message": "size must be positive"}
```

路由前缀 `/v1/fixture/`，与 lease（`/v1/`）和 faked（`/v1/grpc/`、`/v1/http/`）不冲突。鉴权复用 daemon `withAuth`（`X-Mtkit-Token` 对比 `cfg.Token`）。v1 只有 `"token"` 一个 kind（常量 `protocol.KindToken`）；kind 字段为后续扩展预留。

**错误信封**：fixture 控制面与 daemon lease/faked 统一用 `{code,message}`（`fixtured/protocol.ErrorResp` 与 `orchestrator/protocol.ErrorResp` JSON 兼容），客户端可用同一结构解析 fixture handler 与 daemon 鉴权失败两类错误。**size 上界**：除 `size > 0` 外另设 `maxAllocSize = 1<<20`，超限返回 `invalid_size`，防异常请求吃掉 token 值空间。

### Allocator 与 Server

```go
type Allocator struct {
    counter atomic.Int64
}

// Alloc 串行返回 [start, start+size) 半开区间，跨调用者天然不相交。
func (a *Allocator) Alloc(kind string, size int64) (start, end int64)

// Server 由 daemon 在启动时无条件创建（无 enable flag）。
type Server struct{ alloc *Allocator }

func New() *Server

func (s *Server) RegisterRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
    mux.HandleFunc("/v1/fixture/alloc", wrap(s.handleAlloc))
}
```

### 挂载点

```go
// orchestrator/lifecycle.go — Daemon.New()
d.fixtured = fixtured.New()

// orchestrator/server.go — Start() mux 注册段
d.fixtured.RegisterRoutes(mux, d.withAuth)
```

## 2.2 客户端（`client/fixture` 包）

### 分层

```text
CaseScope          (per-test)    — t + src；暴露 UniqueString / UUID 等
    └── blockSource (进程级单例) — mu + next/end；热路径本地自增，冷路径补块
            └── HTTP client (thin) — POST /v1/fixture/alloc
```

### 内部接口与 CaseScope

```go
// iTokenSource 仅包内使用，不进任何公共签名。
type iTokenSource interface {
    Next() (int64, error)  // error 仅在基建故障时非 nil
}

type CaseScope struct {
    t   testing.TB
    src iTokenSource
}

// next 是所有生成函数的底层出口；基建故障通过 t.Fatalf 终止用例。
func (s *CaseScope) next() int64 {
    v, err := s.src.Next()
    if err != nil { s.t.Fatalf("fixture: daemon unreachable: %v", err) }
    return v
}

func (s *CaseScope) cleanup() {}  // t.Cleanup 回调，当前 no-op，按需填充

// 第 1 层
func (s *CaseScope) UniqueString(n int, cs Charset) string
func (s *CaseScope) UniqueHex(n int) string   { return s.UniqueString(n, Hex) }
func (s *CaseScope) UniqueInt(digits int) int64 // digits ∈ [1,18]

// 第 2 层
func (s *CaseScope) UUID() string
func (s *CaseScope) UUIDNoDash() string
func (s *CaseScope) Email(domain ...string) string
func (s *CaseScope) EmailFrom(seed string) string
func (s *CaseScope) Phone(country string) string
```

## 2.3 进程级 Client 与发现

```go
// Client 进程级对象，持有 blockSource 单例，通常按测试包 lazy 初始化一次。
type Client struct{ src *blockSource }

// New 创建进程级 fixture client。addr、token 来自 RuntimeInfo.Addr / .Token。
func New(addr, token string) *Client

// NewScope 为单个测试用例创建 CaseScope。
func (c *Client) NewScope(t testing.TB) *CaseScope {
    s := &CaseScope{t: t, src: c.src}
    t.Cleanup(s.cleanup)
    return s
}
```

发现模式与 `client/fake` 完全一致：调用方自行读取 `mtkit.runtime.json` 拿到 `RuntimeInfo.Addr` / `.Token`，显式传入 `fixture.New`，无自动发现、无隐式全局状态。

**生命周期注意**：当前 mtkit 实现中，daemon 自启、Acquire、runtime 写入都发生在 `mtkit.Run(m)` 内部，且 `Run` 拿到 lease 后才调用 `m.Run()`。因此普通 `TestMain` 不能在 `mtkit.Run(m)` 之前读取 runtime 来创建 fixture client；应在测试函数执行期（或包级 helper 的 `sync.Once` lazy 初始化）读取 runtime。

```go
var (
    runtimePath string
    fxOnce      sync.Once
    fxClient    *fixture.Client
    fxErr       error
)

func TestMain(m *testing.M) {
    kit, err := mtkit.New(project, cfg)
    if err != nil {
        log.Fatal(err)
    }
    runtimePath = kit.RuntimePath()
    os.Exit(kit.
        RegisterContainers(myContainers).
        RegisterSUT(mySUT).
        Run(m))
}

func fxScope(t testing.TB) *fixture.CaseScope {
    t.Helper()
    fxOnce.Do(func() {
        ri, err := mtruntime.Read(runtimePath)
        if err != nil {
            fxErr = err
            return
        }
        fxClient = fixture.New(ri.Addr, ri.Token)
    })
    if fxErr != nil {
        t.Fatalf("fixture runtime unavailable: %v", fxErr)
    }
    return fxClient.NewScope(t)
}

func TestCreateDevice(t *testing.T) {
    scope := fxScope(t)
    deviceUUID := scope.UUID()
    // ...
}
```

## 2.4 单测隔离接缝

`iTokenSource` 包内接口允许纯逻辑单测绕过 HTTP 层注入 trivial fake：

```go
type seqSource struct{ n int64 }
func (s *seqSource) Next() (int64, error) { v := s.n; s.n++; return v, nil }

// newScopeWith 包内构造函数，供单测注入 source，不暴露给公共 API。
func newScopeWith(t testing.TB, src iTokenSource) *CaseScope {
    return &CaseScope{t: t, src: src}
}
```

---

# §3 Provision

Provision 解决的核心问题：**测试前置数据（建表、种子数据）在多个测试进程并发或先后运行时不冲突、不重复执行**。

三个核心组件：`Resource` 声明要建什么；`Registry` 提供分布式互斥 + 已应用账本；`PersistEnv` 提供进程级连接池。

**包归属**：provision 的全部类型与实现归 **`client/fixture`** 包——它们都在**测试（client）进程**内执行（`PersistEnv` 连真实 DB、`Provision` 跑在 client 模式的 `provisionRunnable` 里），与 daemon 无关；daemon 在 provision 中唯一职责是启动时重置账本（§3.3）。因此：

- `client/fixture`（仅测试侧）：CaseScope / L1 / L2 生成 API **＋** `Resource` / `Provision` / `Registry` / `FileRegistry` / `PersistEnv` / 文件锁。
- `fixtured`（仅 daemon）：只剩 `Server` / `Allocator`，不含任何测试侧代码。
- `fixtured/protocol`（leaf）：两侧共享的 wire 契约（请求 / 响应、路径常量）**＋**账本文件名常量；`fixtured`（Server）与 `client/fixture`（HTTP client、账本路径）各自 import 它，互不 import 对方逻辑包，无环。

`mtkit.WithSchema` 接收 `[]fixture.Resource`，门面 import `client/fixture`——无环且无害（早期"避免门面依赖消费端"的顾虑不成立）。**原则：daemon 逻辑包与测试侧逻辑包互不 import，唯一共享物是 `fixtured/protocol` leaf。**

## 3.1 Resource

```go
// Resource 表示一个可 provision 的基础设施资源（表、索引、命名空间、种子数据等）。
// Apply 在 Registry 互斥内、且 key 未注册时被调用；多条写入须用事务包装，保证重试安全。
type Resource interface {
    Key() string
    Apply(ctx context.Context, env *PersistEnv) error
}
```

- `Key()` 推荐命名模式：`"storage.schema.object"`，如 `"mysql.mssiot_user.users"`。
- `Key()` 的返回值**不能变**——它是账本的持久化标识，一旦改变，旧条目永远不会被命中。

## 3.2 Registry

```go
// Registry 提供跨进程幂等执行保证。v1 实现为文本账本（§3.3）。
type Registry interface {
    // EnsureOnce 在互斥保护下幂等执行 fn。
    // skipped=true：key 已注册，fn 未执行。
    // err!=nil：fn 失败或基建故障；key 不被标记，下次可重入。
    // ctx 控制等锁超时；fn 内部应自行处理自身的超时。
    EnsureOnce(ctx context.Context, key string, fn func() error) (skipped bool, err error)
}
```

接口只有一个方法——早期草案拆为 `WithLock` + `Applied` + `MarkApplied` 三段，"检查 + 执行 + 标记"的原子性因此分散到调用方，每个后端都要在接口缝隙里重建原子性。`EnsureOnce` 把原子性下推给后端实现。详见 ADR-0001。

`skipped` 不走 `error` channel：幂等跳过是正常路径，哨兵错误会让所有调用方都过滤"非错误的错误"，容易漏掉。

## 3.3 v1 实现：FileRegistry（文本账本）

**账本文件**：`{RuntimeDir}/mtkit.provision.json`，格式 `{"applied":["key1","key2"]}`。

**为什么不用 MySQL**：Registry 必须在任何资源被 provision 之前可用；MySQL 后端要求 MySQL 连接，造成"用 MySQL 来 provision MySQL"的循环依赖。文本账本无任何基础设施依赖，任何资源类型均可 provision。详见 ADR-0001。

**生命周期**：daemon 在 `Start()` 时写入 `{"applied":[]}`，与 daemon/容器生命周期严格绑定——daemon 重启则文件重置，账本与数据同步清零，不会出现"账本说已 applied 但表不存在"的状态。

**重要（时序约束）**：账本截断必须 **happens-before daemon 对外可发现**（绑定监听端口 / 写 `mtkit.runtime.json`）。否则存在竞态：新 daemon（新容器=空数据）若先写 runtime 让 client 拿到租约、再截断账本，client 可能 `EnsureOnce` 读到**上一代 daemon 残留账本**，误判已 applied 而跳过建表，导致新容器里表不存在——正是本机制要消灭的状态。实现上 daemon `Start()` 须在绑定端口 / 写 runtime 之前同步完成账本写空。

**互斥**：整文件一把锁（Linux/Mac `flock`，Windows `LockFileEx`），`EnsureOnce` 持锁期间执行完整的"读 → 检查 → 执行 → 写"流程。**注意：持锁贯穿 `fn`（即 `Apply`），临界区时长 = 最慢一次 `Apply`（建表 / 灌种子，可能含网络 DDL），且整文件一把锁会让所有进程、所有 key 串行化。** 这是为保证"检查 + 执行 + 标记"原子性而有意为之（否则两进程会同时建表）；provision 低频、DDL 用 `IF NOT EXISTS`，串行成本可接受，但不要理解为"窗口极短"。

**等锁超时**：ctx 取消则立即返回 `ctx.Err()`，fn 不执行，key 不被标记。未设截止时间时默认 **30s**——provision 在启动阶段，进程卡死的危害远大于超时报错；超时后 error 向上传播，`log.Fatal` 终止该测试包。

**路径发现**：`protocol.LedgerPath(runtimePath)`（= 与 runtime 同目录下的 `protocol.LedgerFileName`），daemon 与 client 共用此函数，无需修改 `RuntimeInfo`。

```go
type FileRegistry struct { /* ... */ }

// NewFileRegistry 创建文本账本 Registry。ledgerPath 由调用方从 runtimePath 推算。
func NewFileRegistry(ledgerPath string) *FileRegistry
```

## 3.4 PersistEnv

`PersistEnv` 是**进程级连接池**，按 `sutEnvKey` 懒建并缓存连接。它有**两类消费者**：provision 期的 `Resource.Apply`（框架内部，§3.7），以及测试期直接写/读测试数据的测试代码（调用方）。两者**各持一个实例**——不是冗余，而是被生命周期逼出来的：runtime 只有 `Run` 启动后才可读（§2.3），provision 实例在 `Run` 内、`m.Run()` 之前建，测试实例只能在 `m.Run()` 期间 lazy 建。两个窗口错开，跨窗口共享须引入进程全局，违背 §2.3 的无隐式全局原则，故各建各的（多出的一个空闲池可忽略）。

**不改 `RuntimeInfo` 结构，不改 `ContainerRegistration`**——`sutEnvKey` 经 `RuntimeInfo.SUTEnv`（已有字段）传递，无需新增字段。

```go
// PersistEnv 提供测试基础设施连接，供 Resource.Apply 与测试侧数据写入共用。
// 连接懒初始化，按 sutEnvKey 在进程内缓存复用。
// sutEnvKey = sut_env map 的 key，如 "MYSQL_DSN"，不是容器名或数据库名。
//
// 刻意用**具体 struct 而非 interface**：两类消费者（Resource.Apply、测试侧写数据）都是真 I/O，
// 没有可独立单测的纯逻辑，丢掉接口替换缝无实际损失；换来的是新增后端只需**加方法**，纯增量——
// 不破坏任何调用方，也没有 mock 实现要同步更新。
type PersistEnv struct { /* sutEnv + 懒连接缓存（mu 保护，串行化并发首连） */ }

// 单角色后端：一个 key 一个 client，同时服务 DDL 与数据读写。MySQL/TDengine 均走 database/sql。
func (e *PersistEnv) MySQL(sutEnvKey string) *sql.DB    // 驱动 mysql
func (e *PersistEnv) TDengine(sutEnvKey string) *sql.DB // 驱动 taosWS（websocket，纯 Go 无 cgo）
func (e *PersistEnv) Redis(sutEnvKey string) redis.UniversalClient
func (e *PersistEnv) Mongo(sutEnvKey string) *mongo.Client

// 多角色后端：provision（admin）与数据读写（client）是两套独立 client、两个端点、两个 key，
// 故拆成两个平铺方法，各取各的 key——不假装聚合成一个入口（见“多角色后端”小节）。
// admin.Client 是 pulsaradmin/pkg/admin 的接口；连通性不在构造期探测（SDK 无廉价 ping）。
func (e *PersistEnv) PulsarAdmin(sutEnvKey string) admin.Client    // http://host:8080
func (e *PersistEnv) PulsarClient(sutEnvKey string) pulsar.Client  // pulsar://host:6650
```

`sutEnvKey` 来自 `RuntimeInfo.SUTEnv`（`mtkit.runtime.json` 的 `sut_env` 字段）。多容器、多数据库靠不同 key 区分：

```text
MYSQL_DSN          →  mysql 容器，mssiot_user 库
MYSQL_DEVICE_DSN   →  mysql-device 容器，mssiot_device 库
TDENGINE_DSN       →  tdengine 容器（taosWS：user:pass@ws(host:6041)/db）
REDIS_ADDR         →  redis 容器
PULSAR_ADMIN_URL   →  pulsar 容器，admin REST 端点（:8080）
PULSAR_BROKER_URL  →  pulsar 容器，broker 端点（pulsar://，:6650）
```

连接失败（key 不存在、DSN 有误、连接超时）→ `panic`（与 §1.7 基建故障处理一致）。

```go
// NewPersistEnv 创建进程级连接池；sutEnv 即 RuntimeInfo.SUTEnv。连接懒初始化。
func NewPersistEnv(sutEnv map[string]string) *PersistEnv
```

### 使用契约

框架不写测试侧代码，故下表 B 组靠使用说明约束，框架无法强制。

**A. 框架保证（使用者可依赖）**

| 保证 | 含义 |
| ---- | ---- |
| 进程级缓存 | 同一 `PersistEnv` 内，相同 `sutEnvKey` 的连接只建一次，后续命中缓存 |
| 并发安全 | 懒连接被 `mu` 串行化；返回的 client 自身 goroutine-safe，可被 `t.Parallel()` 用例自由共享 |
| fail-loud | 连接失败直接 `panic`（§1.7），不返回可处理的 error |
| 懒连接 | 没用到的后端不建连 |

**B. 使用者必须遵守（框架无法强制）**

| 约束 | 为什么 | 正确做法 |
| ---- | ---- | ---- |
| **每进程只建一个 `PersistEnv`** | 建 N 个 = N 份缓存 = N 套池，复用落空——这是复用的唯一前提 | 包级 `sync.Once` 持单例，所有用例/helper 共享 |
| **lazy 在 `m.Run()` 期间建，不在 `mtkit.Run()` 之前** | runtime 只有 `Run` 启动后才可读（同 `fxClient` 根因，§2.3/§3.7） | `sync.Once` 里 `mtruntime.Read` → `NewPersistEnv(ri.SUTEnv)`，由测试函数/helper 触发 |
| **不在用例 cleanup 里 Close** | 池是进程级共享物，一个用例关了会拖垮其他并发用例 | 不显式关，进程退出回收 |
| **裸 `sutEnvKey` 不进测试体** | 裸 key 散落测试现场是反模式，且 key 名是注册方↔消费方的耦合点 | 用共享常量；测试现场只调项目语义 helper（如 `infra.PulsarProducer()`） |

### 多角色后端（以 Pulsar 为例）

判据：**一个后端有 >1 个语义不同的 client 角色时，平铺成多个方法，各取各的 key；单角色后端维持一个方法。** 不为统一而把单角色也包成链式（过度设计），也不把 admin/client 硬塞进一个假聚合入口——它们端点、key、SDK 全不同，无共享上下文。

两个 URL 由**同一个 Pulsar 容器的 `SUTEnv` 回调（启动后执行）一并吐出两个 key**：mapped port 启动前不可知，故在启动后回调里组装。

```go
// 注册代码（调用方）。keyPulsarAdmin / keyPulsarBroker 是注册方与 helper 共享的常量，
// 不在两处各写字面量——否则一边改名、另一边运行时才 panic 暴露。
container.ContainerRegistration{
    Name:  "pulsar",
    Start: startPulsar,
    SUTEnv: func(ctx context.Context, c testcontainers.Container) (map[string]string, error) {
        host, err := c.Host(ctx);                     if err != nil { return nil, err }
        admin, err := c.MappedPort(ctx, "8080/tcp");  if err != nil { return nil, err }
        broker, err := c.MappedPort(ctx, "6650/tcp"); if err != nil { return nil, err }
        return map[string]string{
            keyPulsarAdmin:  fmt.Sprintf("http://%s:%s", host, admin.Port()),
            keyPulsarBroker: fmt.Sprintf("pulsar://%s:%s", host, broker.Port()),
        }, nil
    },
}

// 项目 helper（调用方）：裸 key 只活在这里和上面的注册回调，测试现场只见语义名。
func PulsarAdmin()    *pulsaradmin.Client { return env().PulsarAdmin(keyPulsarAdmin) }
func PulsarProducer() pulsar.Client       { return env().PulsarClient(keyPulsarBroker) }
```

**一致性契约**：两个 key 是 `SUTEnv` map 里相互独立的两条，框架无法保证它们指向同一个 Pulsar——这份一致性与任何单个 DSN 是否写对一样，是**注册代码作者的责任**；缺失/写错则首次用到时由懒连接 fail-loud `panic`。

### v1 实现范围

- MySQL：`database/sql` + `github.com/go-sql-driver/mysql`，返回 `*sql.DB`，DSN 直接取 `RuntimeInfo.SUTEnv[sutEnvKey]`。
- Redis：`github.com/redis/go-redis/v9`，返回 `redis.UniversalClient`；`mssiot_user` 同时遗留 `github.com/go-redis/redis/v8`，但 mtkit v1 统一使用 v9，除非后续出现必须兼容 v8 的资源声明。
- Mongo：`go.mongodb.org/mongo-driver/mongo`，返回 `*mongo.Client`，URI 直接取 `RuntimeInfo.SUTEnv[sutEnvKey]`。
- `RedisNS` 是文档性资源，`Apply` 为 no-op。

已实现后端（与三大件最常并用）：

- Pulsar（`github.com/apache/pulsar-client-go` v0.19.0）：`PulsarAdmin`（`pulsaradmin/pkg/admin`，建 tenant/namespace/topic）+ `PulsarClient`（`pulsar`，produce/consume 测试数据），双 key 见上。两者构造期不探连通性（SDK 无廉价 ping），broker/admin 不可达到首次调用才暴露。
- TDengine（`github.com/taosdata/driver-go/v3` v3.8.0）：**websocket 驱动 `taosWS`**（纯 Go、无 cgo，testcontainers + Windows/CI 友好），挂 `database/sql` 返回 `*sql.DB`，与 MySQL 同形；provision 建 database/STable 走 DDL。DSN 形如 `user:pass@ws(host:port)/db`。

## 3.5 Resource 工厂

```go
// MySQLTable 创建"建表"类 Resource。
// ddl 建议含 IF NOT EXISTS，便于 Apply 手动重试。
func MySQLTable(sutEnvKey, key, ddl string) Resource

// RedisNS 声明 Redis 命名空间（v1 Apply 为 no-op，文档性资源）。
func RedisNS(sutEnvKey, key, ns string) Resource
```

稳定 Resource 变量放在 `test/fixture/resources/` 共享包中：

```go
// test/fixture/resources/mysql.go
var UserTable = fixture.MySQLTable(
    "MYSQL_DSN",
    "mysql.mssiot_user.users",
    `CREATE TABLE IF NOT EXISTS users (uid BIGINT PRIMARY KEY, ...) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
)
```

## 3.6 注册机制

**两层分工**：共享层（`test/fixture/resources/`）负责稳定 Resource 定义，Key 不变，跨测试包复用；本地层（`test/infra/`）的 `requirements()` 决定本包实际 provision 哪些资源。

```
# 调用方项目目录结构（不在 mtkit 仓库内）

test/fixture/resources/        ← 共享稳定 Resource 定义
    mysql.go                   → var UserTable, DeviceTable, ...
    redis.go                   → var TokenCache, ...

test/infra/                    ← 集成测试包
    setup_test.go              → TestMain
    requirements_test.go       → func requirements() []fixture.Resource
```

```go
// requirements_test.go
func requirements() []fixture.Resource {
    return []fixture.Resource{resources.UserTable, resources.TokenCache}
}
```

## 3.7 Provision 与 TestMain 接入

```go
// Provision 遍历 resources，对每个资源调 EnsureOnce。
// 同一 daemon 生命周期内，同一 key 只 Apply 一次（跨进程安全）。
func Provision(ctx context.Context, reg Registry, env *PersistEnv, resources []Resource) error {
    for _, r := range resources {
        if _, err := reg.EnsureOnce(ctx, r.Key(), func() error {
            return r.Apply(ctx, env)
        }); err != nil {
            return fmt.Errorf("provision %s: %w", r.Key(), err)
        }
    }
    return nil
}
```

Apply 失败返回带 key 的错误，由调用方 `log.Fatal` 终止。`EnsureOnce` 提供 run-once 保证，Apply 本身无需幂等，但多条写入须用事务包装（失败回滚，key 未标记，可重入）。

`WithSchema` 在 `mtkit.MTKit` 上的签名与 `Run` 内部接线骨架：

```go
// WithSchema 注册需要 provision 的资源列表，多次调用追加。
func (t *MTKit) WithSchema(resources []fixture.Resource) *MTKit

// Run 内部，通过包装 Runnable 在 client-mode Acquire 成功后、m.Run() 前执行。
if len(t.schemaResources) > 0 {
    ledgerPath := filepath.Join(filepath.Dir(cfg.RuntimePath), "mtkit.provision.json")
    ri, err := mtruntime.Read(cfg.RuntimePath)
    if err != nil {
        log.Fatalf("read mtkit runtime for provision: %v", err)
    }
    reg := fixture.NewFileRegistry(ledgerPath)
    env := fixture.NewPersistEnv(ri.SUTEnv)
    if err := fixture.Provision(ctx, reg, env, t.schemaResources); err != nil {
        log.Fatalf("provision failed: %v", err)
    }
}
```

`Provision` 失败直接 `log.Fatal`，不进入 `m.Run()`——schema 不就绪则测试无意义。

TestMain 完整示例：

```go
func TestMain(m *testing.M) {
    kit, err := mtkit.New(project, cfg)
    if err != nil {
        log.Fatal(err)
    }
    runtimePath = kit.RuntimePath()
    os.Exit(kit.
        RegisterContainers(myContainers).
        RegisterSUT(mySUT).
        WithSchema(requirements()).
        Run(m))
}

func fxScope(t testing.TB) *fixture.CaseScope {
    t.Helper()
    // 与 §2.3 相同：测试执行期 lazy 读取 runtime，再创建 fixture client。
}
```
