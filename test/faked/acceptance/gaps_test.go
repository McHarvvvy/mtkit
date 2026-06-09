package acceptance

import (
	"context"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fakedclient "github.com/McHarvvvy/mtkit/client/fake"
	"github.com/McHarvvvy/mtkit/faked"
	fakedhttp "github.com/McHarvvvy/mtkit/faked/http"
	"github.com/McHarvvvy/mtkit/faked/http/ca"
	"github.com/McHarvvvy/mtkit/test/faked/internal/testendpoint"
	"github.com/McHarvvvy/mtkit/test/faked/internal/testpb"
)

// TestFK01_GRPCErrorAction 验证 gRPC 独立 error action：命中后直接返回指定 status（F7）。
func TestFK01_GRPCErrorAction(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	key := caseKey(t)

	h.client.GRPC().
		On(testendpoint.EndpointEcho, testpb.Echo_Ping_FullMethodName).
		RouteKey(key).
		Error(codes.NotFound, "no echo here").
		Apply(t)

	echo := h.dialEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := echo.Ping(ctx, &testpb.PingReq{Key: key})
	if err == nil {
		t.Fatal("error action 命中应返回错误，得 nil")
	}
	st := status.Convert(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("status code 应为 NotFound，得 %s", st.Code())
	}
	if !strings.Contains(st.Message(), "no echo here") {
		t.Fatalf("status message 应含自定义文案，得 %q", st.Message())
	}
	h.requireGRPCHit(t, key, "") // 命中即可，不绑定 action_kind 文案
}

// TestFK02_HTTPMethodMismatchMiss 验证 method 不匹配按未命中归因（F7）。
//
// Given 端点为 /v1/login 注册了 POST 路由；When 对同路径发 PUT（method 不符）；
// Then fail-loud（>=400），且调用日志记一条 matched=false（带正确 method/path）。
// 用独占 daemon：该类 miss 无 route_key，需在私有日志里按 host+method+path 归因。
func TestFK02_HTTPMethodMismatchMiss(t *testing.T) {
	t.Parallel()
	h := newIsolatedHarness(t)
	cl := h.httpClient(t)

	req, err := stdhttp.NewRequest(stdhttp.MethodPut,
		"http://"+testendpoint.HostAccTest+testendpoint.PathLogin,
		strings.NewReader(`{"account_id":"x"}`))
	if err != nil {
		t.Fatalf("build PUT request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("PUT via proxy: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusBadGateway {
		t.Fatalf("method 不匹配 fail-loud 应为 502，得 %d", resp.StatusCode)
	}

	h.requireHTTPMiss(t, "method mismatch PUT /v1/login", func(ev fakedhttp.CallEvent) bool {
		return ev.Endpoint == testendpoint.HostAccTest &&
			ev.Method == stdhttp.MethodPut &&
			ev.Path == testendpoint.PathLogin
	})
}

// TestFK04_HTTPRouteKeyExactMatch 验证规则按 route_key 精确匹配、不向同路由其它 key 过度匹配（F7）。
//
// Given 在 PathLogin 注册 route_key=A 的规则；
// When 以 A 调用 → 命中返回 200；以未注册的 B 调用 → fail-loud（不落到 A 的规则上）。
func TestFK04_HTTPRouteKeyExactMatch(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	keyA := caseKey(t) + "-a"
	keyB := caseKey(t) + "-b" // 同路由、未注册的 key

	wantBody := []byte(`{"token":"exact"}`)
	h.client.HTTP().
		On(testendpoint.HostAccTest, stdhttp.MethodPost, testendpoint.PathLogin).
		RouteKey(keyA).
		Static(fakedclient.Resp().Status(200).Header("Content-Type", "application/json").Body(wantBody)).
		Apply(t)

	cl := h.httpClient(t)

	// A：命中返回 200。
	respA := doJSON(t, cl, "http://"+testendpoint.HostAccTest+testendpoint.PathLogin, fmt.Sprintf(`{"account_id":%q}`, keyA))
	bodyA, _ := io.ReadAll(respA.Body)
	_ = respA.Body.Close()
	if respA.StatusCode != 200 || string(bodyA) != string(wantBody) {
		t.Fatalf("key A 应命中：status=%d body=%q", respA.StatusCode, bodyA)
	}
	h.requireHTTPHit(t, keyA, 200)

	// B：同路由但 key 未注册 → 不应落到 A 的规则，fail-loud（>=400）。
	respB := doJSON(t, cl, "http://"+testendpoint.HostAccTest+testendpoint.PathLogin, fmt.Sprintf(`{"account_id":%q}`, keyB))
	_, _ = io.ReadAll(respB.Body)
	_ = respB.Body.Close()
	if respB.StatusCode != stdhttp.StatusBadGateway {
		t.Fatalf("未注册 key B 不应被 A 的规则命中，期望 502，得 %d", respB.StatusCode)
	}
}

// TestFK03_StaticCAMode 验证静态 CA 模式（CACertFile/CAKeyFile 都填）下 HTTPS MITM 正常（F7）。
//
// 区别于共用 engine 的动态生成 CA：此处预生成一对 CA 文件、独占 engine 走 ca.LoadFromFiles。
func TestFK03_StaticCAMode(t *testing.T) {
	t.Parallel()
	caDir := t.TempDir()
	_, certPath, keyPath, err := ca.Generate(caDir)
	if err != nil {
		t.Fatalf("预生成 CA: %v", err)
	}

	h := staticCAHarness(t, certPath, keyPath)
	key := caseKey(t)
	wantBody := []byte(`{"token":"static-ca"}`)
	h.client.HTTP().
		On(testendpoint.HostAccTest, stdhttp.MethodPost, testendpoint.PathLogin).
		RouteKey(key).
		Static(fakedclient.Resp().Status(200).Header("Content-Type", "application/json").Body(wantBody)).
		Apply(t)

	cl := h.httpClient(t)
	resp := doJSON(t, cl, "https://"+testendpoint.HostAccTest+testendpoint.PathLogin, fmt.Sprintf(`{"account_id":%q}`, key))
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("静态 CA HTTPS status 应为 200，得 %d", resp.StatusCode)
	}
	if string(body) != string(wantBody) {
		t.Fatalf("静态 CA HTTPS body mismatch：want %q got %q", wantBody, body)
	}
}

// staticCAHarness 起一个独占的、用静态 CA 文件的进程内 engine（自带 runtime 目录与调用日志）。
func staticCAHarness(t testing.TB, certFile, keyFile string) *harness {
	t.Helper()
	dir := t.TempDir()
	eng, err := faked.New(faked.Config{
		RuntimeDir:  dir,
		ProxyListen: "127.0.0.1:0",
		CACertFile:  certFile,
		CAKeyFile:   keyFile,
	})
	if err != nil {
		t.Fatalf("new static-CA engine: %v", err)
	}
	mux := stdhttp.NewServeMux()
	eng.RegisterControl(mux)
	srv := httptest.NewServer(mux)
	addrs, err := eng.Start(context.Background())
	if err != nil {
		srv.Close()
		t.Fatalf("start static-CA engine: %v", err)
	}
	t.Cleanup(func() {
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = eng.Stop(ctx)
	})
	return &harness{t: t, runtimeDir: dir, addrs: addrs, client: fakedclient.New(controlAddr(srv))}
}
