package fixture

import "context"

// Resource 表示一个可 provision 的基础设施资源（表、索引、命名空间、种子数据等）。
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

type mysqlTableResource struct {
	sutEnvKey string
	key       string
	ddl       string
}

// MySQLTable 创建“建表”类 Resource。
// ddl 建议含 IF NOT EXISTS，使 Apply 在账本未标记时重入也幂等。
func MySQLTable(sutEnvKey, key, ddl string) Resource {
	return mysqlTableResource{sutEnvKey: sutEnvKey, key: key, ddl: ddl}
}

func (r mysqlTableResource) Key() string { return r.key }

func (r mysqlTableResource) Apply(ctx context.Context, env *PersistEnv) error {
	_, err := env.MySQL(r.sutEnvKey).ExecContext(ctx, r.ddl)
	return err
}

type redisNSResource struct {
	sutEnvKey string
	key       string
	ns        string
}

// RedisNS 声明一个 Redis 命名空间。
// Redis 无 schema 概念，命名空间无需预建，故 v1 Apply 刻意为 no-op——它只作文档性资源，
// 让“本测试包用到哪些 Redis 命名空间”在 requirements 里有显式声明。
func RedisNS(sutEnvKey, key, ns string) Resource {
	return redisNSResource{sutEnvKey: sutEnvKey, key: key, ns: ns}
}

func (r redisNSResource) Key() string { return r.key }

func (r redisNSResource) Apply(ctx context.Context, env *PersistEnv) error { return nil }
