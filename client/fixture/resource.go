package fixture

import "context"

// Resource 表示一个可 provision 的基础设施资源（表、索引、命名空间、种子数据等）。
//
// mtkit 刻意不提供任何预定义 Resource 实现：provision 的形态由使用方的业务决定，无法预见也不应由
// 框架代维护；且这类实现的语义全在 DDL/建库/建 namespace 等具体动作里，封装成内置类型并无价值。
// 故框架只规定本接口，使用方按需自行实现（在 Apply 里用 env.MySQL(key).ExecContext(...) 等执行真正动作）。
//
// Apply 只在 Registry 互斥内、且 key 尚未注册时被调用一次；多条写入须用事务包装，
// 以保证失败回滚后可安全重入（key 未标记则下次重试）。
type Resource interface {
	// Key 返回账本中的持久化标识，推荐 "storage.schema.object"（如 mysql.mssiot_user.users）。
	// 重要：Key 一旦确定不可再改——改了等于换标识，旧账本条目永不命中，会触发重复 Apply。
	Key() string
	// Apply 真正建资源；env 提供懒连接的真实基础设施客户端。
	Apply(ctx context.Context, env *PersistEnv) error
}
