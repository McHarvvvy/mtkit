# provision Registry 后端用文本账本，接口坍缩为 EnsureOnce

provision Registry 负责跨进程幂等协调（"某个资源只 Apply 一次"）。最初规格草案选择 MySQL 专用元表作后端——利用行锁做分布式互斥、账本落同库。这个方案被否决，原因是**循环依赖**：Registry 必须在任何资源被 provision 之前就可用，但 MySQL Registry 本身依赖 MySQL 连接，导致"用 MySQL 来 provision MySQL"的死锁启动序列。

改为**文本账本（JSON 文件）**：daemon 在 `Start()` 时在 RuntimeDir 写入空的 `mtkit.provision.json`，生命周期与 daemon/容器绑定（daemon 重启 → 文件重置 → 容器重建，账本与数据同步清零）。多测试进程通过整文件锁（flock/LockFileEx）序列化读写，粒度粗但 provision 是低频启动操作，并发收益可忽略。文本账本对任何技术栈（MySQL、Redis、MongoDB 等）无依赖，彻底解耦。

Registry 接口同步从三方法（`WithLock` + `Applied` + `MarkApplied`）坍缩为单方法：

```go
EnsureOnce(ctx context.Context, key string, fn func() error) (skipped bool, err error)
```

三方法分离把"原子检查+执行+标记"的责任分散给调用方，每个后端都要在接口缝隙里重建原子性。`EnsureOnce` 把原子性下推给后端（文件锁包住全程；若将来换 Redis，用 Lua 脚本；换 MySQL，用事务），接口语义更紧，后端替换不破坏契约。`skipped=true` 表示 fn 被跳过（key 已应用），供审计/监控使用；`error` 仅在 fn 执行失败或基建故障时非 nil，不用于表达幂等跳过这一正常路径。

## Considered Options

- **MySQL 元表**：否决，循环依赖无法解决启动序列问题。
- **daemon-side HTTP 服务**（Registry 走 daemon 端点）：架构一致性更强，但需新增 HTTP 接口，实现成本高于文件方案，且 provision 这类低频操作不需要 daemon 介入读写。
