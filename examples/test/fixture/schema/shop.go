// Package schema 声明集成测试运行前需 provision 的资源（表 / 索引 / namespace / 只读种子）。
//
// 它只产出 mtkit 的 fixture.Resource 列表，由 bootstrap 经 kit.WithSchema 注册、框架在 m.Run 前
// 跨进程幂等执行一次；本包不持连接、不读 runtime。env key、仓库根等共享值取自最底层的 contract 包
// （**不反向 import bootstrap**，保持依赖自上而下）。每个存储域一个文件（shop.go / ...），集中该域的全部资源声明。
//
// 建表纪律（权威 schema 优先）：本玩具的“权威” DDL 在 examples/schema/sql/*.sql。provision 一律读这些
// 权威 .sql **原样执行**，保证测试建表与生产 schema 零漂移；绝不在此手抄 / 臆造 DDL——臆造的宽松表会
// 掩盖生产真实约束（如 UNIQUE(guid)、char(12)），让测试“对着错的表”绿、却在生产炸。.sql 不经 go:embed，
// 运行时读盘——权威 DDL 不编译进任何二进制，schema/ 保持纯 SQL。
package schema

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	fixture "github.com/McHarvvvy/mtkit/client/fixture"

	"shopdemo/test/contract"
)

// funcResource 是通用 Resource 实现：provision 语义全在 apply 闭包里。
// mtkit 刻意不提供任何内置 Resource 类型（形态由业务决定、无法预见），使用方自带一个最小实现即可。
// Key 用 "storage.db.object" 形式、**一经确定不可更改**（改了等于换标识，旧账本永不命中→重复 Apply）。
type funcResource struct {
	key   string
	apply func(ctx context.Context, env *fixture.PersistEnv) error
}

func (r funcResource) Key() string { return r.key }
func (r funcResource) Apply(ctx context.Context, env *fixture.PersistEnv) error {
	return r.apply(ctx, env)
}

// AccountsTable 声明账号主表 accounts——登录中间件反查它确认身份，是所有登录态入口测试的隐式前置。
func AccountsTable() fixture.Resource {
	return funcResource{key: "mysql.shop.accounts", apply: applyAuthoritativeSQL("accounts.sql")}
}

// ProfilesTable 声明资料表 profiles——setNickname / getNickname 的读写对象。
func ProfilesTable() fixture.Resource {
	return funcResource{key: "mysql.shop.profiles", apply: applyAuthoritativeSQL("profiles.sql")}
}

// applyAuthoritativeSQL 返回一个读 examples/schema/sql/<file> 权威 DDL 并原样整文件执行的 apply 闭包。
//
// 整文件 verbatim 执行：MySQL DSN 默认库 shop 且 multiStatements=true（见 bootstrap/containers.go），
// 故含 DROP / 多语句的权威 .sql 可整体执行、落入正确库。Apply 幂等：DROP TABLE IF EXISTS 安全——
// provision 经 Registry.EnsureOnce 每 daemon 只执行一次、且容器每次全新。
func applyAuthoritativeSQL(file string) func(ctx context.Context, env *fixture.PersistEnv) error {
	return func(ctx context.Context, env *fixture.PersistEnv) error {
		root, err := contract.RepoRoot()
		if err != nil {
			return err
		}
		path := filepath.Join(root, "schema", "sql", file)
		ddl, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read authoritative schema %s: %w", path, err)
		}
		// 连接经 PersistEnv 据 SUTEnv 的 key 取（不是容器名）；key 是 contract 里与 SUT 消费侧共享的契约。
		if _, err = env.MySQL(contract.EnvKeyMySQLDSN).ExecContext(ctx, string(ddl)); err != nil {
			return fmt.Errorf("apply authoritative schema %s: %w", file, err)
		}
		return nil
	}
}
