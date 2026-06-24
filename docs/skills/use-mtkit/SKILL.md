---
name: use-mtkit
description: （Go 项目）用 mtkit 集成测试框架（github.com/McHarvvvy/mtkit）干活时的自洽操作手册：任务流程、决策分支、动手前的可行性闸门、填缝的思考方法。当用户要求"用 mtkit 接入集成测试"、"配 mtkit 的 TestMain / Config"、"注册容器 / SUT / provision 资源"、"组织 test 目录"、"加一个集成测试用例"或"排查 mtkit 的 daemon/SUT/fake 异常"时触发。
---

# 用 mtkit 干活

mtkit 是 Go 应用的集成测试基础设施框架（`TestMain` 一个入口备好容器 / fake / 唯一数据 / 前置数据，多包跨进程复用同一环境）。

**本 skill 不复述 mtkit 怎么用——那是 README 的事，且 README 会演进。** 本 skill 只管：接到任务后按什么顺序做、动手前先过哪个闸门、填缝时怎么思考。具体 API / 配置 / 纪律一律现读权威源，别凭记忆重建：

| 要查                          | 读这里（相对 `pkg/mtkit/`）                  |
| ----------------------------- | -------------------------------------------- |
| 推荐用法、API、配置、落地纪律 | `README.md`（最权威，凡冲突以它为准）        |
| 可编译的活样例（含目录分层）  | `examples/test/`                             |
| 原理 / 进程分流 / 数据流      | `docs/architecture.md`、`docs/modules/*.md`  |
| 术语到底指什么                | `CONTEXT.md`                                 |

---

## 心智模型（决定怎么排查）

mtkit = **机制框架 + 一组缝**。它包办编排 / 进程 / fake / 唯一数据 / 复用；每条机制留一条**要你用领域知识填的缝**，且**对 SUT 有硬性前提**。接入失败几乎都不是框架 bug，而是「缝没填」或「SUT 不满足前提」——排查时先怀疑这两类，别去翻框架源码。

---

## 权威 schema 来源是硬要求（最容易出大错的地方）

provision 建表、seeder 造数前，**必须先定位项目的权威 schema 来源**——生产 DDL、数据库迁移脚本、ORM / schema 定义等；具体在哪因项目而异，没有放之四海的固定路径，得在当前项目里找或问。

- **找到 → 原样使用**：严格按它的列、类型、约束建表 / 造数，不增删、不放宽。
- **找不到 / 不明确 → 停下来问用户**：明确索要权威来源在哪，**绝不凭代码推断或经验臆造 DDL**。动手建表 / 造数那一刻若来源仍不清，先提醒用户这个风险再继续，而不是先建一张「差不多」的表往下走。

**为什么这条值得打断流程（要让用户也理解）**：臆造或放宽的表会丢掉生产真实约束——唯一键、定长字段、非空、外键。测试于是「对着一张错的表」通过，这叫**事实偏移**：用例在测试里绿、却在生产炸；更糟的是生成的测试用例本身验证的就是错的契约，看着覆盖了、其实不准确。权威来源是这条链路不偏移的唯一锚点。

---

## 任务 A：全新把一个服务接入 mtkit

按这个顺序，**别跳过第 1 步直接写用例**——可行性闸门没过，后面全白写。

1. **先验可行性闸门：SUT 能不能在测试模式完整起来。** 这是底线，先回答再动手。
   - 追**完整调用链**列出隐藏依赖：中间件隐式依赖（如登录态网关反查账号表）、写后副作用（缓存失效 / 跨区域 cache 失效的 gRPC / 事件发送）、隐藏跨服务调用。产出一张清单：每个依赖 → 用容器 / 用 fake / 确认 SUT 对其失败容错 / 需改 SUT。
   - SUT 在测试模式必须把依赖**初始化齐全**（别为了图省事注释掉某个依赖的初始化，如某个 RPC client 的初始化），否则第一个碰到它的用例 nil-panic。给不了也模拟不了的组件 → 入侵式改 SUT（测试专用构建分支 / 开关绕过）。
   - **确认每个依赖 SUT 是怎么拿地址的**：能读环境变量 → 用 env 注入（动态端口，README 默认）；只认配置文件写死的地址、改不了 → 让 mtkit 参照 SUT 配置把容器 / fake 钉到固定端口（配置静态端口模式），**这种情况不必改 SUT**。两者可按依赖混用。别一看到「不支持 env」就跳到入侵式改 SUT——先分清是「拿地址方式不同」还是「真的给不了的组件」。详见 README「SUT 怎么发现依赖地址」。
   - 读 README「接入前提」一节逐条核对（含 HTTPS 的 CA 插件、SUT workdir 钉死）。
2. **搭骨架（目录是建议、不是强制）**：mtkit **不规定**目录 / 包名，那是项目自定义范畴。`examples/test/` 展示了一种经过验证的分层，**建议参考**，但要适配项目既有约定，不强制照搬。真正要守住的是可迁移的逻辑原则：**职责分层**（启动 / fake / 造数 / 请求 / 断言各司其职）、**依赖方向无环**、**共享契约（env key、地址、路径）下沉到最底层、不反向 import 启动层**——目录怎么叫无所谓。
   - **fake 内部再分两类、依赖方向相反**：服务端**自注册 endpoint**（fake 薄壳，被 daemon blank-import，只依赖 faked + pb）与客户端**规则注册封装**（依赖 harness + client）。别让服务端 endpoint 包拖进客户端依赖；规则少时直接在用例 inline 注册，出现可复用的规则封装时再抽成 rule 包（详见 README §4「推荐测试目录结构」）。
