# 归档摘要：mtkit 迁移实施方案（段1：testcontainerd → mtkit）

> 历史实施方案的精简摘要。工作已落地，现状见 [architecture.md](../architecture.md)。原始全文不再保留。

## 目的

把已有 `testcontainerd` 的能力整体迁入新包 `mtkit`（`github.com/McHarvvvy/mtkit`，独立 git repo / Go module），并按目标分层落成骨架。**只迁移 + 切骨架，行为等价，不泛化监督逻辑**（ManagedProcess 留到后续）。

## 关键决策

- **另起 mtkit 而非原地改 testcontainerd**：testcontainerd 在用、API 改动大，原地改会破坏现有测试；新包无包袱，按目标布局立骨架。开发期本地 checkout 放 `mssiot_user/pkg/mtkit`，业务仓用 `replace` 指向。
- **解开 daemon「三股」**：原 `daemon/` 把容器编排、SUT 监督、daemon 基座缠在一起。段1 本质就是解开三股，归到正确层：
  - 容器编排（bundle/cleanup/infra 起停）→ `container/`
  - SUT 监督（sut_manager + 进程控制）→ `sut/`（段1 临时含进程控制，后抽 `proc/`）
  - daemon 基座（HTTP control + lease store + reaper + 模式分流）→ **新增的 `orchestrator/`**（最高编排层）
- **依赖方向铁律**：`container`/`sut`/`fixtured` 不得 import `orchestrator`；**顶层 `client/`（消费端）不被任何服务端 import**，只走各 leaf `protocol` + `runtime` + `constant`；**client-mode 是 orchestrator 的内部子包 `orchestrator/client`**（消除「daemon 侧 import 消费端」坏味道）。由 `test/arch_test.go` 架构守护测试回归校验。
- **lease 协议归 `orchestrator/protocol`**；fixtured 协调路由与 lease **共用 daemon 主端口同一 listener**。
- **常量不兼容 `TCD_*`**：`MT_*` 等基座常量重立，不双读旧值，剔除项目领域容器常量。

## 最终状态

daemon「三股」已解开为 container/sut/orchestrator；依赖方向由架构守护测试固化；client-mode 内聚于 orchestrator/client。这套分层即当前代码结构（见 [architecture.md §2](../architecture.md#2-宏观模块图)）。后续阶段（faked 内嵌、fixtured 协调原语、client/fixture 实现）见各自方案与模块文档。
