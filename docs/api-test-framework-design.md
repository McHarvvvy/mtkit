# API 测试框架设计备忘

本文记录 API / 入口级集成测试框架的设计共识：一套可在 `mssiot_user` 及其他 Go 服务复用的测试组织、fixture 分层、资源初始化和数据隔离原则。

> Fake 外部依赖的**具体实现**（fake daemon、control API、rule schema、MITM 代理、CA、链式注册 client）见 `faked-design.md`。本文只描述框架视角下 fake 与 fixture / 数据隔离的衔接契约。

## 1. 目标

- 通过真实 HTTP / gRPC / MQ / task 入口验证系统契约。
- SUT 以接近生产的独立进程运行；本服务自有 DB / Redis / Mongo / Pulsar 用真实基础设施；外部不可控依赖用 fake 替换。
- 业务测试数据通过分桶隔离，不依赖 per-case cleanup。
- 测试包按需声明资源依赖，runtime 在启动期批量 provision。
- 测试代码结构、fixture 模型和 infra 能力可跨项目复用。

### 1.1 基本概念

- **SUT（System Under Test）**：被测系统，以独立进程运行、对测试代码黑盒；框架不改 SUT 源码。
- **PrimaryScope（主租户维度）**：SUT 内部业务逻辑天然据以隔离 / 过滤数据的主租户维度（user 服务通常选 uid，订单服务可能是 merchantID，设备服务 homeID，多租户 SaaS 是 accountID）。框架不假定字段，由使用方初始化时声明；一个服务可有多个候选维度（如 uid / guid），选其一作正式 PrimaryScope，其余作衍生身份字段。要求：是 SUT 主要租户维度（绝大多数实体直接或间接挂其下），且在测试运行内可全局唯一（由 CaseScope 生成）。
- **CaseScope（用例作用域）**：一个用例的隔离命名空间，持有该用例的 PrimaryScope 值（可多个），并提供唯一值生成原语。
- **Seeder / Recipe**：Seeder 是单实体构造器，Recipe 是面向场景的组合器；都负责把 case 级数据写入 SUT 真实存储。
- **Fake**：外部不可控依赖的替身，按 SUT 实际发出请求中的字段路由，字段经 fixture 显式绑定到 case。
- **跨域可见字段（routable field）**：SUT 对外部依赖发起调用时携带、可反查到 case 的字段。由下游协议决定，**不一定是被测服务自己的 PrimaryScope**——调内部下游微服务时常是下游主标识（如 device UUID），调第三方平台时是平台标识（如 device token、merchant ref）。这些字段须在 fixture 创建实体时经 `BindToScope` 绑定到 CaseScope，才能用作 fake 路由依据。

## 2. 测试范围

**Entry 测试**（`test/entry`）：通过系统入口（HTTP / gRPC endpoint、MQ consumer、task / job 入口）触发行为，是 API 测试主体。断言面优先选外部可观察契约：response、反向读接口、业务联动接口、业务契约级外部触达；必要时用 observe helper 封装后门观察。取名 `entry` 而非 `api`，因 MQ / task 非传统 API 但同属系统入口契约测试。

**Component 测试**（`test/component`）：只用于跨包、跨层级、易循环引用或源码归属不清的代码级组件集成测试。单包 DAO / logic / util 测试应放源码同目录。

## 3. 目录结构

```text
test/
├── infra/       # 框架性支撑（含 faked/ fake daemon）
├── fake/        # 具体外部依赖 fake（http/ grpc/）
├── fixture/     # scope/ schema/ seeders/ request/ observe/
├── entry/       # http/ grpc/ mq/ task/
└── component/
```

### 3.1 infra

`test/infra` 放框架性支撑代码：容器生命周期（MySQL / Redis / Mongo / Pulsar）、资源声明 / provision / registry / 锁、SUT 构建启动与环境注入与健康检查、fake daemon 生命周期、HTTP MITM 代理与测试 CA、`TestMain` 统一入口。不放具体业务 fake / fixture / 某 API 专属数据。fake server / daemon 实现见 `faked-design.md`（位于 `infra/faked/`）。当前阶段可保持扁平：

```text
infra/
├── run.go           # TestMain 入口 Run(m)
├── requirements.go  # 资源需求模型
├── registry.go      # provision registry
├── containers.go
├── sut.go
└── faked/           # fake gRPC / HTTP daemon（见 faked-design.md）
```

