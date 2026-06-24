// Package seeders 把 case 级业务数据写入 SUT 的真实存储（Given 的实现，你项目里的 test/fixture/seeders）。
//
// seeder 的本质是**不可重入**：每次调用都生成一条新记录 / 返回一组新值，框架（fixture 唯一值原语）
// 保证这些值跨用例、跨并发互不冲突。同一个 seeder 可被很多用例复用——它不是“某用例独占”，而是“每调一次
// 产一份新数据”。**若后续还要再用这些值（uid/guid/email…），由调用者自己保存返回的句柄**（seeder 不记忆）。
//
// 分工：seeder_* 是单实体构造器、recipe_* 是场景组合器，同包以文件名前缀区分（同包避免互相 import 成环）。
// **一个 seeder 只造一个实体**：底层是两张表（accounts 账号主表 + profiles 资料表）就拆成两个 seeder
// （seeder_account.go / seeder_profile.go）、两个返回 struct，别焊成一个——焊死会丢掉按需各自组合的弹性。
// 「账号 + 资料」这类高频组合由 recipe 封装（recipe_account.go 的 SeedAccountWithProfile）。
// seeder 对测试暴露业务语义字段，内部封装唯一化 / 关联 / 持久化等细节；**只做 Given，不声明 schema、不发请求、不断言**。
package seeders

import (
	"context"
	"testing"
	"time"

	fixture "github.com/McHarvvvy/mtkit/client/fixture"

	"shopdemo/test/contract"
	"shopdemo/test/fixture/harness"
	"shopdemo/test/fixture/request"
)

// Account 是 accounts（账号主表）实体的业务对象（调用者要复用就保存它）。
// 仅含身份字段，供 request helper 模拟网关注入；资料类字段（昵称等）属 profiles 表，由 Profile 承载、不混入此处。
type Account struct {
	AccountID int64
	Guid      string
	Email     string
}

// Identity 把身份字段投影成 request 包所需的网关注入身份。
func (a *Account) Identity() request.Identity {
	return request.Identity{AccountID: a.AccountID, Guid: a.Guid}
}

// SeedAccount 造一个登录态账号：只写 accounts（登录中间件的隐式前置），返回业务对象。
//
// 每次调用都生成全新且不冲突的 accountID / guid / email：accountID 用唯一整数原语偏移成 10 位、guid 用
// 唯一字符串原语并**限长对齐权威 char(12)**（不能塞 32 位 UUID）。scope 在内部自取——用例无需感知发号细节。
// 单独使用即「有账号、无 profiles 行」语义：用于验证 SUT 首次读资料时的默认行为（惰性建默认行）；
// 需要确定资料初值的用例改用 recipe SeedAccountWithProfile（账号 + 资料一并 seed）。
func SeedAccount(t testing.TB) *Account {
	t.Helper()
	cs := harness.Fixture(t).NewScope(t)
	a := &Account{
		AccountID: 1_000_000_000 + cs.UniqueInt(9),
		Guid:      cs.UniqueString(12, fixture.AlphaNum), // 对齐 accounts.guid CHAR(12) + UNIQUE
		Email:     cs.Email(),
	}
	insertAccount(t, a)

	// 关键字段打日志：go test -v 失败时直接可见，降低 debug 成本。
	t.Logf("seed: SeedAccount accountID=%d guid=%s (有账号，无 profiles 行)", a.AccountID, a.Guid)
	return a
}

func insertAccount(t testing.TB, a *Account) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 走进程级 PersistEnv（不在用例 cleanup 关闭）；列对齐权威 schema/sql/accounts.sql。
	db := harness.Persist(t).MySQL(contract.EnvKeyMySQLDSN)
	const stmt = `INSERT INTO accounts (account_id, guid, email, status, create_time) VALUES (?,?,?,?,?)`
	if _, err := db.ExecContext(ctx, stmt, a.AccountID, a.Guid, a.Email, 1, time.Now().Unix()); err != nil {
		t.Fatalf("seed: insert accounts accountID=%d: %v", a.AccountID, err)
	}
}