3. **自底向上跑通，每步验证再加料**：
   - 先只配容器 + `Init` + 启动入口，确认**空环境能起来**（手动 SUT 模式调试最省事）。
   - 再加 fake endpoint（→ 任务 C：填缝）、provision、seeder。
   - 最后写第一个用例，前门断言。
4. **验证收口**：`go build ./...`、`go vet ./...` 通过；首跑预拉镜像 + 调大 `ClientMode.HTTPTimeoutSeconds`（README「注意事项」）。

## 任务 B：给已接入的服务加用例 / 加外部依赖

- **加用例**：seeder 造数（用例里现造、返回值自存）→ request helper 发请求 → **前门断言**（get 验 set，对 seeder 返回字段断言，别写裸字面值）。
- **用例触达一个还没 fake 的外部依赖**：先按任务 C 填缝（加 endpoint），再在用例里注册**带 per-case route_key** 的规则（route_key = SUT 出站请求该字段的实际值，常用 seeder 的唯一字段）。
- **判断数据放哪层**（最常错）：SUT 启动就要 → 容器 `Init`；多用例共享且只建一次 → provision（`WithSchema`）；每用例要一份新的 → seeder。**别把每用例数据塞进 provision**（它只跑一次）。
- 加表 → provision，**先定位项目权威 schema 来源（见上节），原样使用，别臆造 DDL**。

## 任务 C：填缝（写 fake endpoint 等）

mtkit 把外部依赖的 fake 留成「缝」，怎么填**没有唯一模板**。综合三方面想清楚再写，别套个壳了事：

1. **看 examples 怎么写**——这些文件本身是带注释的范本：
   - `examples/test/fake/grpc/loyalty.go`：gRPC typed 薄壳「四步法」（收 typed 请求 → 取 route_key → 建 response 容器 → 调通用引擎，**不写业务分支**）+ `init()` 自注册 + `EnvKey()` 共享契约 + route_key 取真实请求字段。
   - `examples/test/fake/http/exchange.go`：HTTP endpoint（Host + Routes + 每路由的 RouteKey 提取函数）。
   - `examples/test/entry/http/profile_fake_test.go`：用例侧怎么注册规则、断言。
2. **结合项目特点**——SUT 实际怎么消费这个外部依赖：proto / HTTP 形态、读哪个 env key 拿依赖地址（若地址在 SUT 配置里写死、不读 env，则 fake `Listen()` 直接绑那个固定端口，见 README「SUT 怎么发现依赖地址」）、出站请求里哪个字段适合做 route_key。
3. **结合用户具体要求**——要 fake 哪些方法、要表达什么响应 / 错误行为、并发隔离的粒度。

权衡后给出贴合本项目的接缝方案。收口铁律：**route_key 必须 = SUT 出站请求该字段的实际值**（并发隔离根基；为空时引擎直接报错、不会悄悄放行）。**放置**：endpoint 薄壳放 `fake/<proto>/endpoint`（服务端、被 blank-import）；用例要复用的规则注册封装放 `fake/<proto>/rule`（客户端），见 README §4。

## 任务 D：排查失败

先按现象定位日志（产物全在你配置的 `RuntimeDir` 下），详表见 README「注意事项与排查」：

- 用例报莫名业务错误码 / 500 → `logs/app.log`（SUT 真实堆栈：nil client、缺表）。**这类几乎都是「前提没满足」**，回任务 A 第 1 步。
- daemon 起不来 / acquire 超时 → `logs/daemon.log` + `docker ps`。
- fake 没命中 → `logs/faked_{grpc,http}_calls.jsonl` 看 `matched=false`（漏注册 / route_key 不匹配）。
- 连接信息对不上 → `mtkit.runtime.json` 的 `sut_env`。

---

## 读完 README 仍易踩的几条（操作顺序坑）

这些不是 README 内容的复述，是 agent 实操时的顺序 / 优先级提醒：

1. **可行性闸门先于一切**：别被「先写几个用例看看」诱导——SUT 起不来时每个用例都报同一个莫名错误码，是纯浪费。先过任务 A 第 1 步。
2. **权威 schema 先于建表**：来源不清时先问、别先臆造（见上节，事实偏移代价极高）。
3. **客户端懒建、绝不在 `Run` 之前构造**：fixture / PersistEnv / fake 客户端读的 runtime 信息是 `Run` 内部才写的；包级 `sync.Once` 在用例首次用到时建。进程级单例，**别在 cleanup `Close`**。
4. **排查方向**：先怀疑「缝没填 / 前提没满足」，最后才怀疑框架。翻框架源码之前先看 `app.log`。
5. **断言只咬对外契约、前门优先**：直查 DB/Redis 是后门 last resort（耦合 SQL 列 / cache key、掩盖读链路 bug），确无前门才退到后门观察。
6. **产物 gitignore**：目录级忽略整个 `RuntimeDir`（含 bin/logs/faked 子目录），别只忽略单个文件名。

---

## 收尾检查

- [ ] 任务 A：可行性闸门过了（SUT 测试模式能完整起来、隐藏依赖已 fake/容错）
- [ ] 建表 / 造数前定位到项目权威 schema 来源并原样使用；来源不清时已问用户、未臆造 DDL
- [ ] 数据分对三层（Init / provision / seeder）
- [ ] fake 规则带 per-case route_key（= SUT 出站字段实际值）
- [ ] 断言走前门、对 seeder 返回字段而非裸字面值
- [ ] 客户端懒建、不在 cleanup `Close`；整个 `RuntimeDir` 已 gitignore
- [ ] `go build ./...` 与 `go vet ./...` 通过
