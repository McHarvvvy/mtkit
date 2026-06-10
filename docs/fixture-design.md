# Fixture 层设计（落地）

> **【命名映射】** 总模块 `mtkit`（module path `github.com/McHarvvvy/mtkit`）。**daemon 不是独立的 `mtkitd` 二进制**——它是测试二进制自身经 re-exec、以 `MTKIT_RUN_MODE=daemon` 进入的 daemon 模式（实现 `orchestrator.Daemon`）。本文旧名按此读：`fixturebase` → 客户端包 **`client/fixture`**（包名 `fixture`，在 `client/` 下）；协调内核 `coord` → 服务端 **`fixtured`**（与 lease 共用 daemon 主端口、非独立服务）。权威以 `mtkit-migration-plan.md` 为准。

> **【落地现状 · 2026-06】** 段1（testcontainerd → mtkit 迁移）与段2（`proc` 抽出 + faked 内嵌 daemon）**均已完成**，本文的诸多「依赖前提」已是既成事实：
> - **入口非 `bootstrap.Run`**：测试 `TestMain` 经 `mtkit.New(project, cfg).RegisterContainers(...).RegisterSUT(...).Run(m)` 启动；本文出现的 `bootstrap.Run` 是占位写法，**provision 接缝尚未接入该入口**，是段3 待落地点（见 §1）。
> - **faked 已内嵌 daemon 主进程**（非独立 `cmd/faked` 子进程）：control 面（`/v1/grpc/rules`、`/v1/http/rules`）挂 daemon 主 mux、与 lease 共用端口**及同一 token 鉴权**（daemon 经 `RegisterControl(mux, d.withAuth)` 注入，与 fixtured 一致）；数据面（gRPC endpoint / HTTP 代理）自有 listener 但同进程；rule 生命周期 == daemon 生命周期。测试侧用 `client/fake`（`fake.New(controlAddr, token)`，`controlAddr`/`token` = `mtkit.runtime.json` 的 `addr`/`token`）注册 rule。
> - **runtime 发现已具体**：`runtime` 包 + `RuntimeInfo`（`addr`/`token`/`pid`/`faked`/`sut_env`），落盘于 `<tmp>/mt/<project>/mtkit.runtime.json`。
> - **fixtured 接缝已预留**：`orchestrator/server.go` 已留挂载点 `d.fixtured.RegisterRoutes(mux, d.withAuth)`（与 lease 共用主 mux）；`client/fixture` 与 `fixtured` 当前仅占位 `doc.go`——**本文 §2 起的接口与机制即段3 待落地内容**。

本文是 fixture 层的**落地设计**：通用基座 `client/fixture` 的接口与机制，以及项目侧如何在其上扩展。它补的是另三份文档之间的 GAP：

- `api-test-framework-design.md`：**作者视角**——怎么写测试、fixture 使用约定、数据隔离原则（CaseScope / Seeder / Recipe / Request / Observe 的规则与范例）。
- `test-harness-design.md`（doc A）：**模块 / daemon 视角**——daemon、fixtured、provision、ID 分配落在哪。
- `faked-design.md`：fake 外部依赖的实现。
- 本文：**fixture 机制视角**——`client/fixture` 的接口长什么样、唯一性引擎怎么转、怎么接到 fixtured / faked / runtime。

**通用 vs 项目专属**：`client/fixture` 在 umbrella 模块内（只给机制与接口）；项目侧 `test/fixture/{scope,schema,seeders,request,observe}`（见框架文档 §3.3）提供内容，建在 `client/fixture` 上。

---

## 1. 数据流（把 fixture 串进 daemon / fixtured）

```text
TestMain（现状入口）：mtkit.New(project, cfg).RegisterContainers(...).RegisterSUT(...).Run(m)
  → re-exec daemon（MTKIT_RUN_MODE=daemon）就绪（doc A §2/§4）
  → 〔段3 待接〕按包 provision（fixtured 锁 + registry + Apply，doc A §6）；
     provision 接缝须挂进上面这条入口（传入 schema.NotifyStore()），目前尚无 bootstrap.Run

用例体（client 模式）：
  fx := scope.New(t)                         // 分配本用例 PrimaryScope 值（唯一性内部自给，调用者无感）
  u  := seeders.SeedUser(t, fx, opts...)     // fx.UUID()/fx.Email() 等生成（自动追踪）+ 直连 persist（地址来自 runtime 文件）
  fx.AssertBound(u.Guid)                     // 守卫：match 键须含本 case 生成/绑定的唯一值（见 §4）
  ctl.GRPC().On(...).RouteKey(u.Guid)...Apply(t)  // 注册 fake rule
  resp := request.Post(...)                  // When
  got  := observe.Xxx(t, u.UID)              // Then
```

