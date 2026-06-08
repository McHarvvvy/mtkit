package acceptance

import (
	"context"
	"testing"
	"time"

	grpcproto "github.com/McHarvvvy/mtkit/faked/grpc/protocol"
	"github.com/McHarvvvy/mtkit/test/faked/internal/testendpoint"
	"github.com/McHarvvvy/mtkit/test/faked/internal/testpb"
)

// TestDispatch_GRPCStaticHappy 是 TC-01：gRPC 注册-命中-日志 三角验证。
//
// 三通道断言：
//
//	(a) control 注册返回 200（builder.Apply 内部包装：失败即 t.Fatal）
//	(b) 真实 grpc.Dial 调 Echo.Ping → 响应字段与注册值逐字段相等
//	(c) grpc_calls.jsonl 中存在一行 matched==true、route_key==本用例 key、
//	    action_kind==static_response（按 key 归因，共用 daemon 下不污染）
func TestDispatch_GRPCStaticHappy(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	key := caseKey(t)

	want := &testpb.PingRes{Code: 7, Msg: "ok", Echo: "p"}

	h.client.GRPC().
		On(testendpoint.EndpointEcho, testpb.Echo_Ping_FullMethodName).
		RouteKey(key).
		Static(want).
		Apply(t)

	echo := h.dialEcho(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := echo.Ping(ctx, &testpb.PingReq{Key: key, Payload: "p"})
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}

	if got.GetCode() != want.Code || got.GetMsg() != want.Msg || got.GetEcho() != want.Echo {
		t.Fatalf("response mismatch: want {%d %q %q} got {%d %q %q}",
			want.Code, want.Msg, want.Echo,
			got.GetCode(), got.GetMsg(), got.GetEcho())
	}
	h.requireGRPCHit(t, key, grpcproto.ActionKindStaticResponse)
}
