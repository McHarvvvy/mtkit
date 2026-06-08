# Fixture 实现方案（落地）

> **【命名映射】** 总模块 `mtkit`（daemon = `mtkitd`）。本文旧名按此读：`fixturebase` → 客户端包 **`client/fixture`**（包名 `fixture`，在 `client/` 下）；协调内核 `coord` → 服务端 **`fixtured`**（与 lease 共用 daemon 主端口、非独立服务）。权威以 `mtkit-migration-plan.md` 为准。

本文讲 **fixture 怎么写代码**：包布局、依赖接缝、各层接口签名、落地顺序、自测策略。设计依据见 `fixture-design.md`（doc B）、`test-harness-design.md`（doc A）。读者：实现者。

核心策略一句话：**基础设施优先、依赖顺序自底向上**——先拆 `mtkit`（由 `testcontainerd` 演进而来）、把中心 daemon 立起来，再做进程监督 + faked 集成，最后在已就绪的 daemon 上建 fixtured 控制原语与 client/fixture。**不搭 inproc 桩**（顺序保证写 client/fixture 时真 fixtured 已在，桩纯属多余 + 还要回头重接）；client/fixture 直接落 umbrella 模块，不走"本地过渡再上浮"。

---

## 1. 包布局与物理位置

`client/fixture` **直接落 umbrella 模块**的 `client/` 下（客户端区），与服务端的 `fixtured` 分属两端；项目侧 `test/fixture/*` 在本仓库提供内容。因为开发顺序先拆分、daemon 先就绪（§4），不存在"本地过渡再上浮"的必要。

```text
# umbrella 模块（通用机制）
client/fixture/
├── token.go       # tokenSource 内部接口（非公共 API；唯一实现=fixtured client，测试用 trivial fake）
├── primitive.go   # L1 实现：UniqueInt / UniqueString / UniqueHex（作 CaseScope 方法暴露）
├── format.go      # L2：Email / Phone / UUID（CaseScope 方法）+ Sha256Hex / Epoch（自由函数）
├── enums.go       # 标准枚举样例集
├── scope.go       # CaseScope（生成器方法 + 自动追踪 + AssertBound/BindToScope）+ RegisterPrimary + Gen
├── resource.go    # Requirement / Resource / Registry 接口 + provision 驱动
└── persist.go     # PersistEnv：runtime 发现 + 连接底座

# 本仓库 mssiot_user（项目专属内容）
test/fixture/
├── scope/         # RegisterPrimary（uid 等）的注册
├── schema/        # Requirement 声明函数（显式导出）
├── seeders/       # SeedUser / recipe（命名、插表、派生接线、生成器调用）
├── fake/          # 注册接缝 helper（AssertBound + faked 链，可选）
├── request/       # HTTP/gRPC 请求 helper
└── observe/       # 反向读 helper
```

---

## 2. 依赖接缝（让 base 不硬阻塞于 daemon）

`base/` 通过这 3 个接缝触达外部世界。开发顺序保证它们的真实后端（fixtured / runtime）在写 client/fixture 时已就绪，故**不搭 inproc 后端**；接口存在主要是为**纯逻辑单测**留隔离点（测试里写 trivial fake，见 §5）。

```go
// (1) 唯一性来源：L0。区块发号。【内部接缝，不出现在任何公共签名——见 §3】
type tokenSource interface {
    next(kind string) int64        // 单调、跨进程唯一
}
// 唯一生产实现：fixtured client 的 AllocIDBlock（doc A §4.3）；单测用 trivial fake 喂确定序列

// (2) 资源账本 + 锁：provision 去重。
type Registry interface {
    WithLock(ctx context.Context, key string, fn func() error) error  // 临界区
    Applied(key string) bool
    MarkApplied(key, checksum string)
}
// 唯一生产实现：fixtured client（daemon 内存后端，doc A §4.3）

// (3) 持久化环境：seeder/resource 直连写数据的连接来源。
type PersistEnv interface {
    MySQL(db string) *sql.DB
    Redis() redis.UniversalClient
    Mongo() *mongo.Client
}
// 实现：从 runtime 文件解析地址构造（参照 faked client 的 runtime 发现）
```

- `tokenSource` 是**内部接缝**（小写、不导出）：`CaseScope` 内部持有它，但**它不进任何公共 API**——生成器都做成 `CaseScope` 方法、唯一性内部自给（doc B §3.2）。生产唯一实现是 fixtured client；接口只为纯逻辑单测留 fake 点。
- **没有 `FakeBinder` 接缝**：隔离守卫不是可替换后端（只有"进程内 set"一种实现），且 base 不该依赖 faked client。绑定集就是 `CaseScope` 的普通状态（自动追踪生成值）；守卫 `AssertBound` 在**项目注册接缝**调用（doc B §4）。
- `PersistEnv` 后端只需 runtime 文件（faked 已有同款发现逻辑可参照）。

