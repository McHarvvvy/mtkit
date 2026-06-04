# mtkit/addons —— SUT 接入 mtkit 的插件模板

本目录放的是**供被测服务（SUT）拷贝使用的模板文件**，不是 mtkit 框架编译的一部分。
每个文件都是一段「SUT 为接入某项 mtkit 集成测试能力」而需要的代码——你按需把它
**拷贝到 SUT 自己的 `main` 包**即可，无需 import 本目录（这些文件用 `//go:build mtkit_it`
排除在 mtkit 自身构建之外）。

## 为什么是「拷贝」而不是「import」

这些能力必须在 **SUT 进程内**生效（SUT 是独立子进程，框架够不着它的进程内状态），
且要随 SUT 的 `main` 包一起、在 `main()` 前由 `init()` 自动执行。最简单直接的做法就是
把文件放进 SUT 的 main 包——它本就是 `package main`、自带 `//go:build mtkit_it`，拷过去
即用，不引入对 mtkit 子包的额外依赖。

## 用法

1. 把需要的插件文件（如 `mtkit_ca_addon.go`）**拷贝**到 SUT 的 `cmd/<sut>/` 下。
2. boot plan 构建 SUT 时加 `-tags mtkit_it`（mtkit 的 SUT boot plan 已默认这样做）。
3. 集成测试构建即编入该文件、`init()` 生效；生产构建（无该 tag）逐字节不含。

## 现有插件

### `mtkit_ca_addon.go` —— HTTPS MITM 的信任装填

faked 对 SUT 的 HTTPS 外部依赖做 MITM：握手时用动态根 CA 现签对应域名的叶证书。
**SUT 必须信任这张根 CA，握手才能完成**——否则代理截得到流量却建不起 TLS，fake 失效。

该文件的 `init()` 读环境变量 `MTKIT_CA_CERT_FILE`（由 mtkit 注入，值为 faked 动态 CA 的
公钥路径），把它追加进 `http.DefaultTransport` 的 `RootCAs`（从系统根池起步、只增不替）。

- 跨平台一致：不写系统信任库、不依赖 `SSL_CERT_FILE`（仅 Linux 生效，已弃用）。
- 注入方（项目 boot plan，注入 `MTKIT_CA_CERT_FILE`）与读取方（本文件）须用**同一 env 名**。
- 覆盖面：仅作用于走 `http.DefaultTransport` 的 client；SUT 若自建 `http.Transport` 并显式
  设 `TLSClientConfig`，需自行装 `RootCAs`。
- 时序：`init` 早于任何请求；SUT 不应在 init 阶段就发起需被 fake 的外部 HTTPS 请求。

设计依据见 `docs/faked-integration-plan.md` §7.4、`docs/faked-design.md` §4.5.1。
