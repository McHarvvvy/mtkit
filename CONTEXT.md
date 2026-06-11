# mtkit 领域词汇表

> 本文是术语权威来源。只收**领域概念**，不收实现细节、文件路径、算法参数。

---

## L0 · 唯一性原语

daemon 托管的全局 token 发号器。以 `AllocIDBlock(kind, size)` 向客户端发放不相交的 int64 区间 `[start, end)`，保证跨进程全局单调唯一。

## L1 · 技术原语

`CaseScope` 上的生成方法：`UniqueInt`、`UniqueString`、`UniqueHex`。负责将 L0 token 经 Feistel FPE 单射成指定长度、指定字符集的唯一串或整数。与业务语义无关，只关心"多少位/多长/唯一"。

## L2 · 类型化生成器

`CaseScope` 上的格式化生成方法：`UUID`、`UUIDNoDash`、`Email`、`EmailFrom`、`Phone`。每次调用独立消耗一个 token（通过 L1），产出符合行业标准格式的唯一标识符。L2 方法之间不共享 token，不存在"同一标识符、两种格式"语义。

## CaseScope

每个测试用例一个的作用域对象。持有进程级 `blockSource` 引用（token 源）和 `testing.TB`（用于基建故障 fail-loud）。无业务状态，只"发号"。

## blockSource

进程级单例。本地缓存一段 token 区块（HiLo 模式），区块耗尽时向 daemon 补块。同进程内所有 `CaseScope` 共享同一个 `blockSource`。

## fixtured

daemon 内嵌的服务端组件。通过 `POST /v1/fixture/alloc` 暴露 L0 block 分配能力，挂载于 daemon 主端口，复用 `withAuth` 鉴权。无独立二进制。

## daemon

测试二进制以 `MTKIT_RUN_MODE=daemon` re-exec 后进入的常驻模式（`orchestrator.Daemon`）。托管 fixtured、faked、lease 等服务，与测试进程通过 `mtkit.runtime.json` 发现。

## Feistel FPE

平衡 Feistel 网络 + cycle-walking 构成的保形加密（Format-Preserving Encryption）。将 `[0, cap)` 上的 token 双射到同等值域，产生全宽随机外观。是 L1 唯一性的数学基础。

## UUID（client/fixture 语义）

RFC 4122 v4 合规的唯一标识符。`UUID()` 返回标准带短线格式（36字符），`UUIDNoDash()` 返回紧凑无短线格式（32字符）。两者均通过内部 `UniqueHex(30)` 生成，version nibble 固定为 `4`，variant nibble 固定为 `a`。

**注意**：项目侧"与 UUID 等长但无 v4 语义"的标识符（如 meross 设备 ID）不应使用 L2 UUID 方法，直接用 L1 的 `UniqueHex(32)`。

## plus addressing（EmailFrom 语义）

RFC 5321 子地址技术：`local+suffix@domain`。`EmailFrom(seed)` 接收完整合法 email 地址作为 seed，在其 local part 末尾追加 `+{唯一载荷}`，domain 保持不变，local part 原样保留（不裁剪已有 `+` 子地址）。seed 非合法 email 格式则 panic。

## E.164（Phone 语义）

ITU-T 国际电话号码标准：`+{国家码}{订户号}`，如 `+8613812345678`。`Phone(country)` 的输出格式。country 参数为 ISO 3166-1 alpha-2 代码（如 `"CN"`、`"US"`）；初始支持集合：CN / US / GB / DE / FR / JP / IT / CA。传入不支持的 country 则 panic。
