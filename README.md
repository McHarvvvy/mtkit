# mtkit

mtkit 是 Go 应用的**集成测试基础设施框架**：在 `TestMain` 一个入口里，把被测服务（SUT）需要的真实依赖环境（容器 / fake 外部依赖 / 唯一数据 / 前置数据）一次性备好再跑测试，并让多个测试包跨进程复用同一套环境。原理与内部结构见 [docs/architecture.md](docs/architecture.md)。

> 本文只讲**推荐用法与注意事项**——你要做什么、不做会怎样。完整的落地骨架在 [examples/](examples/)（按主题分目录的参照代码），各模块原理在 [docs/modules/](docs/modules/)。

```bash
go get github.com/McHarvvvy/mtkit
```

**前置要求**：用到测试容器时需要本机可用的 Docker。若你完全不注册容器（只用 fake / 不依赖 DB 等），daemon 冷启动不碰 Docker，可无 Docker 运行。

---

## 1. 最小入口

一个测试包的 `TestMain` 只写一句 `kit.Run(m)`，框架自动分流 daemon / client 两种角色，你无感：

```go
func TestMain(m *testing.M) {
    kit, err := mtkit.New("myapp", &mtkit.Config{RuntimeDir: "test/.runtime/mtkit"})
    if err != nil { panic(err) }
    os.Exit(kit.
        RegisterContainers(registerContainers). // 声明依赖容器
        RegisterSUT(bootPlan).                   // 启用并托管 SUT（可选）
        WithSchema(resources).                   // 跨进程幂等装配前置数据（可选）
        Run(m))
}
```

`RuntimeDir` 是 `Config` **唯一必填**项；其余字段与默认值见下方各节与 godoc。`kit.Run(m)` 进入 `m.Run()` 时，容器已就绪、（若启用）SUT 与 faked 已就绪，并如实透传 `m.Run()` 的退出码。完整可参照的入口见 [examples/test/bootstrap/run.go](examples/test/bootstrap/run.go)。

---

## 2. 先选一种 SUT 接入方式

| 方式               | 怎么配                                                                                                                                                                                  | 适合                    |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------- |
| **mtkit 托管 SUT** | `RegisterSUT(plan)` + `SUT.Enabled`；框架编译 / 启动 / 探活 / 停止                                                                                                                      | CI、日常跑测，开箱即用  |
| **手动启动 SUT**   | **不**调 `RegisterSUT`、`SUT.Enabled` 关、daemon `IdleTTL` 设很长；mtkit 只拉基础环境（daemon + 容器 + `Init`）并常驻，你据 runtime 容器地址 / 固定端口自配并手动起 SUT，再跑 `go test` | 断点调试 / 反复重启 SUT |

两种方式用例侧代码完全一致。参照 [examples/test/bootstrap/run.go](examples/test/bootstrap/run.go)（托管）与 [run_manual.go](examples/test/bootstrap/run_manual.go)（手动）。

---

## 3. 接入前提（先确认，否则白跑）

mtkit 对 SUT 有硬性前提；不满足时，第一个碰到的用例会以**莫名的业务错误码 / 500** 暴露，排查成本极高。先确认：

- **SUT 要能在测试环境下启动起来**：容器、初始数据、fake 站点由 mtkit 备好后，SUT 必须能正常起来——前提是把 SUT 连的地址都指向这些备好的东西。要是 SUT 里有 mtkit 给不了、也模拟不了的组件（某个起不来的外部依赖、某段只在生产才成立的初始化），那就**入侵式地改 SUT 的代码**：加一个测试专用的构建分支或开关，把这块绕过或换掉，让它在测试环境下能起来。这是底线，起不来后面全免谈。
- **追完整条调用链，而非只看 handler 直接碰的表 / 服务**：中间件隐式依赖（如登录态网关反查账号表）、写后副作用（缓存失效、事件发送）、隐藏的跨服务调用，都要么用 fake 覆盖、要么确认 SUT 对其失败容错。这些隐式依赖也要落进前置数据（见 §5）。
- **HTTPS faking 需给 SUT 装 CA 插件**：faked 对出站 HTTPS 做 MITM、用动态 CA 现签证书，SUT 必须信任这张 CA 才能握手。把 [addons/](addons/) 的 CA 插件拷进 SUT main 包、以 `-tags mtkit_it` 构建（boot plan 默认已加）。仅 fake HTTPS 时需要；纯 gRPC / 明文 HTTP 不需要。
- **SUT 工作目录要显式钉死**：框架把 SUT 二进制建在 `RuntimeDir` 下、不在源码目录，故 boot plan 里 **不能由二进制路径推导工作目录**，必须显式把 `cmd.Dir` 设为 SUT 约定的工作目录，否则相对路径加载配置 / 资源会失败。见 [examples/test/bootstrap/sut.go](examples/test/bootstrap/sut.go)。

