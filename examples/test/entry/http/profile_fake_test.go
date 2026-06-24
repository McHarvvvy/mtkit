package http_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	fake "github.com/McHarvvvy/mtkit/client/fake"

	grpcrule "shopdemo/test/fake/grpc/rule"
	httpfake "shopdemo/test/fake/http/endpoint"
	"shopdemo/test/fixture/harness"
	"shopdemo/test/fixture/request"
	"shopdemo/test/fixture/seeders"
)

// 本文件演示**用例侧如何用 fake server**：给 fake endpoint 注册 per-case 规则，再断言 SUT 的对外行为
// 反映了 fake 的应答。
//
// route_key 取值要点：它必须等于 SUT 真实出站请求里那个字段的值（gRPC 取 account_id、HTTP 取 query base），
// 引擎据此把「本用例注册的规则」与「SUT 实际发出的调用」精确配对。用 seeder 造出的唯一字段当 route_key，
// 既保证命中、又保证并发用例互不串扰。
//
// 两种规则注册形态都覆盖：gRPC 侧经 `fake/grpc/rule` 的复用封装（StubLoyaltyPoints）注册；HTTP 侧直接在
// 用例里 inline 注册——按「规则是否要复用」二选一（见 README §4「推荐测试目录结构」）。
//
// ⚠ 玩具 SUT（cmd/shop）是骨架，未实现 getProfile / localizedPrice 入口及其出站调用，故这两个用例
// 当前**只演示 fake 的注册与断言写法**；要真正 PASS，需把 SUT 补全到会发起对应出站调用。

// TestHTTP_Profile_GetProfile_FakesLoyaltyPoints 演示 **fake gRPC（规则抽成 rule 包）**：
// SUT 的 getProfile 内部调外部 loyalty.GetPoints 取积分——用 fake 让它返回固定值，断言出现在响应里。
func TestHTTP_Profile_GetProfile_FakesLoyaltyPoints(t *testing.T) {
	t.Parallel()
	user := seeders.SeedAccountWithProfile(t, seeders.WithNickname("alice"))

	const wantPoints int64 = 4321
	// 经 rule 包的复用封装注册（route_key = account id，每用例唯一）——演示「规则抽成 rule 包」的形态。
	grpcrule.StubLoyaltyPoints(t, user.AccountID, wantPoints)

	resp := request.PostV1(t, "/shop/v1/profile/getProfile", struct{}{}, request.AsUser(user.Identity()))
	require.True(t, resp.IsSuccess(), "getProfile 应成功: apiStatus=%d info=%s", resp.ApiStatus, resp.Info)

	var got struct {
		Nickname string `json:"nickname"`
		Points   int64  `json:"points"`
	}
	resp.DecodeData(t, &got)
	require.Equal(t, user.Profile.Nickname, got.Nickname, "昵称应为 seed 值")
	require.Equal(t, wantPoints, got.Points, "积分应来自被 fake 接管的 loyalty 出站调用")
}

// TestHTTP_Profile_LocalizedPrice_FakesExchangeRate 演示 **fake HTTP(S)（规则用例内联）**：
// SUT 的 localizedPrice 内部调外部汇率 HTTPS 接口——用 fake 让它返回固定汇率，断言出现在响应里。
func TestHTTP_Profile_LocalizedPrice_FakesExchangeRate(t *testing.T) {
	t.Parallel()
	user := seeders.SeedAccount(t)

	const base = "EUR"
	const wantRate = 1.08
	// HTTP 侧不抽 rule 包，直接 inline 注册（最小形态）。
	// route_key = base 币种：正是 SUT 调汇率接口时 query 里带的 base 值（见 fake/http/endpoint/exchange.go 的 GenRouteKey）。
	harness.Fake(t).HTTP().
		On(httpfake.HostExchange, http.MethodGet, "/v1/rate").
		RouteKey(base).
		Static(fake.Resp().Status(http.StatusOK).JSONBody(map[string]any{"base": base, "rate": wantRate})).
		Apply(t)

	resp := request.PostV1(t, "/shop/v1/profile/localizedPrice",
		map[string]any{"base": base}, request.AsUser(user.Identity()))
	require.True(t, resp.IsSuccess(), "localizedPrice 应成功: apiStatus=%d info=%s", resp.ApiStatus, resp.Info)

	var got struct {
		Rate float64 `json:"rate"`
	}
	resp.DecodeData(t, &got)
	require.Equal(t, wantRate, got.Rate, "汇率应来自被 fake 接管的 exchange HTTPS 出站调用")
}
