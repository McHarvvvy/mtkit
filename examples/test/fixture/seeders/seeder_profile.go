package seeders

import (
	"context"
	"testing"
	"time"

	"shopdemo/test/contract"
	"shopdemo/test/fixture/harness"
)

// Profile 是 profiles（资料表）实体的业务对象，承载某个账号的昵称等资料字段。
//
// AccountID 标明这份资料所属的账号——资料数据必须挂到某个具体账号上才有意义。Nickname 是本次 seed 的实际
// 持久化值；断言应与该对象字段比对，而非测试代码里的字面量（seeder 可能对值做唯一化改写）。
type Profile struct {
	AccountID int64
	Nickname  string
}

// ProfileOption 以 functional options 表达资料 seeder 的语义字段值（默认全零，由 caller 显式打开关心的字段）。
type ProfileOption func(*Profile)

// WithNickname 设置初始昵称。
func WithNickname(v string) ProfileOption { return func(p *Profile) { p.Nickname = v } }

// SeedProfile 为 accountID 标识的账号造一行资料：写 profiles（昵称读写对象），返回业务对象。
//
// 必须传入 accountID——资料要绑定在一个已存在的账号上才有意义（调用前应已 SeedAccount）。
func SeedProfile(t testing.TB, accountID int64, opts ...ProfileOption) *Profile {
	t.Helper()
	p := &Profile{AccountID: accountID}
	for _, opt := range opts {
		opt(p)
	}
	insertProfile(t, p)

	t.Logf("seed: SeedProfile accountID=%d nickname=%q", p.AccountID, p.Nickname)
	return p
}

func insertProfile(t testing.TB, p *Profile) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db := harness.Persist(t).MySQL(contract.EnvKeyMySQLDSN)
	const stmt = `INSERT INTO profiles (account_id, nickname, update_time) VALUES (?,?,?)`
	if _, err := db.ExecContext(ctx, stmt, p.AccountID, p.Nickname, time.Now().Unix()); err != nil {
		t.Fatalf("seed: insert profiles accountID=%d: %v", p.AccountID, err)
	}
}