### 3.2 fake

`test/fake` 放具体外部依赖的 fake 定义，每外部服务一个文件——endpoint、typed 薄壳（gRPC）、route 声明（注入 / 注册接口由 faked 框架提供，见 `faked-design.md`）：

```text
fake/
├── http/googlepay.go
└── grpc/forum.go
```

不要过早拆成每 fake 一目录；某 fake 复杂到需多文件时再拆。

### 3.3 fixture

`test/fixture` 放测试资源声明、case 级数据构造、请求与观察 helper：

- `scope`：`CaseScope` 和唯一值原语。
- `schema`：schema / table / index / topic / subscription / 只读 profile 的资源声明。
- `seeders`：seeder 和 recipe（同包，避免互相引用产生循环；文件名前缀 `seeder_*` / `recipe_*` 区分）。
- `request`：HTTP / gRPC 请求 helper，封装协议、签名、中间件 header、token、region、vendor、appType 等重复细节。
- `observe`：反向读或必要后门观察 helper，封装 SQL / Redis key / Mongo filter 等观察细节。

`schema` 独立维护、不经 seeder / recipe 封装：schema 与 seeder 无稳定一对一关系（一个 seeder 可依赖多张表，多个 seeder 也可共享一组 schema），schema 是 infra provision 的输入、seeder 是 case 级 Given 的实现，生命周期不同。

暂不引入独立 `model` 目录。`request` / `observe` 默认不依赖 seeder 返回的复杂业务对象，优先用 uid、token、deviceID 等基础字段或 option；将来多个 fixture 子包反复共享复杂业务类型并出现明确依赖问题时再抽 model 包。

```text
fixture/
├── scope/scope.go
├── schema/{mysql_user.go, mysql_notify.go, mongo_device.go, pulsar_user_event.go}
├── seeders/{seeder_user.go, seeder_device.go, seeder_notify.go, recipe_notify.go, recipe_security.go}
├── request/{http.go, grpc.go}
└── observe/{notify.go, device.go}
```

### 3.4 entry / component

`entry` 放通过系统入口触发的黑盒 / 近黑盒测试，文件按 domain + action 命名（不照 URI）；`component` 扁平组织，按业务 workflow / domain 命名。

```text
entry/http/user_notify_switch_test.go
entry/grpc/device_service_bind_test.go
entry/mq/member_account_flush_test.go
entry/task/user_expire_cleanup_test.go

component/notify_security_workflow_test.go
component/user_device_binding_workflow_test.go
```

不建议把 component 按 `dao/logic/cache/mq` 细分；单包 DAO / logic 测试应回到源码同目录。

## 4. Runtime 生命周期

每个测试包通过 `TestMain` 接入统一 runtime：

```go
func TestMain(m *testing.M) {
    os.Exit(infra.Run(m, schema.UserStore(), schema.NotifyStore()))
}
```

典型启动顺序：

1. 收集本测试包声明的资源需求。
2. 启动 / 获取测试基础设施容器。
3. 启动期批量 provision 本次需要的 schema / table / index / topic / subscription / 只读 profile。
4. 启动 fake gRPC / HTTP server。
5. 构造 SUT 环境变量。
6. 构建并启动 SUT 进程。
7. 执行测试用例。
8. 停止 SUT 与 fake server。
9. infra 按租约 / idle 策略释放容器。

资源创建发生在 SUT 启动前，不在单 case 执行中惰性创建。fake server / daemon 与基础设施容器无固定先后依赖，约束只是：资源 provision 和 fake 地址注入都必须在 SUT 启动前完成。

## 5. 资源声明与初始化

### 5.1 按测试包声明依赖

资源依赖以测试包为粒度声明——只跑某个测试包时只初始化该包声明的最小资源集合，不初始化全项目所有表和 topic。推荐由 `fixture/schema` 提供声明函数，测试包不直接关心表名：

```go
func NotifyStore() infra.Requirement {
    return infra.Requirements(
        UserStore(),
        infra.MySQLTables("user_notify", "user_notify_setting"),
        infra.RedisProfiles("notify"),
    )
}
```

不扫描测试代码自动推断依赖（`-run` 过滤、recipe 分支、动态构造会让推断不稳定），采用包级声明。

### 5.2 启动期按需 provision