---

## 3. 各层接口签名

> 总原则：**唯一生成 = CaseScope 方法**（token 内部自给、无感、自动追踪）；**确定性变换/时钟 = 自由函数**（无唯一性诉求）。raw token 不出现在任何公共签名（含 shaper）。

### 3.1 L1 基础技术原语（`primitive.go`，作 CaseScope 方法暴露）

```go
type Charset int
const ( AlphaNum Charset = iota; Lower; Hex; Digit )

func (s *CaseScope) UniqueInt(digits int) int64            // 内部 token 单射进 digits 位十进制空间
func (s *CaseScope) UniqueString(n int, cs Charset) string
func (s *CaseScope) UniqueHex(n int) string
```

- 内部从 `s.tok.next(...)` 取 token、对其单射 → 输出唯一；调用者不见 token。生成值自动登记进本 case（喂 §3.4 守卫）。
- `digits` 不足以容纳 token 时返回错误/panic（紧格式预算，doc B §7）。

### 3.2 L2 公认数据类型（`format.go`）

```go
// 唯一生成 → CaseScope 方法
func (s *CaseScope) Email() string ; func (s *CaseScope) EmailFrom(seed string) string  // → alice+<内部唯一>@x.com
func (s *CaseScope) Phone(country string) string ; func (s *CaseScope) PhoneFrom(country, seed string) string
func (s *CaseScope) UUID() string ; func (s *CaseScope) UUIDDashed() string              // 32 hex / 8-4-4-4-12

// 确定性变换 / 时钟 → 自由函数（无 token、无追踪）
func Sha256Hex(s string) string                            // 派生字段用，doc B §5
func EpochSeconds() int64 ; func EpochMillis() int64
```

- 生成型对内部 token 单射、自动追踪；`Sha256Hex` 是确定性变换。初始锁这 5 类，后续可加（doc B §3.2）。

### 3.3 标准枚举样例集（`enums.go`）

```go
var ISOCountryCodes, ISOCurrencies, IANATimeZones, BCP47Languages []string
func SampleCountry() string                                // 随机挑一个合法值（可重复、非唯一、无 token）
```

挑选非生成；项目枚举（vendor/status）不在此（doc B §3.3）。

### 3.4 CaseScope（`scope.go`）

```go
type CaseScope struct { /* 内部持 tokenSource + 已生成/已绑定值集 + 本 case 的 primary 值 */ }

func New(t *testing.T) *CaseScope                          // 内部取 token、经已注册 shape 塑形出 primary 值
func (s *CaseScope) Primary() any
func (s *CaseScope) AnotherPrimary() any
// L1/L2 唯一生成器即 s 的方法（§3.1/§3.2），产出值自动登记
func (s *CaseScope) BindToScope(field, value string)      // 仅登记"外来"唯一值（SUT 生成读回）
func (s *CaseScope) AssertBound(values ...string)          // 守卫：match 键须含 ≥1 本 case 唯一值，否则 panic

// shaper 只见生成器句柄 Gen（暴露 UniqueInt/UUID... 同名方法），不见 raw token
type Gen interface { UniqueInt(digits int) int64; UniqueString(n int, cs Charset) string; UUIDDashed() string /* ... */ }
func RegisterPrimary(name string, shape func(g Gen) any)   // 项目侧 init 调用
```

- `New` 不固定字段名；`shape` 由项目用 `Gen` 拼（`uid := g.UniqueInt(10)` 等）；**raw token 全程不暴露**。
- `CaseScope` 只"发号+改名+追踪/守卫"，不持业务实体（框架文档 §7.2）。

### 3.5 Requirement / Resource / provision 驱动（`resource.go`）

```go
type Env interface{ PersistEnv }                           // Apply 时拿连接
type Resource interface {
    Key() string
    Checksum() string                                       // 可选，复用期 staleness 告警（doc A §4.3）
    Apply(ctx context.Context, env Env) error               // 幂等
}
type Requirement interface{ Resources() []Resource }
func Requirements(...Requirement) Requirement               // 组合 + 按 Key 去重

// provision 驱动：TestMain 期调用一次
func Provision(ctx context.Context, reg Registry, env Env, req Requirement) error
// 内部：去重 → 对每个 Resource：reg.WithLock(Key) { if !Applied: Apply; MarkApplied }
```

