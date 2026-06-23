// Package rule 是对同级 endpoint 的 fake 规则注册封装（客户端）。
//
// 把「给某 endpoint 预置响应」的注册逻辑收成可复用 helper，由 seeder / 用例调用——这是 README §4 里
// endpoint/rule 切分的客户端一侧。依赖 endpoint（取常量）+ harness（取 fake client）+ pb，单向、无环；
// **绝不被 endpoint 反向依赖**（endpoint 是被 daemon blank-import 的服务端叶子）。
//
// 对照：HTTP 侧（fake/http）本示例**未抽 rule 包**，规则直接在用例里 inline 注册（见
// entry/http/profile_fake_test.go）——两种形态都覆盖，按「规则是否要复用」二选一。
package rule

import (
	"strconv"
	"testing"

	loyaltypb "shopdemo/api/loyaltypb"
	"shopdemo/test/fake/grpc/endpoint"
	"shopdemo/test/fixture/harness"
)

// StubLoyaltyPoints 给指定 accountID 注册 loyalty.GetPoints 的固定积分响应。
//
// route_key = accountID 的十进制串：正是 SUT 调 loyalty 时带的 account_id（endpoint 薄壳从请求同一字段取），
// 每用例唯一、保证并发隔离。可被多个用例复用，故抽成本 helper。
func StubLoyaltyPoints(t testing.TB, accountID, points int64) {
	t.Helper()
	harness.Fake(t).GRPC().
		On(endpoint.EndpointLoyalty, loyaltypb.Loyalty_GetPoints_FullMethodName).
		RouteKey(strconv.FormatInt(accountID, 10)).
		Static(&loyaltypb.GetPointsRes{Points: points}).
		Apply(t)
}