```text
创建时机：runtime 启动期
创建范围：本测试包声明的最小资源集合
执行方式：批量、去重、幂等
业务数据：仍由 seeder / recipe 分桶创建
```

schema / table / collection / index / topic / subscription / 只读 profile 属 infra provision 资源，定义放 `fixture/schema`、执行由 `infra` 负责，不由 case 创建销毁；用例所需 user / device / order 等业务数据属 case 数据，由 seeder / recipe 用 `CaseScope` 创建并分桶隔离。

### 5.3 并发安全

`go test ./...` 默认并发执行多包，不同包可能同时声明相同表 / topic，故 infra 初始化必须跨进程安全：

- 所有资源创建操作幂等（MySQL `CREATE ... IF NOT EXISTS`；Mongo collection / index exists 视为成功且 index name 稳定；Pulsar topic / subscription exists 视为成功；Redis 不创建结构，只靠 profile 和 key prefix 约束）。
- provision 阶段有跨进程锁。
- registry 是所有测试进程共享的状态，不是 Go 进程内存 map。
- checksum 变化显式失败，提示重建测试环境或执行迁移。

### 5.4 文件锁与 JSON registry

默认用 filesystem lock + JSON registry，避免对 MySQL 容器和启动顺序产生强依赖：

```text
.testinfra/
├── provision.lock   # 跨进程互斥
└── registry.json    # 记录已应用资源
```

```json
{
  "resources": {
    "mysql.table.user.users": { "checksum": "sha256:abc", "applied_at": "2026-05-11T10:00:00Z" },
    "pulsar.topic.user-event": { "checksum": "sha256:def", "applied_at": "2026-05-11T10:01:00Z" }
  }
}
```

流程：获取 `provision.lock` → 读 `registry.json` → 汇总去重当前 requirements → 未应用资源执行创建 / 已应用但 checksum 不一致显式失败 → 创建成功更新并原子写入（临时文件 + rename）`registry.json` → 释放锁。第一版可用全局 `provision.lock`，后续如有瓶颈再演进到资源级锁。

文件 registry 只在同机、同工作目录或共享 state dir 下有效。CI 拆到多机且无共享 volume 时需替换为 MySQL / Redis / etcd backend，故保留接口：

```go
type Registry interface {
    WithLock(ctx context.Context, key string, fn func(State) (State, error)) error
}
```

默认实现为 file registry。

## 6. Fake 设计（框架契约）

`test/fake` 只 fake 外部不可控依赖，不 fake 被测服务自己的 DAO / cache / MQ。fake server / daemon 的实现细节见 `faked-design.md`；本节只规定 fake 在测试框架中的契约——与 fixture、数据隔离的衔接。

### 6.1 部署形态

fake 可与测试进程同进程，也可独立成 daemon（独立 daemon 模式见 `faked-design.md`）。两种形态差别只在 fake 状态是 Go 内存还是跨进程 control API，都满足：

- SUT 通过真实网络访问 fake，不经任何源码注入。
- 每个 fake 方法有默认未实现行为，未配置时快速失败。
- 行为注入接口统一：测试用例声明匹配字段和返回数据，不注册跨进程 Go 函数。
- 业务 fake 放 `test/fake`，fake server / daemon 生命周期放 `test/infra`。

### 6.2 路由模型

fake 按 SUT 实际发出、fake 实际收到的请求字段做**精确路由**，测试用例声明本用例关心的匹配字段。约束：

- 匹配字段必须能从 fake 实际收到的请求中提取，不能用只存在于测试源请求、不会传到外部依赖的字段。
- 精确匹配，未声明字段不参与匹配；不做"更具体规则优先"等通用 matcher 逻辑。
- 重复注册相同路由键立即失败，禁止静默覆盖。
- 0 命中必须 fail loud，不返回空响应。

### 6.3 已绑定字段（routable field）约束

匹配字段至少包含一个**已被 fixture 绑定到 CaseScope 的跨域字段**——这是 fake 并发隔离的承重环节：两条并发用例同一字段值不同 → 路由键不同 → fake 不混淆。

具体字段由下游协议决定（device UUID、device token、merchant ref 等），**不一定是被测服务自己的 PrimaryScope**。fixture 创建跨域字段时必须 `BindToScope`：

```go
func SeedDevice(t *testing.T, fx *CaseScope, opts ...Option) *Device {
    device := persist(...)
    fakegrpc.BindToScope(t, "device_uuid", device.UUID, fx)
    return device
}
```

