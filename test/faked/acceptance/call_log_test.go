package acceptance

import (
	"context"
	stdhttp "net/http"
	"testing"
	"time"

	fakedclient "github.com/McHarvvvy/mtkit/client/fake"
	fakedhttp "github.com/McHarvvvy/mtkit/faked/http"
	"github.com/McHarvvvy/mtkit/test/faked/internal/testendpoint"
	"github.com/McHarvvvy/mtkit/test/faked/internal/testpb"
)

// TestCallLog_PathsAndGrowth 是 TC-10：调用日志路径与 runtime.json 声明一致 +
// engine 路径（命中 + rule 未注册的未命中）确实写日志。
//
// 共用 daemon 下不能用文件 size 增量判断——改为按本用例唯一 route_key 在两份
// jsonl 中分别确认"命中行"与"未命中行"存在（命中/未命中都带 route_key，可归因）。
func TestCallLog_PathsAndGrowth(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)

	grpcLog := h.addrs.GRPCCallLog
	httpLog := h.addrs.HTTPCallLog
	if grpcLog == "" || httpLog == "" {
		t.Fatalf("runtime.json missing log paths: grpc=%q http=%q", grpcLog, httpLog)
	}

	hitKey := caseKey(t) + "-hit"
	missKey := caseKey(t) + "-miss"

	// 命中 gRPC：注册 + 调用一次。
	h.client.GRPC().
		On(testendpoint.EndpointEcho, testpb.Echo_Ping_FullMethodName).
		RouteKey(hitKey).
		Static(&testpb.PingRes{Code: 1}).
		Apply(t)
	echo := h.dialEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := echo.Ping(ctx, &testpb.PingReq{Key: hitKey}); err != nil {
		t.Fatalf("grpc hit: %v", err)
	}
	// 未命中 gRPC（route_key 没注册）——engine 路径写日志。
	_, _ = echo.Ping(ctx, &testpb.PingReq{Key: missKey})

	h.requireGRPCHit(t, hitKey, "")
	h.requireGRPCMiss(t, missKey)

	// 命中 HTTP：注册 + 调一次。
	h.client.HTTP().
		On(testendpoint.HostAccTest, stdhttp.MethodPost, testendpoint.PathToken).
		RouteKey(hitKey).
		Static(fakedclient.Resp().Status(200).Body([]byte("ok"))).
		Apply(t)
	cl := h.httpClient(t)
	doForm(t, cl, "http://"+testendpoint.HostAccTest+testendpoint.PathToken, "client_id="+hitKey).Body.Close()
	// 未命中 HTTP（route 已声明但 rule 未注册）——engine 路径写日志、带 route_key。
	doForm(t, cl, "http://"+testendpoint.HostAccTest+testendpoint.PathToken, "client_id="+missKey).Body.Close()

	h.requireHTTPHit(t, hitKey, 200)
	h.requireHTTPMiss(t, "no-rule route_key="+missKey, func(ev fakedhttp.CallEvent) bool {
		return ev.RouteKey == missKey
	})
}

// TestCallLog_MissesLogged 验证三类 HTTP 未命中都写调用日志（事实 6 + §9.2）：
//
//	(1) host 未注册       —— proxy.dispatch 早退分支
//	(2) 路由未注册         —— mux 无匹配 pattern
//	(3) GenRouteKey 失败   —— /token 缺 client_id（此类 miss 日志 route_key 为空）
//
// 第 (3) 类的日志行没有 route_key、path 固定为 /token，在共用 daemon 的同一份日志里
// 无法按 key 归因。因此本用例改用独占 daemon（newIsolatedHarness）——私有日志文件里
// 按 host / path 匹配即可确定是本用例触发的 miss。
func TestCallLog_MissesLogged(t *testing.T) {
	t.Parallel()
	h := newIsolatedHarness(t)
	cl := h.httpClient(t)

	// (1) 未注册 host
	doForm(t, cl, "http://no-such.faked/x", "a=b").Body.Close()
	h.requireHTTPMiss(t, "unknown host", func(ev fakedhttp.CallEvent) bool {
		return ev.Endpoint == "no-such.faked"
	})

	// (2) 已注册 host 但未注册路由
	doForm(t, cl, "http://"+testendpoint.HostAccTest+"/never-declared", "a=b").Body.Close()
	h.requireHTTPMiss(t, "unknown route", func(ev fakedhttp.CallEvent) bool {
		return ev.Endpoint == testendpoint.HostAccTest && ev.Path == "/never-declared"
	})

	// (3) 路由已注册但 GenRouteKey 失败（/token 的 FormValue("client_id") 取不到）
	doForm(t, cl, "http://"+testendpoint.HostAccTest+testendpoint.PathToken, "no_client_id=1").Body.Close()
	h.requireHTTPMiss(t, "routekey extract fail", func(ev fakedhttp.CallEvent) bool {
		return ev.Endpoint == testendpoint.HostAccTest && ev.Path == testendpoint.PathToken && ev.RouteKey == ""
	})
}