- **provision、写数据都在测试进程**（DDL / seed 直连，fixtured 只给「锁 + 账本 + 发号」），见 doc A §4.3/§6。
- **fake rule 生命周期**：faked 已内嵌 daemon 主进程，rule 注册经 daemon 主端口（`client/fake` → `/v1/{grpc,http}/rules`），留存到 daemon 退出、靠 route_key 全局唯一隔离（faked-design §15），**无需 per-case cleanup**——与"不提供业务数据清理"的分桶纪律一致（框架文档 §8.3）。

---

## 2. 资源声明：`Requirement` / `Resource`

```go
package fixture

// Resource 一条可声明、可幂等创建的资源（表 / index / topic / subscription / 只读 profile）。
type Resource interface {
    Key() string                              // 稳定身份，如 "mysql.table.meross_user.users"
    Apply(ctx context.Context, env Env) error // 幂等创建（CREATE ... IF NOT EXISTS 等）；env 提供连接/地址
}

// Requirement 可组合的资源束；Requirements 组合并按 Key 去重。
type Requirement interface{ Resources() []Resource }
func Requirements(reqs ...Requirement) Requirement
```

**声明用显式导出函数，不用 `init` 自注册**（与 fake endpoint 的 `init`+`import _` 相反，理由：schema 的"激活"是 eager 且昂贵的 provision，必须**按包显式选择**、可见可审计；`init` 是包粒度全集、靠 import 副作用驱动 provision 会失控）：

```go
// 项目侧 test/fixture/schema
func NotifyStore() client/fixture.Requirement {
    return client/fixture.Requirements(UserStore(), MySQLTables("user_notify", "user_notify_setting"), RedisProfiles("notify"))
}
// 测试包 TestMain 显式传入本包所需。
// 现状入口是 mtkit.New(...).Run(m)；段3 需为其增加 provision 接缝（下例 WithSchema 为待定命名）：
func TestMain(m *testing.M) {
    os.Exit(mtkit.New(project, cfg).
        RegisterContainers(...).
        WithSchema(schema.NotifyStore()). // 〔段3 待加〕：Run 前驱动 Provision
        Run(m))
}
```

**两层**（doc A §6）：① SUT 启动必需的库 / tenant / 配置 → daemon `Init` 全局引导；② 测试用表 / topic → 按包 provision。provision 流程（`fixtured.WithLock → 查 registry → 缺则 Apply → MarkApplied`）由 `client/fixture` 驱动、在测试进程跑，详见 doc A §6。`Registry`（锁 + 已应用账本的抽象）接口定义在 `client/fixture`，由 fixtured client（daemon 内存后端）实现。

---

## 3. 数据生成分层（核心）

把"唯一性"与"格式"拆开：唯一性只在一处保证，每种格式是从唯一 token 到目标格式的**单射函数**——碰撞免于证明，不同字段不必强塞同一生成器。基于对本项目持久化 / 缓存 / API / 外部依赖字段的全面普查，分层如下。注意 **L1 与 L2 不在同一根轴上**：L1 是"基础技术原语"（与标准化无关），L2 才涉及"格式的标准化程度"。

```text
L0 唯一性原语        ── 区块发号 token / 锁 / daemon API（doc A §4.3）
L1 基础技术原语      ── 无语义、与标准无关的构件：生成定长唯一 int / 串 / hex
L2 公认数据类型      ── 生成唯一且合法的格式实例：email / phone / uuid / sha256
                       （"公认"含正式标准 RFC/ISO 与 事实标准——广泛通用但无正式 spec）
[标准枚举样例集]      ── 取值集稳定的标准枚举：ISO 国家码 / 货币 / IANA 时区 / 语言（挑选，非生成）
─────────────────  以上属 client/fixture  ↑   以下属项目  ↓  ─────────────────
项目层               ── 领域命名(uid 别名/guid 格式/order_id 方案) + 项目枚举(vendor/status) + 组合 + 派生接线 + 加密专用
```