每个 fake method 启动时声明自己接受的 routable fields，框架在两处校验：**注册侧**校验匹配键至少含一个 routable field，否则 panic；**fake 侧**校验匹配键中的字段值能在反查表找到对应 case，否则报 `unbound_match_field`（提示漏调 `BindToScope`）。fake 收到实际请求时，按 method 声明的 routable fields 从请求提取字段值、查反查表确认所属 CaseScope，再查规则。CaseScope 用于校验隔离和记录调用归属，不替代规则唯一键。

这套机制让请求**不含** PrimaryScope 时 fake 仍能准确路由。代价是 seeder 创建跨域字段时必须记得 `BindToScope`——进 seeder 检查清单（见 7.3）。

### 6.4 规则生命周期与异步调用

fake 规则属测试框架状态：in-process 形态可 `t.Cleanup` 在用例结束时移除；daemon 形态规则保留到 daemon 退出，靠 routable field 全局唯一保证不串扰。无论哪种形态，若 SUT 异步调用 fake，测试必须等待业务可观察结果再退出，避免清理 / 断言早于异步调用窗口。只有无法按请求字段分派、且需为不同用例返回不同数据的全局 fake 行为，才用 exclusive case（见 8.4）。

## 7. Fixture 分层模型

```text
Schema Requirement -> Runtime Provision
CaseScope -> Seeder / Recipe -> Request / Observe -> Test Case
```

`test/fixture` 负责资源声明、Given 和观察辅助，不隐藏业务断言。

### 7.1 Schema

`fixture/schema` 维护测试所需的 schema / table / index / topic / subscription / 只读 profile 声明。规则：

- 只描述测试运行前需 provision 的资源，不创建 case 级业务数据。
- 不经 seeder / recipe 封装，测试包在 `TestMain` 显式声明。
- 必须有稳定 resource key 和 checksum。
- 创建动作由 `infra` 执行，`fixture/schema` 不管理锁 / registry / 容器生命周期。

### 7.2 CaseScope

`fixture/scope` 为一个用例提供独立逻辑命名空间，承载其 PrimaryScope 值，并提供唯一值原语。由三类能力组成：

**(a) PrimaryScope 持有**——创建时即分配本用例的 PrimaryScope 值：

```go
fx := scope.New(t)   // fx 内部已持有本用例独占的 primary scope 值（user 服务对应 uid）
```

框架不固定字段名，使用方初始化时声明提供者：

```go
func init() {
    scope.RegisterPrimary("uid", func() any { return nextGlobalUID() })
}
```

一个用例需多个 PrimaryScope 值（如"用户 A 给 B 发消息"）时用 `fx.AnotherPrimary()` 显式申请，不在同一用例复用同一值制造冲突。

**(b) 唯一值原语**——生成"测试不关心其语义"、只需运行内唯一的字段：

```go
token := fx.NextString("token")
```

**不应**用于生成测试关心其语义的字段（email、name、role、plan、status 等），后者通过 seeder option 显式传入。

**(c) 语义值唯一化**——保留语义可读、同时持久化层要求唯一的字段（email、phone、name）：

```go
// 测试代码侧看到 "alice@example.com"；fixture 内部 Uniquify 改写成 "alice+<scope_suffix>@example.com"
realEmail := fx.Uniquify("alice@example.com")
```

seeder 在持久化前对相关字段调 `Uniquify`，`scope_suffix` 来自 CaseScope 隔离 token，保证跨用例唯一；具体改写规则封装在 seeder 内部，对测试代码不可见。

**CaseScope 不做**：不持有业务实体（user / device 对象是 seeder 返回值）、不表达实体间关系（归属由 seeder 显式传 option 建立）、不替 seeder 决定字段语义。CaseScope 是"发号 + 改名"工具，不是业务模型。

### 7.3 Seeder

`fixture/seeders` 中的 seeder 构造真实业务数据：测试代码侧暴露本测试相关的全部语义字段，fixture 内部完成唯一化、关联建立、持久化、scope 绑定等机制细节。设计规则：

- 按业务实体或强内聚聚合命名，不按 API 命名。
- 用 functional options 表达字段值；返回业务对象而非裸 ID。
- 内部可耦合 DB / Redis / Mongo 表结构，外部暴露业务语义。
- 不声明或 provision schema；只做 Given，不做 When / Then。

