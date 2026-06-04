package acceptance

import (
	"context"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/McHarvvvy/mtkit/faked/test/internal/testendpoint"
	"github.com/McHarvvvy/mtkit/faked/test/internal/testpb"
)

// TestFailLoud_GRPC 是 TC-04 的 gRPC 部分：未命中 fail-loud。
// 不注册任何 rule（或注册了无关 rule），断言 gRPC 调用返回 FailedPrecondition。
// 不断言日志——proxy/mux 早退分支与 engine 分支的日志行为不一致，统一在 TC-10 验证。
func TestFailLoud_GRPC(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	echo := h.dialEcho(t)

	cases := []struct {
		name     string
		key      string
		wantCode codes.Code
	}{
		{
			name:     "no-rule",
			key:      caseKey(t) + "-no-such-key", // 唯一且未注册 → fail-loud
			wantCode: codes.FailedPrecondition,
		},
		{
			name:     "empty-route-key",
			key:      "", // 薄壳把空 key 透传给 engine，触发 "empty fake route_key"
			wantCode: codes.FailedPrecondition,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := echo.Ping(ctx, &testpb.PingReq{Key: tc.key})
			if err == nil {
				t.Fatalf("expected error %s, got nil", tc.wantCode)
			}
			if got := status.Code(err); got != tc.wantCode {
				t.Fatalf("expected code %s, got %s (err=%v)", tc.wantCode, got, err)
			}
		})
	}
}

// TestFailLoud_HTTP 是 TC-04 的 HTTP 部分：未命中 fail-loud。
// daemon 早退路径返回 502（未注册 host）或 404（未注册 path）；engine 路径返回 502（rule 未注册）。
// 所有路径都不写成功响应——SUT 拿到的状态码必然 >= 400。
func TestFailLoud_HTTP(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	cl := h.httpClient(t)
	key := caseKey(t)

	cases := []struct {
		name       string
		url        string
		body       string
		wantStatus int
	}{
		{
			name:       "unknown-host",
			url:        "http://" + key + ".no-such.faked/anything",
			wantStatus: stdhttp.StatusBadGateway,
		},
		{
			name:       "unknown-route",
			url:        "http://" + testendpoint.HostAccTest + "/unknown/" + key,
			wantStatus: stdhttp.StatusBadGateway, // 未注册路由统一收敛为 502 并记调用日志
		},
		{
			name:       "no-rule",
			url:        "http://" + testendpoint.HostAccTest + testendpoint.PathToken,
			body:       "client_id=" + key,
			wantStatus: stdhttp.StatusBadGateway,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			method := stdhttp.MethodGet
			var body *strings.Reader
			if tc.body != "" {
				method = stdhttp.MethodPost
				body = strings.NewReader(tc.body)
			}
			var req *stdhttp.Request
			var err error
			if body != nil {
				req, err = stdhttp.NewRequest(method, tc.url, body)
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			} else {
				req, err = stdhttp.NewRequest(method, tc.url, nil)
			}
			if err != nil {
				t.Fatalf("build req: %v", err)
			}
			resp, err := cl.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status: want %d got %d", tc.wantStatus, resp.StatusCode)
			}
		})
	}
}
