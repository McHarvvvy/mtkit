package acceptance

import (
	"context"
	"io"
	stdhttp "net/http"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fakedclient "github.com/McHarvvvy/mtkit/client/fake"
	"github.com/McHarvvvy/mtkit/faked/test/internal/testendpoint"
	"github.com/McHarvvvy/mtkit/faked/test/internal/testpb"
)

// TestStateful_GRPC 是 TC-07 的 gRPC 部分：sequence/once 单 rule 行为。
// 共用 daemon 下每个 sub-test 用各自 caseKey 派生唯一 route_key，状态天然隔离。
func TestStateful_GRPC(t *testing.T) {
	t.Parallel()

	t.Run("sequence-repeat-last", func(t *testing.T) {
		t.Parallel()
		h := sharedHarness(t)
		key := caseKey(t)
		r1 := &testpb.PingRes{Code: 1, Msg: "r1"}
		r2 := &testpb.PingRes{Code: 2, Msg: "r2"}
		r3 := &testpb.PingRes{Code: 3, Msg: "r3"}
		h.client.GRPC().
			On(testendpoint.EndpointEcho, testpb.Echo_Ping_FullMethodName).
			RouteKey(key).
			Sequence().Add(r1).Add(r2).Add(r3).
			RepeatLastOnExhausted().
			Apply(t)

		want := []*testpb.PingRes{r1, r2, r3, r3, r3}
		got := pingNTimes(t, h, key, len(want))
		for i, w := range want {
			if got[i].GetCode() != w.Code || got[i].GetMsg() != w.Msg {
				t.Fatalf("call %d: want %v got %v", i, w, got[i])
			}
		}
	})

	t.Run("sequence-error", func(t *testing.T) {
		t.Parallel()
		h := sharedHarness(t)
		key := caseKey(t)
		r1 := &testpb.PingRes{Code: 1, Msg: "r1"}
		r2 := &testpb.PingRes{Code: 2, Msg: "r2"}
		// 用非默认 code 验证耗尽错误可定制（点 7-A）：耗尽后应返回 ResourceExhausted。
		h.client.GRPC().
			On(testendpoint.EndpointEcho, testpb.Echo_Ping_FullMethodName).
			RouteKey(key).
			Sequence().Add(r1).Add(r2).
			ErrorOnExhausted(codes.ResourceExhausted, "echo sequence exhausted").
			Apply(t)

		echo := h.dialEcho(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// 前 2 次成功
		if _, err := echo.Ping(ctx, &testpb.PingReq{Key: key}); err != nil {
			t.Fatalf("call 1: %v", err)
		}
		if _, err := echo.Ping(ctx, &testpb.PingReq{Key: key}); err != nil {
			t.Fatalf("call 2: %v", err)
		}
		// 第 3、4 次耗尽 → 自定义的 ResourceExhausted
		for i := 3; i <= 4; i++ {
			_, err := echo.Ping(ctx, &testpb.PingReq{Key: key})
			if err == nil {
				t.Fatalf("call %d: expected error after exhaustion, got nil", i)
			}
			if got := status.Code(err); got != codes.ResourceExhausted {
				t.Fatalf("call %d: expected ResourceExhausted, got %s", i, got)
			}
		}
	})

	t.Run("once", func(t *testing.T) {
		t.Parallel()
		h := sharedHarness(t)
		key := caseKey(t)
		first := &testpb.PingRes{Code: 1, Msg: "once"}
		h.client.GRPC().
			On(testendpoint.EndpointEcho, testpb.Echo_Ping_FullMethodName).
			RouteKey(key).
			Once(first).
			ThenError(codes.AlreadyExists, "consumed").
			Apply(t)

		echo := h.dialEcho(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		got, err := echo.Ping(ctx, &testpb.PingReq{Key: key})
		if err != nil {
			t.Fatalf("call 1: %v", err)
		}
		if got.GetCode() != first.Code || got.GetMsg() != first.Msg {
			t.Fatalf("call 1 mismatch: want %v got %v", first, got)
		}
		// 第 2、3 次：AlreadyExists
		for i := 2; i <= 3; i++ {
			_, err := echo.Ping(ctx, &testpb.PingReq{Key: key})
			if err == nil {
				t.Fatalf("call %d: expected error, got nil", i)
			}
			if c := status.Code(err); c != codes.AlreadyExists {
				t.Fatalf("call %d: expected AlreadyExists, got %s", i, c)
			}
		}
	})
}

// TestStateful_HTTP 是 TC-07 的 HTTP 部分：sequence(repeat-last / error) + once。
func TestStateful_HTTP(t *testing.T) {
	t.Parallel()

	t.Run("sequence-repeat-last", func(t *testing.T) {
		t.Parallel()
		h := sharedHarness(t)
		key := caseKey(t)
		h.client.HTTP().
			On(testendpoint.HostAccTest, stdhttp.MethodPost, testendpoint.PathToken).
			RouteKey(key).
			Sequence().
			Add(fakedclient.Resp().Status(200).Body([]byte("a1"))).
			Add(fakedclient.Resp().Status(503).Body([]byte("a2"))).
			RepeatLastOnExhausted().
			Apply(t)

		cl := h.httpClient(t)
		// 命中 4 次：期望 [200/a1, 503/a2, 503/a2, 503/a2]——耗尽后重复最后一份。
		wantStatus := []int{200, 503, 503, 503}
		wantBody := []string{"a1", "a2", "a2", "a2"}
		for i := 0; i < 4; i++ {
			status, body := hitForm(t, cl, key)
			if status != wantStatus[i] || body != wantBody[i] {
				t.Fatalf("call %d: want %d/%q got %d/%q", i+1, wantStatus[i], wantBody[i], status, body)
			}
		}
	})

	t.Run("sequence-error", func(t *testing.T) {
		t.Parallel()
		h := sharedHarness(t)
		key := caseKey(t)
		// 耗尽后返回自定义响应（504 + body），验证 ErrorOnExhausted 可定制（与 gRPC 对称）。
		h.client.HTTP().
			On(testendpoint.HostAccTest, stdhttp.MethodPost, testendpoint.PathToken).
			RouteKey(key).
			Sequence().
			Add(fakedclient.Resp().Status(200).Body([]byte("a1"))).
			Add(fakedclient.Resp().Status(503).Body([]byte("a2"))).
			ErrorOnExhausted(fakedclient.Resp().Status(504).Body([]byte("exhausted"))).
			Apply(t)

		cl := h.httpClient(t)
		type call struct {
			status int
			body   string
		}
		got := make([]call, 0, 3)
		for i := 0; i < 3; i++ {
			status, body := hitForm(t, cl, key)
			got = append(got, call{status, body})
		}
		// 期望：[200/a1, 503/a2, 504/exhausted]——第 3 次返回自定义耗尽响应。
		want := []call{{200, "a1"}, {503, "a2"}, {504, "exhausted"}}
		for i, w := range want {
			if got[i] != w {
				t.Fatalf("call %d: want %d/%q got %d/%q", i+1, w.status, w.body, got[i].status, got[i].body)
			}
		}
	})

	t.Run("once", func(t *testing.T) {
		t.Parallel()
		h := sharedHarness(t)
		key := caseKey(t)
		h.client.HTTP().
			On(testendpoint.HostAccTest, stdhttp.MethodPost, testendpoint.PathToken).
			RouteKey(key).
			Once(fakedclient.Resp().Status(200).Body([]byte("first"))).
			ThenRespond(fakedclient.Resp().Status(409).Body([]byte("consumed"))).
			Apply(t)

		cl := h.httpClient(t)
		statuses := []int{}
		for i := 0; i < 2; i++ {
			status, _ := hitForm(t, cl, key)
			statuses = append(statuses, status)
		}
		if statuses[0] != 200 || statuses[1] != 409 {
			t.Fatalf("statuses: want [200 409], got %v", statuses)
		}
	})
}

// TestStateful_CrossRuleIsolation 是 TC-08：两条 sequence rule 交叉命中、各自计数独立。
// 顺序 A→B→A→B 期望响应序列 a1, b1, a2, b2。
func TestStateful_CrossRuleIsolation(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	keyA := caseKey(t) + "-a"
	keyB := caseKey(t) + "-b"

	a1, a2 := &testpb.PingRes{Code: 11, Msg: "a1"}, &testpb.PingRes{Code: 12, Msg: "a2"}
	b1, b2 := &testpb.PingRes{Code: 21, Msg: "b1"}, &testpb.PingRes{Code: 22, Msg: "b2"}

	h.client.GRPC().
		On(testendpoint.EndpointEcho, testpb.Echo_Ping_FullMethodName).
		RouteKey(keyA).
		Sequence().Add(a1).Add(a2).ErrorOnExhausted(codes.FailedPrecondition, "a exhausted").
		Apply(t)
	h.client.GRPC().
		On(testendpoint.EndpointEcho, testpb.Echo_Ping_FullMethodName).
		RouteKey(keyB).
		Sequence().Add(b1).Add(b2).ErrorOnExhausted(codes.FailedPrecondition, "b exhausted").
		Apply(t)

	echo := h.dialEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	calls := []struct {
		key  string
		want *testpb.PingRes
	}{
		{keyA, a1}, {keyB, b1}, {keyA, a2}, {keyB, b2},
	}
	for i, c := range calls {
		got, err := echo.Ping(ctx, &testpb.PingReq{Key: c.key})
		if err != nil {
			t.Fatalf("call %d (%s): %v", i, c.key, err)
		}
		if got.GetCode() != c.want.Code || got.GetMsg() != c.want.Msg {
			t.Fatalf("call %d (%s): want %v got %v", i, c.key, c.want, got)
		}
	}
}

// pingNTimes 调 n 次 Echo.Ping(key)，返回 n 个响应；任一错误 t.Fatal。
func pingNTimes(t testing.TB, h *harness, key string, n int) []*testpb.PingRes {
	t.Helper()
	echo := h.dialEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := make([]*testpb.PingRes, 0, n)
	for i := 0; i < n; i++ {
		got, err := echo.Ping(ctx, &testpb.PingReq{Key: key})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		out = append(out, got)
	}
	return out
}

// hitForm 向 /token 发一次 form POST（client_id=key），返回 status 与 body 文本。
func hitForm(t testing.TB, cl *stdhttp.Client, key string) (int, string) {
	t.Helper()
	resp := doForm(t, cl, "http://"+testendpoint.HostAccTest+testendpoint.PathToken, "client_id="+key)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}