**语义字段必须显式**——凡影响测试结果的字段（决定 SUT 分支、影响返回值 / 权限 / 是否触发外部调用），seeder 须满足其一：

```go
// (a) 通过 option 接收语义值
user := seeders.SeedUser(t, fx, seeders.WithEmail("alice@example.com"), seeders.WithRole("admin"), seeders.WithPlan("pro"))
// (b) 通过命名 seeder / recipe 编码语义
user := seeders.SeedAdminUser(t, fx)       // 名字告诉你 role=admin
user := seeders.SeedSuspendedUser(t, fx)   // 名字告诉你 status=suspended
```

option 方式下 seeder 内部对需唯一化字段调 `fx.Uniquify`，返回值保留 SUT 实际持久化的真实值；断言时用对象字段而非字面值。

**禁止隐藏默认值**——凡影响测试结果的字段，seeder 内部不许填默认值；caller 既没传 option 也没用命名 recipe 时应 panic 或编译期报错（required option pattern）。允许填默认值的仅限对所有用例都无业务语义、纯占位让记录合法的字段：创建时间戳、SUT 不验证内容的内部 token、schema NOT NULL 但测试不关心的字段。某字段在部分测试是占位、在另一些是关键判定时，按后者处理、全部要求显式声明。

**绑定跨域字段到 case**——seeder 创建的对象若含会出现在 SUT 对外请求里的字段（device UUID / token、外部账号 ID、merchant ref），必须调 `BindToScope` 通报给 fake（机制见 6.3）；漏掉会导致 `unbound_match_field` 或并发路由错乱。

**关键字段日志**——seeder 创建对象后把关键字段打到测试日志，`go test -v` 失败时直接可见、降低 debug 成本、便于 CI 日志 cross-check：

```go
t.Logf("fixture: SeedUser uid=%v email=%s role=%s plan=%s", user.UID, user.Email, user.Role, user.Plan)
```

**命名风格**——推荐 `SeedUser()` / `SeedAdminUser()` / `SeedNotifyAttribute()`；不推荐按 API（`SeedUserForNotifySwitch`）或测试编号（`SeedDataForTC04`）命名。

### 7.4 Recipe

`fixture/seeders` 中的 recipe 是面向业务场景的组合器：Seeder 回答"创建一个实体"，Recipe 回答"创建一个完整业务上下文"。

```go
scene := seeders.SeedSecurityCenterClosableUser(t, fx,
    seeders.WithSecurityPassword("123456"),
    seeders.WithSecurityPasswordVerified(true),
)
```

规则：名字表达业务状态（不表达 API / 测试编号）；内部调 seeder 不复制插表逻辑；返回含后续请求和观察所需实体的场景对象；不提前封装（组合复用度高时才新增）；不声明 / provision schema；不执行请求、不做断言。

**命名编码语义状态**——由 recipe 名承载的状态必须在名字显式表达，读者只读 recipe 名和 option 应能完整复原本 case 的 Given。推荐 `SeedSecurityCenterClosableUser()` / `SeedNotifyUserWithoutTimezone()` / `SeedHomeWithOwnerAndMember()`；不推荐 `SeedNotifyUser2()` / `SeedTestUser()` / `SeedComplexScenario()`。字段组合超过 3 个、命名变冗长时，拆成 builder + option，或只把高频组合做成命名 recipe、其他退回 base seeder + options。

### 7.5 Request helper

请求构造仍由用例决定，但签名、网关注入 header、token、region、vendor、appType 等中间件细节不应每个用例裸写：

```go
body, headers := request.NewHTTPV1("/user/v1/notify/switch").
    WithUID(user.UID).WithToken(user.Token).WithVendor("meross").WithRegion("hhw").BuildSignedJSON(payload)
```

中间件数据分两类：纯请求上下文放 `request/http.go`（`WithUID` / `WithToken` / `WithoutIdentity` / `WithRegion` / `WithVendor`）；需真实后端状态支撑的身份放 seeder / recipe（`SeedUser` / `SeedLoggedInUser` / `SeedAdminUser`）。`request` 默认不依赖 seeder 返回的复杂对象，优先用 uid、token、region 等基础字段或 option。

### 7.6 Observe helper

`observe` 不替用例做断言，只封装"如何观察系统状态"，用例仍负责 Then：

