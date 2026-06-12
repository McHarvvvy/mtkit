// Package fixture 是 mtkit 的测试侧 fixture 工具，运行在测试（client）进程内，分两块职责：
//
//	生成：CaseScope 提供跨进程、跨用例、跨字段都唯一的测试数据（UniqueString/UUID/Email/Phone 等）。
//	      唯一性只在一处保证——daemon 串行发全局唯一 int64 token，每种格式是 token 到目标格式的单射（FPE），
//	      碰撞免于逐格式证明（详见 docs/fixture-spec.md §1）。
//	provision：Resource 声明前置数据（建表 / 种子），Registry（FileRegistry 文本账本）提供跨进程幂等执行，
//	      PersistEnv 提供进程级真实基础设施连接池。
//
// 本包与 daemon 逻辑包 fixtured 互不 import，唯一共享物是 fixtured/protocol leaf（wire 契约 + 账本契约）。
//
// 用法：调用方读 mtkit.runtime.json 拿到 Addr/Token，用 New 创建进程级 Client，再 NewScope(t) 派生 per-test 作用域；
// provision 经 mtkit.WithSchema 接入，在 client 模式 Acquire 成功后、m.Run() 前执行。
package fixture
