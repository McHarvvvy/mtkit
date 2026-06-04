package acceptance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	stdhttp "net/http"
	"testing"
	"time"

	grpcproto "github.com/McHarvvvy/mtkit/faked/grpc/protocol"
	httpproto "github.com/McHarvvvy/mtkit/faked/http/protocol"
	"github.com/McHarvvvy/mtkit/faked/test/internal/testendpoint"
	"github.com/McHarvvvy/mtkit/faked/test/internal/testpb"
)

// TestRegisterInvalid_GRPC 是 TC-05 的 gRPC 部分：非法 rule 注册阶段被拒绝。
//
// 断言具体契约错误码（设计 §6.4）+ HTTP status——每行非法点对应一个精确的拒绝
// 理由，只断言 4xx 大类会让等价类表失去意义（不同 bug 可能命中同一 4xx）。
// 只放过自由文本 message。
func TestRegisterInvalid_GRPC(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	ctx := context.Background()
	key := caseKey(t)

	// 合法 echo PingRes 的 base64 protobuf 字节——作为"合法字段"基线，隔离单一字段的非法性。
	validRespB64 := mustEncodeProto(t, &testpb.PingRes{Code: 1})
	staticData := func(respB64 string) json.RawMessage {
		return jsonRaw(t, grpcproto.StaticResponseData{Resp: respB64})
	}

	cases := []struct {
		name       string
		rule       grpcproto.Rule
		wantCode   string
		wantStatus int
	}{
		{
			name: "unknown-endpoint",
			rule: grpcproto.Rule{
				Endpoint:   "nonexist",
				FullMethod: testpb.Echo_Ping_FullMethodName,
				RouteKey:   key,
				Action:     grpcproto.Action{Kind: grpcproto.ActionKindStaticResponse, Data: staticData(validRespB64)},
			},
			wantCode:   grpcproto.ErrCodeInvalidEndpoint,
			wantStatus: stdhttp.StatusBadRequest,
		},
		{
			name: "unknown-method",
			rule: grpcproto.Rule{
				Endpoint:   testendpoint.EndpointEcho,
				FullMethod: "/x.y.Z/Bogus",
				RouteKey:   key,
				Action:     grpcproto.Action{Kind: grpcproto.ActionKindStaticResponse, Data: staticData(validRespB64)},
			},
			wantCode:   grpcproto.ErrCodeInvalidMethod,
			wantStatus: stdhttp.StatusBadRequest,
		},
		{
			name: "empty-route-key",
			rule: grpcproto.Rule{
				Endpoint:   testendpoint.EndpointEcho,
				FullMethod: testpb.Echo_Ping_FullMethodName,
				RouteKey:   "",
				Action:     grpcproto.Action{Kind: grpcproto.ActionKindStaticResponse, Data: staticData(validRespB64)},
			},
			wantCode:   grpcproto.ErrCodeInvalidRouteKey,
			wantStatus: stdhttp.StatusBadRequest,
		},
		{
			name: "unsupported-action",
			rule: grpcproto.Rule{
				Endpoint:   testendpoint.EndpointEcho,
				FullMethod: testpb.Echo_Ping_FullMethodName,
				RouteKey:   key,
				Action:     grpcproto.Action{Kind: "bogus", Data: json.RawMessage(`{}`)},
			},
			wantCode:   grpcproto.ErrCodeUnsupportedAction,
			wantStatus: stdhttp.StatusBadRequest,
		},
		{
			name: "invalid-action-data",
			rule: grpcproto.Rule{
				Endpoint:   testendpoint.EndpointEcho,
				FullMethod: testpb.Echo_Ping_FullMethodName,
				RouteKey:   key,
				// 故意缺 "resp" 字段
				Action: grpcproto.Action{Kind: grpcproto.ActionKindStaticResponse, Data: json.RawMessage(`{}`)},
			},
			wantCode:   grpcproto.ErrCodeInvalidActionData,
			wantStatus: stdhttp.StatusBadRequest,
		},
		{
			name: "invalid-resp-not-base64",
			rule: grpcproto.Rule{
				Endpoint:   testendpoint.EndpointEcho,
				FullMethod: testpb.Echo_Ping_FullMethodName,
				RouteKey:   key,
				Action:     grpcproto.Action{Kind: grpcproto.ActionKindStaticResponse, Data: staticData("!!!not-base64!!!")},
			},
			wantCode:   grpcproto.ErrCodeInvalidResp,
			wantStatus: stdhttp.StatusBadRequest,
		},
		{
			// error action 的 code=OK 会让 status.Error 返回 nil → 静默成功，必须在注册阶段拒绝。
			name: "error-code-ok",
			rule: grpcproto.Rule{
				Endpoint:   testendpoint.EndpointEcho,
				FullMethod: testpb.Echo_Ping_FullMethodName,
				RouteKey:   key,
				Action: grpcproto.Action{
					Kind: grpcproto.ActionKindError,
					Data: jsonRaw(t, grpcproto.ErrorData{Code: "OK", Message: "should be rejected"}),
				},
			},
			wantCode:   grpcproto.ErrCodeInvalidActionData,
			wantStatus: stdhttp.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := h.client.GRPC().RegisterRule(ctx, tc.rule)
			assertAPIError(t, err, tc.wantCode, tc.wantStatus)
		})
	}
}