---

## 4. 推荐测试目录结构

把测试代码按职责分层、依赖方向无环——这是并发、复用、可维护的前提。[examples/](examples/) 是一个可编译的玩具工程，它的 `test/` 目录就是这套结构的活样例：

```
test/
├── bootstrap/   # 顶层启动层：TestMain 入口 Run、容器注册、SUT boot plan、RuntimePath
├── fake/        # 外部依赖 fake：grpc/ http/（每服务一文件，init() 自注册）
├── fixture/
│   ├── harness/   # 进程级单例引导（懒读 runtime → fixture.Client / PersistEnv）
│   ├── schema/    # provision 资源声明（一资源一函数）
│   ├── seeders/   # case 级造数（只做 Given）
│   ├── request/   # 请求 helper（签名 / 身份注入 / 响应解析）
│   └── peephole/  # 后门观察兜底（尽量少用）
└── entry/       # 入口黑盒测试：http/ grpc/ …（每包一个 TestMain）
```

依赖方向：`bootstrap → fake/* + schema`；`harness → bootstrap`；`seeders/request → harness`；`entry → bootstrap + fixture/*`。

---

## 5. 落地纪律（填缝时最容易错的地方）

每条都附「不做会怎样」，对应骨架在 examples。

- **fixture / fake / PersistEnv 客户端在用例里懒建，别在 `Run` 之前建。** 它们要读 runtime 信息，那是 `Run` 跑起来才写好的；等用例执行时它一定就绪，所以用包级 `sync.Once` 在第一次用到时建一份就好。这些客户端是进程级单例，**别在用例 cleanup 里 `Close`**——关掉会连累其他并发用例。见 [examples/test/fixture/harness](examples/test/fixture/harness/harness.go)。

- **前置数据分三层，别放错。** ① **容器 `Init`**——SUT 启动就必须有的东西（建库、基础 namespace），没有它 SUT 起不来；② **provision**（`WithSchema`）——测试用的表 / 字典 / 基准数据，跨用例共享、跨进程只建一次；③ **seeder**——每个用例自己要的那份新数据，用例里现造。归层判断：SUT 启动就要 → Init；多个用例共用且只建一次 → provision；每次要一份新的 → seeder。放错会出问题，最常见是把每用例数据塞进 provision（它只跑一次，给不了「每次一份新的」）。见 [docs/modules/fixture.md](docs/modules/fixture.md)。

- **provision 读权威 schema 原样执行，别臆造 DDL。** 建表前先找权威 schema 定义，有则原样读盘执行——臆造的宽松表会丢掉生产真实约束（唯一键、定长字段），让测试「对着错的表」绿、却在生产炸。`Resource.Key()` 一经确定不可改（改了等于换标识 → 重复 `Apply`），`Apply` 必须幂等可重入。见 [examples/test/fixture/schema](examples/test/fixture/schema/shop.go)。

- **seeder 不可重入，返回值要复用须自存。** seeder 每次调用都生成一组框架保证互不冲突的新值 / 新记录，可被多个用例复用；但它**不记忆**——之后还要用这些值（id/guid/email…），得由调用方自己存下它返回的对象。seeder 只负责造数据，不声明 schema、不发请求、不断言。见 [examples/test/fixture/seeders](examples/test/fixture/seeders/seeder_account.go)。

- **fake 规则必须带 per-case route_key。** gRPC 写 typed 薄壳严守「四步法」（收 typed 请求 → 取 route_key → 建 response 容器 → 调 `Engine.HandleUnary`，**不写业务分支**）；route_key 取真实请求字段，建议派生自 `t.Name()` + 原子序号。route_key 为空时引擎直接报错、不会悄悄放行——这是并发隔离的根基。endpoint 经 `init()` 自注册，TestMain blank-import 让 daemon 可见。见 [examples/test/fake](examples/test/fake/)、[docs/modules/faked.md](docs/modules/faked.md)。

- **断言只咬对外契约。** 验证优先用**前门**：尽量用 get 接口验 set 结果（前门读顺带验证缓存读穿、写后失效整条对外可见性）；前门读写在**用例侧**。直查存储（DB/Redis）是后门，万不得已才用——它耦合 SQL 列 / cache key，容易随重构碎掉，还会掩盖读链路自己的 bug。断言对 seeder 返回对象的字段、不写裸字面值；写后读关键路径用「预读→改→再读」暴露缓存未失效类 bug；只构造可达状态。见 [examples/test/entry/http](examples/test/entry/http/profile_test.go)。

> **为什么能并行**：隔离靠「命名空间不相交」而非加锁——并行用例各自从 fixture 取唯一值（同表不同行）、fake 规则各带 route_key（同路由按用例分流），故可放心 `t.Parallel()`。原理见 [docs/modules/fixture.md](docs/modules/fixture.md) 与 [faked.md](docs/modules/faked.md)。