> L1 是技术地基（多少位、多长、是否唯一），任何项目都一样、与"标准/共识"无关。"共识/事实标准"是 L2 的属性——email、电话号码这类大家都懂、未必有正式 spec 却有事实约定的数据类型。"标准化程度"这根轴只描述 L2 与项目层的边界（见 §3.4），不描述 L1。

### 3.1 L0 唯一性来源（一个，跨进程）

```text
进程本地区块缓存 ← fixtured.AllocIDBlock(kind, size)（doc A §4.3）
```

- daemon 串行发块 → 各进程拿到不相交区间 → token **跨进程全局唯一**、单调；本地用完再补块，热路径无锁。
- 无 container 跨 daemon 存活，token 随 daemon/容器同生死：新 daemon 从头发号，数据也是新的，**不与存量撞**（doc A §4.3）。

### 3.2 L1 基础技术原语 + L2 公认数据类型

**关键：唯一性来源（L0 token）不出现在任何公共签名里**——包内用区块分段 + 单射算法自给唯一，调用者无需提供任何信息。因此"生成唯一值"的 API 一律做成 **CaseScope 方法**（`fx` 持有内部 token 源；做成方法还让 `fx` 能自动追踪自己发出的值，喂 §4 守卫）；"确定性变换 / 时钟"不需唯一性，保持自由函数。

```go
// L1：唯一生成 → CaseScope 方法（无语义、与标准无关；只管"多少位/多长/唯一"）
func (s *CaseScope) UniqueInt(digits int) int64
func (s *CaseScope) UniqueString(n int, cs Charset) string   // Charset: AlphaNum / Lower / Hex / Digit ...
func (s *CaseScope) UniqueHex(n int) string

// L2：唯一生成 → CaseScope 方法；生成 + 保形装饰成对（取代旧的万能 Uniquify）
func (s *CaseScope) Email() string          ; func (s *CaseScope) EmailFrom(seed string) string  // alice@x.com → alice+<内部唯一>@x.com
func (s *CaseScope) Phone(country string)   ; func (s *CaseScope) PhoneFrom(country, seed string)
func (s *CaseScope) UUID() string           ; func (s *CaseScope) UUIDDashed() string

// 确定性变换 / 时钟：无唯一性、无 token → 自由函数
func Sha256Hex(s string) string                                     // 派生型：hash_email/hash_value 的构件
func EpochSeconds() int64    ; func EpochMillis() int64
```

- **L2 初始集合锁定这 5 类**（email / phone / uuid / sha256 / epoch）——普查中高频且满足 §3.4 判据；URL / IP / base32-TOTP / ISO 枚举一律 deferred 或归他处。后续可增。
- **TokenSource 是内部接缝、非公共参数**：`fx` 内部持有，生产唯一实现是 fixtured client（跨进程唯一）；它不进任何导出签名，纯逻辑单测用 trivial fake（见 impl-plan）。
- **"生成唯一" ≠ "挑一个合法值"**：email / uuid / phone 要每 case 生成唯一新实例 → L2 生成器；country_code / currency 是从真实集合挑一个、还可能多 case 复用 → 见 §3.3，**不进生成栈**。
- 旧 `Uniquify` 名义太泛、已废弃：拆成每格式的 `Xxx()`（凭空生成）+ `XxxFrom(seed)`（保留可读种子、注入唯一后缀）。断言用对象字段不用字面值（框架文档 §7.6）。

### 3.3 标准枚举样例集（client/fixture，挑选非生成）

取值集**稳定、跨项目共识**的标准枚举放 client/fixture，免得每个项目重抄一遍；它们**没有唯一性**，是"挑一个合法值"的便利常量 / picker：

```go
var ISOCountryCodes / ISOCurrencies / IANATimeZones / BCP47Languages []string
func SampleCountry() string   // 随机挑一个合法值（可重复，语义由调用方/项目决定取哪个）
```

- 收录线同 §3.4：取值集稳定、定义与项目无关。**项目内枚举（vendor / user_source / status / type / category）取值随业务变，归项目**，不进 client/fixture。
- 它们本质是 category-C 语义值，仍由 seeder option 显式传入（框架文档 §7.3）；client/fixture 只提供"合法样例"以免重复定义。

### 3.4 收录判据（哪一档归 client/fixture，哪些归项目）

