# 归档摘要：faked 集成 mtkit 实施方案（P1）

> 历史实施方案的精简摘要。工作已落地，现状见 [modules/faked.md](../modules/faked.md)。原始全文不再保留。

## 目的

把 faked 引擎代码迁入 mtkit，并让 mtkit daemon **在自己进程内**直接托管 faked——替代原先「测试进程侧 fork」/「独立子进程」两种形态。

## 关键决策

- **内嵌而非独立子进程**（本方案最重要的设计修订）。原设计把 faked 定为「daemon 监督的独立子进程」，核心顾虑是 re-exec 下测试进程各自 fork 会产生 N 个实例。但该顾虑的真正解法是「由唯一的 daemon 拥有 faked」——daemon 进程内跑 faked 同样满足单实例，且更直接。「独立子进程」更强的两点收益经评估**不抵其复杂度**：
  - *依赖隔离*：re-exec 早已把 docker SDK 链进测试二进制，此处仅增量。
  - *廉价 reload*：改 endpoint 是低频场景（新增外部依赖才动），rule 是运行时注入、无需重启；为低频优化背「proc 监督 + 独立配置/runtime/发现链 + 跨平台进程树终止」整层不值。
  - 代价：放弃容器长活下的「秒级 reload faked」（日常 `go test`/CI 每次重编自动最新）；**本期不做 reload**。
- **配置/runtime 单一化，环境变量收窄到 2 个**：`MTKIT_CONFIG_PATH` + `MTKIT_RUN_MODE`。faked 无独立配置/runtime 文件——引擎参数进 `mtkit.config.json` 的 `faked` 块，地址进 `mtkit.runtime.json` 的 `faked` 段。`FAKED_*` 全删。
- **control 并入 daemon 主 mux**：rule 注册 handler（`/v1/grpc/rules`、`/v1/http/rules`）挂 daemon 主端口、与 lease/fixtured 共用端口与鉴权；只有**数据面**（每 fake gRPC 一个 listener + HTTP MITM 代理）仍是独立真端口。
- **`env_key` 从配置移到 `Endpoint.EnvKey()` 接口**：env 名是项目领域知识，不是运行时配置；与「endpoint 靠 `init()` 静态注册」风格一致。删配置里的 `http.endpoints`（改「注册即启用」）。
- **proc 引擎只服务 SUT**：`proc.Process`（泛化自 `sut.Manager`）承载 SUT 的 build/start/ready/stop + 进程树终止；faked 内嵌不走 proc。
- **HTTPS 信任：动态根 CA + SUT 程序级 `RootCAs`**：一张根 CA 通吃所有域名（握手时 per-host 动态签叶证书）；SUT 只拿公钥（`MTKIT_CA_CERT_FILE`），用模板插件 [`addons/mtkit_ca_addon.go`](../../addons/) 装入 `http.DefaultTransport.RootCAs`，跨平台一致、不写系统信任库、不用 `SSL_CERT_FILE`。生产构建（无 `-tags mtkit_it`）零足迹。
- **`NO_PROXY=127.0.0.1,localhost` 的语义**：`HTTP_PROXY` 指代理地址、`NO_PROXY` 过滤请求**目标**地址，二者不矛盾。由此引出硬约束：**被 fake 的 HTTP(S) 依赖必须用真实域名**，配成 `127.0.0.1` 会命中 NO_PROXY 绕过代理、fake 失效。
- **门面 API 改注册式**：`SUTBootPlan` 从 `Config` 移出并瘦身为 `Command` + `ProbeAddrs`（生命周期参数入配置）；`New(project, *Config)` + 链式 `RegisterContainers`/`RegisterSUT`。

## 最终状态

faked 已内嵌于 daemon、随 daemon 生命周期；control 面共用主端口、数据面独立监听；CA 动态生成 + addon 程序级信任。这套即当前实现（见 [modules/faked.md](../modules/faked.md)）。