```go
got := observe.NotifySwitchInfo(t, user.UID)
require.Equal(t, 1, got.ForumNotify)
```

观察须走 DB / Redis / Mongo 后门时，SQL / Redis key / Mongo filter 等细节封装在 observe helper，避免测试文件直接出现表名或底层查询。`observe` 默认不依赖复杂业务对象，文件名按业务域命名、函数名表达观察对象而非断言动作。

**断言原则：对象字段比对，不与字面值比对**——期望值应来自 fixture 返回的对象字段：

```go
// ✅ 和 fixture 对象字段比对
user := seeders.SeedUser(t, fx, seeders.WithEmail("alice@example.com"))
got := observe.UserInfo(t, user.UID)
require.Equal(t, user.Email, got.Email)
// ❌ 和字面值比对：seeder Uniquify 改写规则一变测试就崩
require.Equal(t, "alice@example.com", got.Email)
```

seeder 可能对语义字段做唯一化改写（`alice@example.com` 在 SUT 内实为 `alice+xxx@example.com`），和对象比对则无论内部如何改写断言永远成立。测试代码里的字面值只承担可读性，不承担断言职责。

## 8. 数据隔离策略

### 8.1 隔离机制的因果链

并发隔离不依赖 per-case cleanup，也不依赖物理环境分割，而依赖以下因果链同时成立：

```text
1. CaseScope 生成本用例的 PrimaryScope 值，运行内全局唯一。
2. 所有写入 SUT 的业务实体都经 seeder，seeder 把该 PrimaryScope 值串入记录的租户字段（直接或经次级实体间接关联）。
3. SUT 业务逻辑本身多租户、按 PrimaryScope 过滤，两条用例 PrimaryScope 不同 → 数据在 SUT 视角互不可见。
4. SUT 调外部依赖时携带的是下游协议要求的字段（device UUID/token、merchant ref），不一定含自己的 PrimaryScope。
5. fixture 创建上述跨域字段时已经 BindToScope 把"字段值 → 本 case"注册到 fake 反查表；fake 收到请求时按请求中实际存在的字段反查到 case，再做规则路由。
```

第 4、5 步最易误解：**SUT 调下游按下游协议传字段，不按自己的租户结构传字段**（user 服务调 device 服务 GetDeviceInfo 传 device UUID 而非 uid；调第三方推送传 device token，连 UUID 都没有），所以"fake 请求里有 SUT 的 PrimaryScope"不成立。真正成立的是**反查链**：fixture 创建 device 时知它属 case A、注册 device token 时知它属 case A 的某 device，每步映射创建时同步给 fake，fake 按请求中实际有的字段反查即可定位 case。

任意一环失效都破坏并发安全，对应维护检查项：

- **新增 seeder 查第 2 条**：写入的所有表 / 集合 / key 是否都带 PrimaryScope 维度？存在不带维度的全局表则不能用于并发用例，须走独占例外。
- **新增 seeder 查第 5 条**：是否创建了会出现在 SUT 对外请求中的跨域字段？是则必须 `BindToScope`。
- **新增外部 fake method 查第 4 条**：SUT 实际发出的请求里至少一个字段必须是 fixture 能绑定的跨域字段；若全是全局常量（vendor、region、固定 app_id）则无法并发隔离，须走独占例外。

### 8.2 资源在隔离机制中的分类

按角色分四类（同一字段可兼具多种角色，如外部账号 ID 既需唯一化又出现在外部请求里，此时持久化前唯一化、作 fake 路由字段时还须 `BindToScope`）：

- **(a) PrimaryScope 字段**：被测服务主租户维度，CaseScope 持有并保证全局唯一，SUT 按此过滤；字段因服务而异（见 1.1）。
- **(b) 跨域可见字段（routable field）**：会出现在 SUT 对外请求中、被跨进程调用携带，是 fake 路由素材；fixture 创建时须 `BindToScope`（见 6.3）。示例：调 device 服务的 device UUID、调推送平台的 device token、调支付平台的 merchant ref / trade no、调论坛服务的 forum user guid。这些字段在 SUT 存储中也存在，特殊在"会跨越 SUT 进程边界出现在外部请求里"。
- **(c) 语义可读字段（需唯一化）**：保留语义可读、同时 schema 要求唯一，经 `Uniquify` 在 seeder 内改写。示例：email、phone、login_name、external_account_id。
- **(d) 纯随机占位字段**：测试不关心语义、只需运行内唯一，经 `NextString` / `NextInt64` 生成。示例：trace_id、内部 session token、SUT 不校验内容的随机串。