---

## 6. 你要关心的环境变量

你真正要打交道的环境变量只有一类：**你和 SUT 之间的共享契约**——同一个名字，声明的一侧和 SUT 读取的一侧必须逐字对上。mtkit 自动注入的那些（出站代理、CA 路径、运行模式）你不用管，也不用知道。

| 变量                                                           | 你在哪声明    | 谁赋值             | 要点                                                    |
| -------------------------------------------------------------- | ------------- | ------------------ | ------------------------------------------------------- |
| fake endpoint 的 `EnvKey()`                                    | endpoint 里   | mtkit 注入直连地址 | 名字必须和 SUT 读这个依赖地址时用的名字一致             |
| 容器连接串 key（`SUTEnv` 回调吐出，如 `SHOP_IT_MYSQL_DSN`）    | containers 里 | mtkit 注入 SUT     | SUT 连容器、`PersistEnv` 取连接，用的是同一个 key       |
| SUT 切测试配置用的开关（若你的 SUT 靠环境变量区分测试 / 正式） | boot plan 里  | 你自己设           | 纯属你项目的约定，mtkit 不关心；用来让 SUT 加载测试配置 |

> SUT 自己监听的服务端口不在这张表里，也**不会出现在 runtime 文件里**——一个服务开几个端口、各是什么语义，mtkit 不替你猜。把它定义成测试侧的常量，给 boot plan 的 `ProbeAddrs`（就绪探测）和 request helper（拼请求基址）共用。别去 runtime 里找「SUT 端口」，那里没有。

---

## 7. 注意事项与排查

- **产物记得 gitignore**：runtime 文件、SUT/daemon 二进制、`logs/`、`faked/` 的 CA 全落在 `RuntimeDir` 下。把它放进测试目录（如 `test/.runtime/mtkit`）排查方便，一条目录级忽略就够：`/test/.runtime/`。

- **Docker 仅在注册容器时才需要**：daemon 冷启动会清理同名孤儿容器，但这步在「零注册容器」时直接短路返回、根本不创建 Docker 客户端。所以你注册了容器才需要本机有可用 Docker；只用 fake、不依赖容器时无 Docker 也能跑。
- **首跑超时**：首次会拉镜像 + 全量编译 SUT，可能远超 client 默认 acquire 超时（60s）。把 `ClientMode.HTTPTimeoutSeconds` 调大（如 300）并预拉镜像。镜像 tag 用本机 / CI 可得的版本，避免每次首拉。
- **daemon 复用 / 空闲回收**：`DaemonMode.IdleTTLSeconds` 控制空闲多久自动退出（连带停容器）。调短=本地快回收；调长=多包之间复用同一环境（复跑可从 30s 级降到 2s 级）。`go test ./...` 跨包并发共享一个 daemon + 一组容器（靠租约），前提是第 5 节的隔离纪律都守住。
- **装配非法直接返回 1**：未 `RegisterContainers`、容器重名、SUT 启用却没 `RegisterSUT` 等，`Run` 返回 1 且不跑用例。

| 现象                         | 先看哪里（`RuntimeDir` 下）                                                       |
| ---------------------------- | --------------------------------------------------------------------------------- |
| 用例报莫名业务错误码         | `logs/app.log`（SUT 真实堆栈：nil RPC client、缺表等）                            |
| daemon 起不来 / acquire 超时 | `logs/daemon.log` + `docker ps`（容器是否就绪）                                   |
| fake 没命中                  | `logs/{grpc,http}_calls.jsonl`（`matched=false` 即漏注册规则 / route_key 不匹配） |
| 连接信息对不上               | `mtkit.runtime.json` 的 `sut_env`（mtkit 实际注入 SUT 的 env 全集）               |
| HTTPS 握手失败               | 确认 CA 插件已拷入 SUT main 包、构建带 `-tags mtkit_it`                           |

---

## 8. 文档与示例导航

| 你想了解                          | 去哪                                                           |
| --------------------------------- | -------------------------------------------------------------- |
| 落地骨架（按主题的参照代码）      | [examples/](examples/)                                         |
| 宏观结构 / 进程分流 / 数据流      | [docs/architecture.md](docs/architecture.md)                   |
| daemon/client、租约、SUT/进程管理 | [docs/modules/orchestration.md](docs/modules/orchestration.md) |
| 容器声明与编排                    | [docs/modules/container.md](docs/modules/container.md)         |
| 唯一数据与前置数据装配            | [docs/modules/fixture.md](docs/modules/fixture.md)             |
| 假冒 gRPC / HTTP(S) 外部依赖      | [docs/modules/faked.md](docs/modules/faked.md)                 |
| 某术语到底指什么                  | [CONTEXT.md](CONTEXT.md)                                       |
