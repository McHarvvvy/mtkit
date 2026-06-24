package seeders

import "testing"

// AccountWithProfile 是 SeedAccountWithProfile 返回的场景对象：含账号身份（嵌入 *Account，其 Identity()
// AccountID 等可直接用）+ 已写入的资料行。断言资料字段走 .Profile。
type AccountWithProfile struct {
	*Account
	Profile *Profile
}

// SeedAccountWithProfile 组合「账号 + 资料」这一最常见场景：先 SeedAccount 写 accounts，再 SeedProfile
// 写 profiles，返回含两个实体的场景对象。资料 option 透传给资料 seeder。
//
// 名字显式表达它组合了账号 + 资料两个实体——不叫 SeedAccount（那是只造账号的单实体 seeder）：读者只读名字
// 就知道这个 Given 造了哪些表。需要确定资料初值的用例走本组合器；只读默认值的用例单独用 SeedAccount。
func SeedAccountWithProfile(t testing.TB, opts ...ProfileOption) *AccountWithProfile {
	t.Helper()
	a := SeedAccount(t)
	p := SeedProfile(t, a.AccountID, opts...)
	return &AccountWithProfile{Account: a, Profile: p}
}
