# 归档：历史实施方案（摘要）

本目录收录 mtkit 演进过程中的**实施方案文档**。它们记录的工作**均已落地**，其设计现状已由 [`architecture.md`](../architecture.md) 与 [`modules/`](../modules/) 反映。这里只保留**精简摘要**，留存「为什么这么做」的关键决策脉络，供回溯；不再维护、不作为当前事实来源。

> 当前事实以**代码**为权威，文档以架构/模块文档为准。本目录为历史快照。

| 文档 | 阶段 | 一句话 |
| --- | --- | --- |
| [mtkit-migration-plan.md](mtkit-migration-plan.md) | 段1 | testcontainerd 能力整体迁入 mtkit，解开 daemon「三股」为 container/sut/orchestrator |
| [faked-integration-plan.md](faked-integration-plan.md) | P1 | faked 由「测试进程 fork / 独立子进程」改为「内嵌 daemon 主进程」 |
| [mtkit-test-plan.md](mtkit-test-plan.md) | — | mtkit 自身的功能测试重构计划（~45 用例，已全绿） |