只要 SUT 的存储、外部调用、Redis key、MQ 消息以上述某类字段为维度组织数据，就在隔离机制覆盖内：DB 行含 PrimaryScope / 跨域字段、Redis key 模板含上述维度、Mongo filter 含上述维度、MQ 消息含可过滤业务维度、fake 规则路由键含跨域字段。

### 8.3 默认策略：分桶

普通业务数据的默认隔离只有一条——数据分桶：

```text
CaseScope 只分配唯一命名空间和唯一值。
Seeder 只写入业务数据。
Fixture 不提供面向业务数据的清理 / 重置 API。
测试用例不负责清理业务数据。
```

不提供业务数据清理 API，因为：清理削弱分桶纪律、诱导测试依赖删除而非唯一性；清理逻辑本身是新失败源；DB / Redis / Mongo / MQ 易漏清；并行测试有误删风险；失败后保留现场更利排查。

cleanup 不是业务数据隔离手段，但可作受控例外用于释放框架资源或恢复独占修改：fake route / handler 注册（in-process）、临时文件 / 端口 / 后台 helper / timer、测试锁与 exclusive case 修改过的全局状态、其他无法分桶且已声明独占的资源。本地调试需清空业务环境时走 infra 维护工具 / 重启 mtkit daemon / 重建容器，不进普通 fixture API。

### 8.4 分桶无法单独解决的资源

全局唯一配置、固定自然键（vendor / region / config key）、全局 MQ topic / consumer subscription / 消费进度、不含业务维度的全局 Redis key、覆盖式全局 fake handler、业务本身要测同一对象并发竞争——这些用明确例外机制：

1. **独占测试**：修改全局状态时显式用 exclusive case 或测试锁，并在 cleanup 恢复。
2. **全局资源 profile**：对固定配置预置只读 profile；必须修改时用 exclusive。
3. **fake 按请求分派**：优先按实际 fake 请求字段路由，而非全局覆盖。
4. **必要时重建测试环境。**

```text
默认：分桶   例外：独占 + 受控 cleanup   维护：重建环境
```

## 9. 测试用例结构

```go
func TestHTTP_User_NotifySwitch(t *testing.T) {
    fx := scope.New(t)
    scene := seeders.SeedNotifyUser(t, fx)

    resp := request.PostHTTPV1(t, "/user/v1/notify/switch",
        request.WithUID(scene.User.UID), request.WithToken(scene.User.Token),
        request.JSON(map[string]any{"forumNotify": 1}))
    requireSuccess(t, resp)

    got := observe.NotifySwitchInfo(t, scene.User.UID)
    require.Equal(t, 1, got.ForumNotify)
}
```

用例负责 **Given**（调 seeder / recipe 构造上下文）、**When**（用 request helper 触发入口）、**Then**（用 response / observe / 前门反向读断言）。Fixture 负责构造数据、封装重复请求细节、封装观察通道。不应出现：用例直接写大量 DB / Redis 细节；用例直接清理业务数据；recipe 暗中执行请求 / 断言；seeder 按 API / 测试编号命名；seeder / recipe 暗中声明 / provision schema；断言依赖 cache key / SQL 列 / 内部调用次数 / 内部日志（外部 fake 调用记录只用于断言业务契约级触达，触达次数仅在契约明确要求时才断言）。

### 9.1 反例对照

**反例 1：隐藏关键语义**

```go
// ❌ 实际依赖 plan=pro，用例里看不出来
user := seeders.SeedUser(t, fx)
// ✅ 语义可读
user := seeders.SeedProUser(t, fx)  // 或 seeders.SeedUser(t, fx, seeders.WithPlan("pro"))
```

**反例 2：用字面值断言导致 fixture 改造时崩**

```go
// ❌ 一旦改 Uniquify 规则断言就崩
require.Equal(t, "alice@example.com", got.Email)
// ✅ 和 fixture 对象字段比对
require.Equal(t, user.Email, got.Email)
```

**反例 3：fake match 未锚定到已绑定字段**

