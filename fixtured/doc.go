// Package fixtured 是 mtkit 的 daemon 侧 fixture 协调器，随主 daemon 进程运行，职责有二：
//
//	发号：Server 在 daemon 主端口暴露 /v1/fixture/alloc，由 Allocator 用 atomic FetchAdd 串行发放
//	      全局唯一、单调的 token 区块——这是测试数据唯一性的源头（client 侧 HiLo 缓存消费，详见 fixture-spec §1）。
//	账本重置：InitFileLedger 在 daemon 启动、对外可发现之前把 provision 账本写空，保证账本与容器数据同生命周期。
//
// 本包是纯 daemon 侧代码，与测试侧逻辑包 client/fixture 互不 import，唯一共享物是 fixtured/protocol leaf。
package fixtured