// TestRegisterInvalid_HTTP 是 TC-05 的 HTTP 部分。
func TestRegisterInvalid_HTTP(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	ctx := context.Background()
	key := caseKey(t)

	validBody := base64.StdEncoding.EncodeToString([]byte("ok"))
	resp := func(status int, bodyB64 string) httpproto.Action {
		return httpproto.Action{
			Kind: httpproto.ActionKindStaticResponse,
			Data: jsonRaw(t, httpproto.HTTPResponseSpec{Status: status, BodyBase64: bodyB64}),
		}
	}

	cases := []struct {
		name       string
		rule       httpproto.Rule
		wantCode   string
		wantStatus int
	}{
		{
			name: "unknown-endpoint",
			rule: httpproto.Rule{
				Endpoint: "no-such.unknown", Method: stdhttp.MethodPost, Path: testendpoint.PathToken,
				RouteKey: key, Action: resp(200, validBody),
			},
			wantCode:   httpproto.ErrCodeInvalidEndpoint,
			wantStatus: stdhttp.StatusBadRequest,
		},
		{
			name: "invalid-route",
			rule: httpproto.Rule{
				Endpoint: testendpoint.HostAccTest, Method: stdhttp.MethodPost, Path: "/never-declared",
				RouteKey: key, Action: resp(200, validBody),
			},
			wantCode:   httpproto.ErrCodeInvalidRoute,
			wantStatus: stdhttp.StatusBadRequest,
		},
		{
			name: "invalid-status",
			rule: httpproto.Rule{
				Endpoint: testendpoint.HostAccTest, Method: stdhttp.MethodPost, Path: testendpoint.PathToken,
				RouteKey: key, Action: resp(600, validBody),
			},
			wantCode:   httpproto.ErrCodeInvalidStatus,
			wantStatus: stdhttp.StatusBadRequest,
		},
		{
			name: "invalid-body",
			rule: httpproto.Rule{
				Endpoint: testendpoint.HostAccTest, Method: stdhttp.MethodPost, Path: testendpoint.PathToken,
				RouteKey: key, Action: resp(200, "!!!not-base64!!!"),
			},
			wantCode:   httpproto.ErrCodeInvalidBody,
			wantStatus: stdhttp.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := h.client.HTTP().RegisterRule(ctx, tc.rule)
			assertAPIError(t, err, tc.wantCode, tc.wantStatus)
		})
	}
}

// TestRegisterInvalid_DuplicateRule 是 TC-06：重复注册同 RuleKey。
// 三通道协同：(a) 第二次返回 409 duplicate_rule；(b) 命中拿到第一次注册的 response。
func TestRegisterInvalid_DuplicateRule(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	key := caseKey(t)

	first := &testpb.PingRes{Code: 1, Msg: "first"}
	second := &testpb.PingRes{Code: 2, Msg: "second"}

	h.client.GRPC().
		On(testendpoint.EndpointEcho, testpb.Echo_Ping_FullMethodName).
		RouteKey(key).
		Static(first).
		Apply(t)

	// 第二次注册同 RuleKey、不同 response，期望 409 duplicate_rule。
	dupRule := grpcproto.Rule{
		Endpoint:   testendpoint.EndpointEcho,
		FullMethod: testpb.Echo_Ping_FullMethodName,
		RouteKey:   key,
		Action: grpcproto.Action{
			Kind: grpcproto.ActionKindStaticResponse,
			Data: jsonRaw(t, grpcproto.StaticResponseData{Resp: mustEncodeProto(t, second)}),
		},
	}
	err := h.client.GRPC().RegisterRule(context.Background(), dupRule)
	assertAPIError(t, err, grpcproto.ErrCodeDuplicateRule, stdhttp.StatusConflict)

	// 命中验证：拿到第一次的 response，第二次被拒绝、未替换 store。
	echo := h.dialEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := echo.Ping(ctx, &testpb.PingReq{Key: key})
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got.GetCode() != first.Code || got.GetMsg() != first.Msg {
		t.Fatalf("expected first response, got %v", got)
	}
}