项目侧 `schema.NotifyStore()` 等返回 `Requirement`；`bootstrap.Run` 调 `Provision`（doc B §2）。

---

## 4. 落地顺序（基础设施优先，自底向上）

三大阶段，对齐 doc A 演进；每阶段在动手前先验证其最高风险假设。

**阶段 1：拆分 `mtkit`（容器控制 vs 中心 daemon）**

- **先 spike 验证可扩展性**（doc A §11 待核实）：daemon 进程内能否额外跑一个 HTTP listener、监督 SUT 之外的子进程、提供非 re-exec 的常驻 `Serve()`。证实再动大手术。
- 把"容器管理"从"daemon shell / 编排"中拆出（doc A §3 的 `container/` vs `orchestrator/`）；fixtured 仅留空壳（HTTP listener 挂载点），原语在阶段 3 填。

**阶段 2：进程监督泛化 + faked 子进程集成**

- 把现有"单 SUT 托管"泛化为 `ManagedProcess`（doc A §4.1）：`Build/Start/Ready/Stop` + 依赖序 + 可插拔就绪（SUT 走 TCP 探活、faked 走 runtime 文件落盘）。
- faked 从"测试进程 fork"改为"daemon 监督的 `cmd/faked` 子进程"（单例、修 runtime.json 竞争 bug）；接入 reload。

**阶段 3：fixtured 控制原语 + client/fixture（直接在 umbrella）+ 项目侧打样**

| 步 | 内容 | 依赖 |
|---|---|---|
| 3a | fixtured 原语：`AllocIDBlock` / `WithLock` / registry（daemon 内存）+ thin fixtured client | 阶段 1 daemon shell |
| 3b | `tokenSource`(内部) / `Registry` / `PersistEnv` 接缝 + fixtured/runtime 实现 | 3a |
| 3c | L1 `primitive.go`（CaseScope 生成方法）| 3b |
| 3d | L2 `format.go` + `enums.go` | 3c |
| 3e | `CaseScope`（生成器方法 + 自动追踪 + `AssertBound`/`BindToScope`）+ `RegisterPrimary`/`Gen` | 3b–3d |
| 3f | `Resource`/`Requirement`/`Provision` | 3b（Registry/Env） |
| 3g | 项目侧 `scope/schema/seeders/request/observe` + 注册接缝 `fx.AssertBound`，以 **notify 域**打样端到端 | 3a–3f + faked（阶段 2） |

**3c/3d/3e/3f 是纯逻辑**，可对 trivial fake 跑充分单测（§5），不必每次起 daemon；3g 端到端跑真 daemon + 容器 + faked。

---

## 5. client/fixture 自测策略

- **L1**：唯一性（大量 token → 输出无重复）、单射（不同 token → 不同输出）、格式（位长/字符集约束）。
- **L2**：用标准库校验合法性——`net/mail.ParseAddress`(email)、`uuid.Parse`、E.164 正则、`Sha256Hex` 对拍已知向量；并发取 N 个断言唯一。
- **CaseScope**：注册一个测试 `shape(Gen)`，断言 `Primary`/`AnotherPrimary` 不同；生成值自动登记后 `AssertBound` 通过、仅全局常量时 `AssertBound` panic；`BindToScope` 的外来值也被守卫接受。
- **Provision**：用 trivial fake Registry + fake/容器 Env，断言去重（重复 Key 只 Apply 一次）、并发安全（多 goroutine 抢同 Key 只建一次）。
- 纯逻辑测试用**测试内 trivial fake**（实现 `tokenSource`/`Registry` 内部接口、喂确定序列），不起 daemon；真实跨进程行为由阶段 3g 端到端覆盖。

---

## 6. 待决 / 风险

- **阶段 1 spike 是总前提**：若 `mtkit` 的 daemon 扩不动（多子进程监督 / 内嵌 listener / 非 re-exec `Serve()`），整套架构假设需调，越早暴露越好——故 spike 排在最前。
- **`PersistEnv` 连接复用**：每测试包一个进程，连接池在进程内建一次即可；注意 `t.Cleanup` 不关共享连接（随进程退出）。
- **shaper 的紧格式**：定长 uid 的 `UniqueInt(digits)` 位预算，token 单调增、压力测试时留意是否单射溢出（doc B §7）。
- **首批打样域**：建议 notify（已有 `api-test-framework-design.md` 范例 + schema 声明示例），验证阶段 3 全链路后再铺其他域。
