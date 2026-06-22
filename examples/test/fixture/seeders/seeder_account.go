// Package seeders 把 case 级业务数据写入 SUT 的真实存储（Given 的实现，你项目里的 test/fixture/seeders）。
//
// seeder 的本质是**不可重入**：每次调用都生成一条新记录 / 返回一组新值，框架（fixture 唯一值原语）
// 保证这些值跨用例、跨并发互不冲突。同一个 seeder 可被很多用例复用——它不是“某用例独占”，而是“每调一次
// 产一份新数据”。**若后续还要再用这些值（uid/guid/email…），由调用者自己保存返回的句柄**（seeder 不记忆）。
//
// 分工：seeder_* 是单实体构造器、recipe_* 是场景组合器，同包以文件名前缀区分（同包避免互相 import 成环）。
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

// Account 是 SeedAccount 返回的业务对象（调用者要复用就保存它）。
// 身份字段供 request helper 模拟网关注入；Nickname 是本次 seed 的实际持久化值——断言应与这些对象字段比对，
// 而非测试代码里的字面量（seeder 可能对值做唯一化改写）。
type Account struct {
	AccountID int64
	Guid      string
	Email     string
	Nickname  string
}

// Identity 把身份字段投影成 request 包所需的网关注入身份。
func (a *Account) Identity() request.Identity {
	return request.Identity{AccountID: a.AccountID, Guid: a.Guid}
}

type options struct{ nickname string }

// Option 以 functional options 表达 seeder 的语义字段值（默认全零，由 caller 显式打开关心的字段）。
type Option func(*options)

// WithNickname 设置初始昵称。
func WithNickname(v string) Option { return func(o *options) { o.nickname = v } }

// SeedAccount 造一个登录态账号：写 accounts（登录中间件隐式前置）+ profiles（昵称读写对象），返回业务对象。
//
// 每次调用都生成全新且不冲突的 accountID / guid / email：accountID 用唯一整数原语偏移成 10 位、guid 用
// 唯一字符串原语并**限长对齐权威 char(12)**（不能塞 32 位 UUID）。scope 在内部自取——用例无需感知发号细节。
func SeedAccount(t testing.TB, opts ...Option) *Account {
	t.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	cs := harness.Fixture(t).NewScope(t)
	a := &Account{
		AccountID: 1_000_000_000 + cs.UniqueInt(9),
		Guid:      cs.UniqueString(12, fixture.AlphaNum), // 对齐 accounts.guid CHAR(12) + UNIQUE
		Email:     cs.Email(),
		Nickname:  o.nickname,
	}
	insertAccount(t, a)
	insertProfile(t, a)

	// 关键字段打日志：go test -v 失败时直接可见，降低 debug 成本。
	t.Logf("seed: SeedAccount accountID=%d guid=%s nickname=%q", a.AccountID, a.Guid, a.Nickname)
	return a
}

// FreshAccount 造一个“已注册但无 profiles 行”的账号：只写 accounts，不写 profiles。
// 用于验证 SUT 首次读资料时的默认行为（惰性建默认行）。仍需 accounts，否则登录中间件查无账号而拒绝。
func FreshAccount(t testing.TB) *Account {
	t.Helper()
	cs := harness.Fixture(t).NewScope(t)
	a := &Account{
		AccountID: 1_000_000_000 + cs.UniqueInt(9),
		Guid:      cs.UniqueString(12, fixture.AlphaNum),
		Email:     cs.Email(),
	}
	insertAccount(t, a)
	t.Logf("seed: FreshAccount accountID=%d guid=%s (有账号，无 profiles 行)", a.AccountID, a.Guid)
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

func insertProfile(t testing.TB, a *Account) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db := harness.Persist(t).MySQL(contract.EnvKeyMySQLDSN)
	const stmt = `INSERT INTO profiles (account_id, nickname, update_time) VALUES (?,?,?)`
	if _, err := db.ExecContext(ctx, stmt, a.AccountID, a.Nickname, time.Now().Unix()); err != nil {
		t.Fatalf("seed: insert profiles accountID=%d: %v", a.AccountID, err)
	}
}