- **进 L2（生成器）**当且仅当：① 格式合法性规则**不引用本项目**即可定义（无论来自正式标准 RFC/ISO，还是 email/电话这类无正式 spec 的事实标准）；② 消费方（SUT / 下游）**会解析或校验**它。只满足①不满足②（SUT 当不透明串，如 `pay_token`）→ 退回 **L1 技术原语**生成唯一串即可，不做 L2 格式。
- **进枚举样例集**：取值集稳定 + 跨项目共识（ISO/IANA/BCP47）。
- **归项目**：格式需项目知识（`guid` 排布、`order_id` 方案、`benefit_id`/`sid`/`app_id`）、或取值随业务变的项目枚举（`vendor`/`status`…）、或加密专用对象。

### 3.5 CaseScope 入口

```go
func New(t *testing.T) *CaseScope                 // 分配本用例 PrimaryScope 值（唯一性内部自给）
func (s *CaseScope) Primary() any                 // 本用例主 scope 值（user 服务即 uid）
func (s *CaseScope) AnotherPrimary() any          // 再要一个（如"A 给 B 发消息"）
// L1/L2 唯一生成器即本 fx 的方法（见 §3.2），生成的值自动登记进本 case
func (s *CaseScope) BindToScope(field, value string)  // 仅"外来"唯一值（见 §4）
func (s *CaseScope) AssertBound(values ...string)     // 守卫（见 §4）

// 项目侧注册 PrimaryScope 提供者：shaper 只见生成器句柄 Gen，看不到 raw token
func RegisterPrimary(name string, shape func(g Gen) any)
// Gen 是最小生成句柄：暴露 UniqueInt/UniqueString/UniqueHex/Email/UUID... 同名方法，但不暴露 token
```

- 框架不固定 PrimaryScope 字段（uid / merchantID / homeID…）；项目 `shape` 用 `Gen` 把唯一值塑形成本项目格式（如 `uid := g.UniqueInt(10)`、`guid := g.UUIDDashed()` 或自定义）。**raw token 不出现在任何公共面**（含 shaper）。
- 生成器输出对内部 token 单射，故跨用例唯一；`fx` 自动追踪所有经它生成的值（喂 §4 守卫）。
- 塑形不了的（密码学对象）走项目专用生成器、不进 token 源——见 §3.4 项目层。

> 紧格式位预算：定长短数字等，token 须能单射进其格式空间；空间太小会先耗尽（边缘，见 §7）。底层 `AllocIDBlock` 的 `kind` 留作"按格式分独立序列"的余地，v1 可单 kind。

---

## 4. 隔离守卫：AssertBound + 自动追踪

**先厘清谁提供隔离**：并发隔离来自**唯一生成**——SUT 调下游带的是下游协议字段（device UUID / token / merchant ref），这些字段由 `fx` 唯一生成 → route_key 跨 case 不同 → faked（case-agnostic，只按 route_key 字符串路由，faked-design §15）不串。**做到唯一生成，隔离就已成立。**

`AssertBound` / 追踪机制**不提供隔离**，只是一道**防御性 lint**：把"match 键只用了全局常量（vendor/region）→ 并行才暴露的 flaky 串扰"（框架文档 §9.1 反例 3）变成注册期的**确定性失败 + 清晰原因**。

```go
func (s *CaseScope) AssertBound(values ...string)     // match 键须含 ≥1 本 case 唯一值，否则 panic
func (s *CaseScope) BindToScope(field, value string)  // 仅登记"外来"唯一值（见下）
```

机制：

- **自动追踪**：凡经 `fx` 生成器（§3.2）产出的值都自动登记为"本 case 唯一值"。因为任何 `fx` 生成值都 per-case 唯一，守卫"match 键含 ≥1 本 case 唯一值"宽度刚好——它只拦"仅含全局常量"。
- **`BindToScope` 缩到边角**：只用于**不经 `fx` 生成**的 per-case 唯一值（如 SUT 自己生成、测试再读回的 ID）。经 `fx` 生成的字段**无需**手动登记。
- **守卫在项目注册接缝调用**：faked client 是传输级通用件、不依赖 client/fixture，故守卫不塞进其链式 builder，而由项目 seeder/request helper 在注册前调 `fx.AssertBound(matchValues...)`（见 §1 示例）。可选：项目封一个小 helper 把 `AssertBound` + 注册链包一层，使其更难漏调。
- 返回全局静态数据的 method 用 `routekey.Static()`（faked-design §15），其并发共享性由测试包并发策略决定，不走守卫。

