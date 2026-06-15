# mtkit 领域词汇表

> 本文是术语权威来源。只收**领域概念**，不收实现细节、文件路径、算法参数。

---

## L0 · 唯一性原语

daemon 托管的全局 token 发号器。以 `AllocIDBlock(kind, size)` 向客户端发放不相交的 int64 区间 `[start, end)`，保证跨进程全局单调唯一。

## L1 · 技术原语

`CaseScope` 上的生成方法：`UniqueInt`、`UniqueString`、`UniqueHex`。负责将 L0 token 经 Feistel FPE 单射成指定长度、指定字符集的唯一串或整数。与业务语义无关，只关心"多少位/多长/唯一"。`UniqueInt` 返回 `int64`，合法位数为 `1..18`；19 位及以上数字载荷用 `UniqueString(n, Digit)`。

## L2 · 类型化生成器

`CaseScope` 上的格式化生成方法：`UUID`、`UUIDNoDash`、`Email`、`EmailFrom`、`Phone`。每次调用独立消耗一个 token（通过 L1），产出符合行业标准格式的唯一标识符。L2 方法之间不共享 token，不存在"同一标识符、两种格式"语义。

## CaseScope

每个测试用例一个的作用域对象。持有进程级 `blockSource` 引用（token 源）和 `testing.TB`（用于基建故障 fail-loud）。无业务状态，只"发号"。

## blockSource

进程级单例。本地缓存一段 token 区块（HiLo 模式），区块耗尽时向 daemon 补块。同进程内所有 `CaseScope` 共享同一个 `blockSource`。

## fixtured

daemon 内嵌的服务端组件。通过 `POST /v1/fixture/alloc` 暴露 L0 block 分配能力，挂载于 daemon 主端口，复用 `withAuth` 鉴权。无独立二进制。provision 的公共接口与实现（`Resource` / `Registry` / `PersistEnv` / `Provision`）也归此包，避免 `mtkit` 门面依赖消费端 `client/fixture`。

## daemon

测试二进制以 `MTKIT_RUN_MODE=daemon` re-exec 后进入的常驻模式（`orchestrator.Daemon`）。托管 fixtured、faked、lease 等服务，与测试进程通过 `mtkit.runtime.json` 发现。

## Feistel FPE

平衡 Feistel 网络 + cycle-walking 构成的保形加密（Format-Preserving Encryption）。将 `[0, cap)` 上的 token 双射到同等值域，产生全宽随机外观。是 L1 唯一性的数学基础。

## UUID（client/fixture 语义）

RFC 4122 v4 合规的唯一标识符。`UUID()` 返回标准带短线格式（36字符），`UUIDNoDash()` 返回紧凑无短线格式（32字符）。两者均通过内部 `UniqueHex(30)` 生成，version nibble 固定为 `4`，variant nibble 固定为 `8`。

**注意**：项目侧"与 UUID 等长但无 v4 语义"的标识符（如 meross 设备 ID）不应使用 L2 UUID 方法，直接用 L1 的 `UniqueHex(32)`。

## provision

测试前置数据的一次性幂等建立（建表、索引、命名空间、种子数据等）。在 daemon 就绪后、`m.Run()` 前执行，由 `Provision` 函数驱动：对每个 `Resource` 调 `Registry.EnsureOnce`，保证同一 daemon 生命周期内同一 key 只 Apply 一次。

## Resource

可 provision 的基础设施单元。`Key()` 返回跨进程跨运行的稳定字符串标识；`Apply(ctx, env)` 执行实际建立操作（建表、写种子等）。`Key` 一旦确定不能更改，它是 Registry 账本的持久化索引。

## Registry

provision 的跨进程幂等协调机制。唯一方法 `EnsureOnce(ctx, key, fn)`：在互斥保护下，若 key 未曾应用则执行 fn 并标记，否则跳过（`skipped=true`）。v1 后端为文本账本（FileRegistry）。

## 文本账本（FileRegistry）

Registry 的 v1 实现后端。daemon 启动时在 RuntimeDir 创建空的 `mtkit.provision.json`（JSON 集合格式），生命周期严格绑定 daemon/容器——daemon 重启则文件重置。多测试进程通过整文件锁（flock/LockFileEx）序列化读写，无任何数据库依赖。

## PersistEnv

为 `Resource.Apply` 提供数据库连接的进程级连接池。从 `RuntimeInfo.SUTEnv`（`mtkit.runtime.json` 的 `sut_env` 字段）读取连接字符串，按 sutEnvKey 懒初始化并复用。与 Registry 完全独立——Registry 协调"谁来执行"，PersistEnv 提供"执行时用的连接"。v1 使用 `database/sql` + `github.com/go-sql-driver/mysql`、`github.com/redis/go-redis/v9`、`go.mongodb.org/mongo-driver/mongo`；Redis namespace 资源本身为 no-op。

## 标准枚举样例集（Sample* 语义）

`client/fixture` 提供的"挑选非生成"便利函数：`SampleCountry()`、`SampleCurrency()`、`SampleTimeZone()`、`SampleLanguage()`，以及对应的全集变量 `ISOCountryCodes`、`ISOCurrencies`、`IANATimeZones`、`BCP47Languages`。无唯一性、无 token 消耗，用于"测试需要一个合法值"而非"需要唯一值"的场景。收录判据：取值集稳定 + 定义与本项目无关（ISO/IANA/BCP47）。项目内枚举（vendor/status 等）不进此处。

## plus addressing（EmailFrom 语义）

RFC 5321 子地址技术：`local+suffix@domain`。`EmailFrom(seed)` 接收完整合法 email 地址作为 seed，在其 local part 末尾追加 `+{唯一载荷}`，domain 保持不变，local part 原样保留（不裁剪已有 `+` 子地址）。seed 非合法 email 格式则 panic。

## E.164（Phone 语义）

ITU-T 国际电话号码标准：`+{国家码}{订户号}`，如 `+8613812345678`。`Phone(country)` 的输出格式。country 参数为 ISO 3166-1 alpha-2 代码（如 `"CN"`、`"US"`）；初始支持集合：CN / US / GB / DE / FR / JP / IT / CA。传入不支持的 country 则 panic。第一版只保证 E.164 语法与国家码正确，不承诺真实号段可分配。
