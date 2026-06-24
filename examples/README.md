# mtkit examples —— 推荐的集成测试落地骨架

这里是把 mtkit 接到一个真实服务上做 API 集成测试时，**项目侧测试代码该长什么样**的参照工程。
框架本身的能力与原理见上一层 [README](../README.md) 与 [docs/](../docs/)；本目录补足另一半：**你要在自己项目里写的那部分**。

## 怎么读

- 这是一个**可编译的玩具工程**：它有自己的 `go.mod`（module 名 `shopdemo`，`replace` 指向相邻的本地 mtkit），
  `go build ./...` / `go vet ./...` 全绿。作为嵌套 module，它被 mtkit 仓的 `go build/test ./...` 自动跳过，不污染框架。
- 业务是自拟玩具域 **`shop`**：登录态改 / 读昵称（`setNickname`/`getNickname`，走 MySQL + Redis 读穿缓存），
  外部依赖有 gRPC `loyalty` 与 HTTPS 汇率接口——刚好覆盖容器、provision、fake、前门观察、并发隔离等全部主题。
- 落到你项目时：把 `test/` 这套目录搬到 `<yourmodule>/test/` 下、改成你的 module 名；`api/`、`cmd/`、`schema/` 对应你项目自己的 proto、SUT、权威 schema。
- ⚠ `cmd/shop` 是**可编译的 SUT 骨架**（搭出端口与依赖读取的外形，业务逻辑留空）。所以 `go test` 不会开箱即过——要真跑通，得把 SUT 按注释补全。

## 工程布局

```
examples/                          # 玩具工程根（module shopdemo）
├── go.mod                         # module shopdemo; replace mtkit => ../
├── api/loyaltypb/                 # 你项目的 proto：fake/grpc/endpoint 与 SUT 都 import 它
├── cmd/shop/                      # 玩具 SUT：main.go 骨架 + mtkit_ca_addon.go（HTTPS faking 用）
├── schema/sql/                    # 根级“权威” DDL，provision 读盘原样执行
└── test/                          # ← 测试代码都在这（搬到你项目即 <yourmodule>/test/）
    ├── contract/                  # 最底层共享契约：env key、SUT 地址、runtime 路径
    ├── bootstrap/ fake/ fixture/ entry/
```

## 目录与主题映射

| 目录 | 主题 | 看点 |
| --- | --- | --- |
| [`test/bootstrap/`](test/bootstrap/) | TestMain 接入 / SUT 两种启动方式 | `run.go` 托管 SUT；`run_manual.go` 手动启动 SUT；`containers.go` 容器+SUTEnv 契约；`sut.go` boot plan 与 workdir 钉死 |
| [`schema/sql/`](schema/sql/) | 权威 schema | 玩具“权威” DDL，provision 读盘原样执行（别臆造 DDL） |
| [`test/fake/grpc/endpoint/`](test/fake/grpc/endpoint/) | fake gRPC endpoint（服务端） | 四步薄壳、无业务分支、route_key 取真实请求字段、`init()` 自注册；**用例侧用法见 [`profile_fake_test.go`](test/entry/http/profile_fake_test.go)** |
| [`test/fake/http/endpoint/`](test/fake/http/endpoint/) | fake HTTP(S) endpoint + CA addon | `Host+Routes+GenRouteKey`；HTTPS faking 需把 [`addons/`](../addons/) 的 CA 插件拷进 SUT（见 `cmd/shop/`）；**用例侧用法见 [`profile_fake_test.go`](test/entry/http/profile_fake_test.go)** |
| [`test/fake/grpc/rule/`](test/fake/grpc/rule/) | fake 规则注册封装（客户端·可选） | `StubLoyaltyPoints` 把 gRPC 规则注册收成可复用 helper；依赖 endpoint + harness + client/fake。HTTP 侧未抽、规则用例内联作对照——两种形态都覆盖 |
| [`test/contract/`](test/contract/) | 共享契约（最底层叶子） | env key、SUT 地址、runtime 路径集中一处；bootstrap 与各 fixture 包都依赖它，藉此打破反向依赖 |
| [`test/fixture/harness/`](test/fixture/harness/) | lazy 单例引导 | `sync.Once` 懒读 runtime；`Fixture`/`Persist`/`Fake` 进程级单例，绝不在用例 cleanup Close |
| [`test/fixture/schema/`](test/fixture/schema/) | provision 资源 | `funcResource` + 读权威 `.sql`；`Key()` 不可变、`Apply` 幂等 |
| [`test/fixture/seeders/`](test/fixture/seeders/) | case 级造数 | 一个 seeder 一个实体（`seeder_account.go`/`seeder_profile.go`），多实体组合走 `recipe_*`（`SeedAccountWithProfile`，名字看得出组合）；seeder 不可重入、每调一次生成不冲突新值、返回值要复用由调用者自存；只造数据 |
| [`test/fixture/request/`](test/fixture/request/) | 请求 helper | 签名 / 网关身份注入 / 响应解析收敛一处；低耦合 |
| [`test/fixture/peephole/`](test/fixture/peephole/) | 后门观察兜底 | 文档化空包：前门优先，后门万不得已才用 |
| [`test/entry/http/`](test/entry/http/) | 黑盒用例 | `profile_test.go` 前门 set→get + 预读暴露缓存；`profile_fake_test.go` fake gRPC/HTTP 用法；可达状态、`t.Parallel` |

## 依赖方向（无环）

`contract`（最底层共享契约）← 被所有人依赖；`bootstrap → contract + fake/<proto>/endpoint`；`harness/schema/seeders/request → contract`（seeders/request 另依赖 harness）；`fake/grpc/rule → endpoint + harness + client/fake`；`entry → bootstrap + fixture/* + fake/grpc/rule`。

`bootstrap` 是**顶层启动层**（blank-import `fake/grpc/endpoint`、`fake/http/endpoint` 触发 endpoint 自注册），它依赖别人、只被 `entry` 依赖——所以叫 bootstrap 而非 infra（它不是被依赖的底座）。**叶子包要的共享值（env key、地址、路径）一律放进最底层的 `contract`，谁都不反向 import `bootstrap`**，依赖方向自上而下、无环。

> fake 分两类、依赖方向相反：服务端 `endpoint`（fake 薄壳，被 daemon blank-import，只依赖 faked + pb）与客户端规则注册封装。本示例**两种注册形态都覆盖**：gRPC 侧把规则抽成 `fake/grpc/rule`（`StubLoyaltyPoints`，可复用）；HTTP 侧规则直接在用例里 inline 注册（最小形态）。按「规则是否要复用」二选一，见 [README §4](../README.md)。

## 两种 SUT 接入方式

- **mtkit 托管 SUT**（[`test/bootstrap/run.go`](test/bootstrap/run.go)）：`RegisterSUT` + boot plan，框架编译 / 启动 / 探活 / 停止。开箱即用，适合 CI 与日常跑测。
- **手动启动 SUT**（[`test/bootstrap/run_manual.go`](test/bootstrap/run_manual.go)）：mtkit 只拉基础环境（daemon + 容器 + Init）并常驻（长 IdleTTL、不带 boot plan）；你据 runtime 容器地址 / 固定端口自配并手动起 SUT，再跑 `go test`。适合断点调试 / 反复重启 SUT。

两种是你的选择，用例侧代码完全一致。
