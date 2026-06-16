# 模块设计：容器编排

`container` 包负责测试容器的声明、并发启停、幂等管理与孤儿清理，是 [testcontainers-go](https://github.com/testcontainers/testcontainers-go) 之上的一层薄编排。它只关心「一组相互独立的容器如何被可靠地拉起、初始化、注入 SUT、清理」，不替使用者决定用什么镜像——镜像与连接细节留在使用者提供的回调里。

跨模块的启停顺序、回滚、与就绪编排的关系见 [architecture.md §4.4](../architecture.md#44-首次-acquire跨模块就绪编排)；本文聚焦容器声明的契约与 `Bundle` 自身的行为。

---

## 1. 声明：ContainerRegistration 的三个回调

使用者通过 `RegisterContainers` 提供一组 `ContainerRegistration`，每项是一个容器的完整声明：

```go
type ContainerRegistration struct {
    Name   string                                                          // 唯一标识，用于回滚/清理/日志
    Start  func(ctx) (testcontainers.Container, error)                     // 创建并启动容器，返回句柄
    Init   func(ctx) error                                                 // 可选：所有容器起好后执行
    SUTEnv func(ctx, c testcontainers.Container) (map[string]string, error) // 可选：启动后产出注入 SUT 的 env
}
```

三个回调对应容器生命周期的三个时点，职责清晰分离：

- **`Start`**（必填）：创建并启动容器，返回句柄交框架托管。
- **`Init`**（可选）：在**所有**容器启动后执行，一般用于初始化 SUT 必须的外部数据资源。
- **`SUTEnv`**（可选）：在容器启动后由 daemon 调用，负责返回 SUT 需要的与这个容器有关的环境变量，最典型的就是容器的连接字符串

`SUTEnv` 产出的 key（如 `MYSQL_DSN`）正是 [fixture.md](fixture.md) 里 `PersistEnv` 按 `sutEnvKey` 索引连接、以及注入 SUT 进程的同一批 key。

---

## 2. 校验：单一真相 ValidateRegistrations

`ValidateRegistrations` 是注册合法性的**单一校验真相**，被门面 `New` 与 `NewBundle` 共用，避免规则两处漂移。规则：非空集合、每项 `Name` 非空、`Start` 非 nil、`Name` 不重复。门面在 `Run` 入口即校验，不合法直接返回 1、不进入 daemon。

---

## 3. 编排：Bundle 的行为契约

`Bundle` 管理这组容器的生命周期，对 daemon 暴露 `StartAll` / `StopAll` / `SUTEnv` / `Started` / `Containers`。其**契约**（写在 `bundle.go` 顶部）是理解它的关键：

> 容器被假定**相互独立**——`StartAll` 并发启动、**不保证注册顺序即启动顺序**；容器不得依赖另一容器「先就绪」。需要容器间初始化关系的，放进 `Init`（所有容器起好后串行执行）。

- **`StartAll`**：最多 4 个 worker（`maxCreateConcurrency`）并发创建；全部起好后**串行**执行各 `Init`。任一 `Start` 或 `Init` 失败 → 整体回滚（逆序 `Terminate` 已建容器）到干净态，保证下次重试从零开始。`started` 标志使重复调用幂等。
- **`StopAll`**：逆序 `Terminate`。逆序只是**礼貌性收尾**（后注册的通常更外围），**不承诺依赖拓扑语义**——若将来真有容器间启动依赖，应显式引入阶段/依赖模型，而非依赖这里的隐式顺序。
- **`SUTEnv`**：启动后收集各容器 `SUTEnv` 回调产物并合并；不同容器贡献**同名 key 视为开发期错误，直接报错**（拒绝静默覆盖）。回调可能做 Docker 调用（`MappedPort`），故不持锁执行。
- 并发与幂等由 `Bundle` 自己的 `b.mu` + `started` 负责，orchestrator 不再叠加 infra 锁（见 [architecture.md §4.5](../architecture.md#45-停止编排与并发分层)）。

---

## 4. 孤儿清理：CleanupOrphans

daemon **冷启动前**清理同名残留容器（`orchestrator.Daemon.Start` 第一步）：

- 容器名已与注册名绑定，若上次异常退出留下同名容器，Docker 会因 name conflict 拒绝创建新容器，整套测试拉不起来。
- 实现：列出所有容器（含已停止）→ 匹配注册名 → `ContainerRemove(Force, RemoveVolumes)` → **轮询 `Inspect` 直到 `NotFound`**。轮询是必要的：`ContainerRemove` 返回成功不代表底层立即删完，不等会与紧接着的 create 形成竞态。
- 该实现经 `orchestrator.WithOrphanCleaner` 可被注入 no-op——进程内编排测试借此与 Docker 解耦（见测试相关归档）。

---

## 5. 用法主线

```go
kit.RegisterContainers(func(ctx context.Context) ([]container.ContainerRegistration, error) {
    return []container.ContainerRegistration{
        {
            Name: "mysql",
            Start: func(ctx context.Context) (testcontainers.Container, error) {
                return testcontainers.GenericContainer(ctx, /* mysql 镜像请求 */)
            },
            Init: func(ctx context.Context) error {
                // 所有容器已起，可在此建库
                return nil
            },
            SUTEnv: func(ctx context.Context, c testcontainers.Container) (map[string]string, error) {
                host, _ := c.Host(ctx)
                port, _ := c.MappedPort(ctx, "3306/tcp")
                dsn := fmt.Sprintf("user:pass@tcp(%s:%s)/app", host, port.Port())
                return map[string]string{"MYSQL_DSN": dsn}, nil  // key 供 SUT / PersistEnv 使用
            },
        },
    }, nil
})
```

---

## 6. 注意事项

- **容器必须相互独立**：跨容器依赖放 `Init`，不要假设启动顺序。
- **连接串组装放 `SUTEnv`**：映射端口启动后才可知；这里产出的 key 是 SUT 注入与 `PersistEnv` 索引的同一批。
- **同名 env key 冲突报错**：多容器/多库靠不同 key 区分，框架不静默覆盖。
- **冷启动会强删同名容器**（`Force + RemoveVolumes`）：注册名应当是测试专用、可被随时清理的名字，勿与生产容器同名。
- **失败即整体回滚**：`Start`/`Init` 任一失败，已起容器全部 `Terminate`，环境回到干净态。