---

## 5. Seeder / Recipe base

具体 seeder / recipe 属项目（命名、字段语义、插表逻辑见框架文档 §7.3/§7.4），`client/fixture` 只提供它们依赖的**通用原语与契约**：

- `CaseScope`（L1/L2 生成器方法、`BindToScope`/`AssertBound`）+ 自由函数（`Sha256Hex` / `Epoch*` / 枚举样例集）。
- **required-option 模式助手**：影响测试结果的语义字段必须显式传入，缺失则编译期 / `panic`（框架文档 §7.3"禁止隐藏默认值"）。
- **直连 persist 助手**：从 runtime 文件取 DB / Redis / Mongo 地址、建连接（见 §6）；seeder 用它写真实存储。
- **关键字段日志**助手：`t.Logf` 打 uid / email / role 等，失败可见（框架文档 §7.3）。

契约：seeder 只做 Given（不声明 schema、不发请求、不断言）；返回业务对象（断言用其字段，不用字面值）；routable 字段用 `fx` 生成器产出（自动追踪）；仅"外来"唯一值才需 `BindToScope`。

**派生字段接线**（普查暴露的真坑）：`hash_email` / `hash_value` 等是**派生自另一字段**的值（如 `Sha256Hex(normalize(email))`）。seeder 必须用**同一个**已生成的源字段去算派生值，否则两者对不上、按其查询失败。`client/fixture` 提供构件（`Sha256Hex`），但"派生自同源字段 + 规范化规则"由项目 seeder 保证。

---

## 6. Request / Observe base

具体 helper 属项目（框架文档 §7.5/§7.6）；`client/fixture` 提供底座：

- **runtime 发现**（现状已具体）：读 `mtkit.runtime.json`（`runtime` 包 + `RuntimeInfo`）——`addr`（daemon 主端口，lease / faked control / 段3 fixtured control 共用）、`sut_env`（容器连接信息，如 `REDIS_ADDR`，由 daemon 注入 SUT 后落盘）、`faked`（代理 / CA 调试可见性），据此构造 DB / Redis / Mongo / HTTP / gRPC 连接。
- `request` 在此上封装签名、网关 header、token、region 等；`observe` 在此上封装 SQL / key / filter。两者默认只依赖 uid / token 等基础字段，不依赖复杂业务对象（框架文档 §7.5）。

---

## 7. 通用 vs 项目专属 + 不做的事

| `client/fixture`（通用机制） | 项目（专属内容） |
|---|---|
| `CaseScope`（内部持 token 源、自动追踪）+ `BindToScope`/`AssertBound` 守卫 | `RegisterPrimary` 的字段名与 `shape(g Gen)` |
| L1 技术原语方法（`fx.UniqueInt/String/Hex`）、L2 生成器方法（`fx.Email/Phone/UUID…`）、自由函数（`Sha256Hex`/`Epoch*`）、标准枚举样例集（ISO/IANA/BCP47） | 领域命名(uid 别名/guid 格式/order_id 方案)、项目枚举(vendor/status…)、加密专用对象 |
| `Requirement` / `Resource` / `Registry` 接口 + provision 驱动 | 具体 schema 声明函数（显式导出，非 init） |
| seeder / recipe 原语（required-option、persist、日志、`Sha256Hex` 派生构件） | 具体 `SeedUser` / recipe（命名、字段、插表、派生接线） |
| runtime 发现 + 连接底座 | 具体 `request` / `observe` helper |

**不做的事**：

- 不提供面向业务数据的 cleanup / reset——靠分桶（框架文档 §8.3）。
- 不收项目内枚举 / 项目格式 ID / 加密专用对象——见 §3.4 判据。
- 不做"格式塑形 DSL"——按实际需要写具体 shaper，第 N 个再看是否抽公共。
- 不在 `client/fixture` 持有业务实体或表达实体关系——CaseScope 只"发号 + 改名"（框架文档 §7.2）。

**开放项**：紧格式 ID（定长短数字）跨多次运行的耗尽 / 回收，v1 不处理（int64 token 实际不回绕；真遇紧格式再按 `kind` 分序列或绑容器生命周期回收）。
