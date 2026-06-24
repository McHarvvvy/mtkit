package http_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"shopdemo/test/fixture/request"
	"shopdemo/test/fixture/seeders"
)

// profile 是昵称的可观察视图，字段对齐 SUT getNickname 接口的响应契约。
type profile struct {
	Nickname string `json:"nickname"`
}

// setNickname 以 user 身份调更新昵称入口（When 的 helper，与前门读同处用例侧）。
func setNickname(t *testing.T, a *seeders.Account, name string) *request.Resp {
	t.Helper()
	return request.PostV1(t, "/shop/v1/profile/setNickname",
		map[string]any{"nickname": name}, request.AsUser(a.Identity()))
}

// getNickname 以 user 身份调前门 getNickname 读回当前昵称——**前门观察（有 set 就有 get，用 get 验 set）**。
// 读取失败或业务非成功直接 t.Fatal：观察通道本身不该失败，失败属用例前置 / 环境问题。
func getNickname(t *testing.T, a *seeders.Account) profile {
	t.Helper()
	resp := request.PostV1(t, "/shop/v1/profile/getNickname", struct{}{}, request.AsUser(a.Identity()))
	if !resp.IsSuccess() {
		t.Fatalf("读取昵称失败 accountID=%d apiStatus=%d info=%s", a.AccountID, resp.ApiStatus, resp.Info)
	}
	var got profile
	resp.DecodeData(t, &got)
	return got
}

// TestHTTP_Profile_SetThenGet
// 行为：登录用户改昵称后，再读返回新昵称（业务事实：写入对外可见）。
// 验证手段（前门 get 验 set + 预读→改→再读）：先 getNickname 把旧值灌入 SUT 读穿缓存 → setNickname 改新值
//
//	→ 再 getNickname 反向读，断言为新值。**预读这一步让“写后漏删缓存”类 bug 暴露**——否则再读会命中旧缓存
//	而沉默通过。断言对 seeder 返回对象 / set 的入参，不写裸字面值。
func TestHTTP_Profile_SetThenGet(t *testing.T) {
	t.Parallel()
	// recipe 组合「账号 + 资料」两个实体；seeder 每次都生成一组不冲突的新数据，故可放心 t.Parallel。
	user := seeders.SeedAccountWithProfile(t, seeders.WithNickname("old-name"))

	before := getNickname(t, user.Account) // 预读：把旧值灌入读穿缓存
	require.Equal(t, user.Profile.Nickname, before.Nickname, "预读应等于 seed 的初始昵称")

	const newName = "new-name"
	resp := setNickname(t, user.Account, newName)
	require.True(t, resp.IsSuccess(), "更新昵称应成功: apiStatus=%d info=%s", resp.ApiStatus, resp.Info)

	after := getNickname(t, user.Account)
	require.Equal(t, newName, after.Nickname, "改名后再读应为新值（含缓存已失效）")
}

// TestHTTP_Profile_FreshUserReturnsDefault
// 行为：读取从未写过资料的用户昵称，返回系统默认且不报错（SUT 首次读惰性建默认行）。
// 验证手段：只分配登录身份、不 seed profiles 行 → getNickname → 断言返回默认昵称。
func TestHTTP_Profile_FreshUserReturnsDefault(t *testing.T) {
	t.Parallel()
	// 只造账号、不 seed 资料行：SeedAccount 单实体即「有账号、无 profiles 行」——这是真实可达状态。
	user := seeders.SeedAccount(t)
	got := getNickname(t, user)
	require.Equal(t, "guest", got.Nickname, "无资料行时应返回系统默认昵称")
}

// TestHTTP_Profile_AnonymousRejected
// 行为：无有效登录身份调用更新昵称被拒绝（未登录）。
// 验证手段：不注入身份 → setNickname → 断言业务不成功。只咬对外契约（apiStatus），不咬内部实现。
func TestHTTP_Profile_AnonymousRejected(t *testing.T) {
	t.Parallel()
	resp := request.PostV1(t, "/shop/v1/profile/setNickname",
		map[string]any{"nickname": "x"}, request.Anonymous())
	require.False(t, resp.IsSuccess(), "无身份调用应被拒绝（未登录），实际 apiStatus=%d", resp.ApiStatus)
}