```go
// ❌ vendor 不是已绑定字段，多条并发用例路由键会相同
fakegrpc.RegisterGetUserInfo(t, fakegrpc.MatchGetUserInfo().Vendor("meross"), profile)
// ✅ 含已绑定字段（user.Guid 在 SeedUser 时已 BindToScope）
fakegrpc.RegisterGetUserInfo(t, fakegrpc.MatchGetUserInfo().Vendor("meross").Guid(user.Guid), profile)
```

### 9.2 标杆样本

串起所有原则的完整样本，新增 case 可参考：

```go
func TestHTTP_User_NotifySwitch_AdminCanToggle(t *testing.T) {
    fx := scope.New(t)

    // 语义可读字段经 option 显式传；角色状态体现在构造函数名
    admin := seeders.SeedAdminUser(t, fx, seeders.WithEmail("alice@example.com"))

    // 配置外部 fake：用已绑定到 case 的字段 guid 做路由
    fakegrpc.RegisterGetUserInfo(t,
        fakegrpc.MatchGetUserInfo().Vendor("meross").Guid(admin.Guid),
        fakegrpc.UserInfoActive(fakegrpc.UserInfoActiveData{DisplayName: "Alice"}))

    resp := request.PostHTTPV1(t, "/user/v1/notify/switch",
        request.WithUID(admin.UID), request.WithToken(admin.Token),
        request.JSON(map[string]any{"forumNotify": 1}))
    requireSuccess(t, resp)

    // Then：和 fixture 对象字段比对
    got := observe.NotifySwitchInfo(t, admin.UID)
    require.Equal(t, 1, got.ForumNotify)

    // 业务契约要求触达论坛服务一次时才断言 fake 调用记录
    fakegrpc.AssertCalled(t, admin.Guid, "/forumpb.ForumService/GetUserInfo", 1)
}
```

演示了：PrimaryScope（admin.UID）由 fixture 注入、admin.Guid 是衍生身份 / fake routable field；语义可读字段显式声明；语义状态体现在构造函数名；fake match 锚定已绑定字段；断言用 fixture 对象字段。

## 10. 生成测试代码的决策顺序

1. 确认入口存在：HTTP route / gRPC service / MQ handler / task entry。
2. 反推业务契约和可观察面。
3. 梳理依赖：真实内部 IO、fake 外部依赖、时间 / 随机 / ID。
4. 检查测试包是否已声明必要 infra requirements。
5. 检查 infra 是否已具备运行依赖。
6. 检查 fake 是否已有目标外部服务和方法。
7. 检查 `fixture/schema` 是否已有所需资源声明。
8. 检查 `fixture/seeders` / `request` / `observe` 是否已有所需 helper。
9. 生成 entry 测试用例。
10. 运行编译、格式化和相关测试。

## 11. 不做的事

- 修改 SUT 源码以注入 mock；mock 本服务自己的 DB / Redis / Mongo / MQ。
- 每次运行全量创建所有表和 topic；在单 case 执行中惰性建表。
- 扫描测试源码自动推断资源依赖；经 seeder / recipe 间接声明 schema。
- 在普通 fixture 中提供面向业务数据隔离的 cleanup / reset API。
- 让 recipe 执行请求或断言。
- 断言中依赖 SQL 列 / Redis key / cache key / 内部调用次数 / 内部日志（外部 fake 调用记录只用于业务契约级触达）。
- 在 seeder 中为"会影响测试结果"的字段填隐藏默认值。
- 在用例中用字面值与 SUT 返回值比对（应与 fixture 对象字段比对）。
- 注册未包含已绑定跨域字段的 fake match 规则。
- 让 CaseScope 持有业务实体或表达业务实体关系。

## 12. 当前落地关系

当前仓库已有基础：`test/setup` 可迁移为 `test/infra`；`test/testutil/fake_*` 可迁移为 `test/fake`；后续新增 fixture 放 `test/fixture`、新增 API / 入口测试放 `test/entry`。迁移分阶段进行，不为目录重命名打断当前测试能力。建议演进顺序：

1. 保留现有测试能力，先引入 `infra.Run` 和包级 `Requirement` 模型。
2. 实现 file registry（`provision.lock` + `registry.json`）。
3. 将现有 fake server manager 迁入 `infra`，具体 fake 迁入 `fake`。
4. 引入 `fixture/scope` / `schema` / `seeders` / `request` / `observe`。
5. 新增测试优先落 `entry`，旧测试按需迁移。
