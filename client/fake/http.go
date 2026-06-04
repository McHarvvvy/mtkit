package fake

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	httpproto "github.com/McHarvvvy/mtkit/faked/http/protocol"
)

// httpRulesPath 是 HTTP 协议 rule 注册的 control API 路径。
const httpRulesPath = "/v1/http/rules"

// HTTPRespBuilder 构造单个 HTTP fake response，可被 Static / Sequence / Once 复用。
type HTTPRespBuilder struct {
	status    int
	statusSet bool
	headers   map[string]string
	body      []byte
	err       error
}

// Resp 创建一个 HTTP response builder。
func Resp() *HTTPRespBuilder {
	return &HTTPRespBuilder{}
}

// Status 设置响应状态码。
//
// 越界（不在 [100,599]）时只累积错误，不修改 b.status / b.statusSet：
// 一旦写入非法值，整个 builder 报废，Apply 时统一 fatal；后续 Status() 也无法"纠正"
// 已累积的错误——保持状态机单向。
func (b *HTTPRespBuilder) Status(status int) *HTTPRespBuilder {
	if status < 100 || status > 599 {
		b.addErr(fmt.Errorf("status %d out of range [100,599]", status))
		return b
	}
	b.status = status
	b.statusSet = true
	return b
}

// Header 设置单个响应头；同名 header 后写覆盖先写。
func (b *HTTPRespBuilder) Header(key, value string) *HTTPRespBuilder {
	if b.headers == nil {
		b.headers = make(map[string]string)
	}
	b.headers[key] = value
	return b
}

// Headers 合并一组响应头；同名 header 后写覆盖先写。
func (b *HTTPRespBuilder) Headers(headers map[string]string) *HTTPRespBuilder {
	for k, v := range headers {
		b.Header(k, v)
	}
	return b
}

// Body 设置原始响应体；body 在内部复制一份，调用方后续修改不会影响 builder。
func (b *HTTPRespBuilder) Body(body []byte) *HTTPRespBuilder {
	b.body = bytes.Clone(body)
	return b
}

// JSONBody 把 v marshal 成 JSON 响应体，并设置 Content-Type 为 "application/json"。
//
// 警告:会覆盖此前用 .Header("Content-Type", ...) 设置的同名头——若需要 charset 等
// 修饰，请在 JSONBody 之后再调 .Header 显式覆盖。
func (b *HTTPRespBuilder) JSONBody(v any) *HTTPRespBuilder {
	raw, err := json.Marshal(v)
	if err != nil {
		b.addErr(fmt.Errorf("marshal json body: %w", err))
		return b
	}
	return b.Body(raw).Header("Content-Type", "application/json")
}

// FormBody 把 values 编码为 application/x-www-form-urlencoded 响应体。
//
// 警告:与 JSONBody 相同——会覆盖此前用 .Header("Content-Type", ...) 设置的同名头。
func (b *HTTPRespBuilder) FormBody(values url.Values) *HTTPRespBuilder {
	return b.Body([]byte(values.Encode())).Header("Content-Type", "application/x-www-form-urlencoded")
}

func (b *HTTPRespBuilder) toSpec() (httpproto.HTTPResponseSpec, error) {
	if b == nil {
		return httpproto.HTTPResponseSpec{}, fmt.Errorf("nil response")
	}
	if b.err != nil {
		return httpproto.HTTPResponseSpec{}, b.err
	}
	if !b.statusSet {
		return httpproto.HTTPResponseSpec{}, fmt.Errorf("missing status")
	}
	headers := cloneHeaders(b.headers)
	return httpproto.HTTPResponseSpec{
		Status:     b.status,
		Headers:    headers,
		BodyBase64: base64.StdEncoding.EncodeToString(b.body),
	}, nil
}

func (b *HTTPRespBuilder) addErr(err error) {
	if err != nil && b.err == nil {
		b.err = err
	}
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = v
	}
	return out
}

// httpClient 在 control 基础之上固化 HTTP rule 注册路径。
type httpClient struct {
	ctrl *control
}

func newHTTP(ctrl *control) *httpClient { return &httpClient{ctrl: ctrl} }

// On 开始声明一条 HTTP rule 的 route 坐标。
func (c *httpClient) On(endpoint, method, path string) *httpRouteBuilder {
	return &httpRouteBuilder{client: c, endpoint: endpoint, method: method, path: path}
}

// RegisterRule 提交一条原始 HTTP rule DTO；底层 transport 直通。
func (c *httpClient) RegisterRule(ctx context.Context, rule httpproto.Rule) error {
	return c.ctrl.post(ctx, httpRulesPath, rule, nil)
}

type httpRouteBuilder struct {
	client   *httpClient
	endpoint string
	method   string
	path     string
}

// RouteKey 填入本 rule 的 route_key，并进入 action 选择阶段。
func (b *httpRouteBuilder) RouteKey(routeKey string) *httpRuleBuilder {
	return &httpRuleBuilder{
		client:   b.client,
		endpoint: b.endpoint,
		method:   b.method,
		path:     b.path,
		routeKey: routeKey,
	}
}

type httpRuleBuilder struct {
	client   *httpClient
	endpoint string
	method   string
	path     string
	routeKey string
}

// Static 注册 static_response。
func (b *httpRuleBuilder) Static(resp *HTTPRespBuilder) *httpStaticBuilder {
	spec, err := resp.toSpec()
	return &httpStaticBuilder{
		base: b,
		spec: spec,
		err:  wrapErr("encode static_response", err),
	}
}

// Sequence 注册 sequence_response：每次 Add 追加一份按顺序返回的 response。
func (b *httpRuleBuilder) Sequence() *httpSequenceBuilder {
	return &httpSequenceBuilder{base: b}
}

// Once 注册 once_response：首次返回 resp，第二次起返回 ThenRespond 指定的响应。
func (b *httpRuleBuilder) Once(resp *HTTPRespBuilder) *httpOnceBuilder {
	spec, err := resp.toSpec()
	return &httpOnceBuilder{
		base:  b,
		first: spec,
		err:   wrapErr("encode once_response", err),
	}
}

type httpStaticBuilder struct {
	base *httpRuleBuilder
	spec httpproto.HTTPResponseSpec
	err  error
}

// Apply 提交 static_response rule；失败时直接 t.Fatal。
func (b *httpStaticBuilder) Apply(t testing.TB) {
	t.Helper()
	if b.err != nil {
		t.Fatalf("faked client: %v", b.err)
	}
	// httpproto.StaticResponseData 是 HTTPResponseSpec 的 type alias，直接 marshal b.spec。
	data, err := json.Marshal(b.spec)
	if err != nil {
		t.Fatalf("faked client: encode static_response: %v", err)
	}
	rule := b.base.rule(httpproto.ActionKindStaticResponse, data)
	if err := b.base.client.RegisterRule(helperCtx(), rule); err != nil {
		t.Fatalf("faked client: register static_response: %v", err)
	}
}

type httpSequenceBuilder struct {
	base            *httpRuleBuilder
	responses       []httpproto.HTTPResponseSpec
	onExhausted     string
	onExhaustedResp *httpproto.HTTPResponseSpec
	err             error
}

// Add 追加一份 sequence_response 响应。
func (b *httpSequenceBuilder) Add(resp *HTTPRespBuilder) *httpSequenceBuilder {
	spec, err := resp.toSpec()
	if err != nil {
		b.addErr(wrapErr("encode sequence_response", err))
		return b
	}
	b.responses = append(b.responses, spec)
	return b
}

// RepeatLastOnExhausted 设置序列耗尽后重复最后一份 response。
func (b *httpSequenceBuilder) RepeatLastOnExhausted() *httpSequenceBuilder {
	b.onExhausted = httpproto.OnExhaustedRepeatLast
	return b
}

// ErrorOnExhausted 设置序列耗尽后返回指定的自定义响应（status + headers + body）。
// 与 gRPC 的 ErrorOnExhausted(code, msg) 对称——HTTP 的"错误"就是带特定 status 的响应。
func (b *httpSequenceBuilder) ErrorOnExhausted(resp *HTTPRespBuilder) *httpSequenceBuilder {
	b.onExhausted = httpproto.OnExhaustedError
	spec, err := resp.toSpec()
	if err != nil {
		b.addErr(wrapErr("encode on_exhausted_resp", err))
		return b
	}
	b.onExhaustedResp = &spec
	return b
}

// Apply 提交 sequence_response rule；失败时直接 t.Fatal。
func (b *httpSequenceBuilder) Apply(t testing.TB) {
	t.Helper()
	if b.err != nil {
		t.Fatalf("faked client: %v", b.err)
	}
	if len(b.responses) == 0 {
		t.Fatalf("faked client: sequence_response needs at least 1 response")
	}
	if b.onExhausted == "" {
		t.Fatalf("faked client: sequence_response needs on_exhausted; call RepeatLastOnExhausted or ErrorOnExhausted")
	}
	data, err := json.Marshal(httpproto.SequenceResponseData{
		Responses:       b.responses,
		OnExhausted:     b.onExhausted,
		OnExhaustedResp: b.onExhaustedResp,
	})
	if err != nil {
		t.Fatalf("faked client: encode sequence_response: %v", err)
	}
	rule := b.base.rule(httpproto.ActionKindSequenceResponse, data)
	if err := b.base.client.RegisterRule(helperCtx(), rule); err != nil {
		t.Fatalf("faked client: register sequence_response: %v", err)
	}
}

func (b *httpSequenceBuilder) addErr(err error) {
	if err != nil && b.err == nil {
		b.err = err
	}
}

type httpOnceBuilder struct {
	base       *httpRuleBuilder
	first      httpproto.HTTPResponseSpec
	second     httpproto.HTTPResponseSpec
	secondDone bool
	err        error
}

// ThenRespond 设置 once_response 在第二次及后续调用时返回的 HTTP response。
func (b *httpOnceBuilder) ThenRespond(resp *HTTPRespBuilder) *httpOnceBuilder {
	spec, err := resp.toSpec()
	if err != nil {
		if b.err == nil {
			b.err = wrapErr("encode once_response", err)
		}
		return b
	}
	b.second = spec
	b.secondDone = true
	return b
}

// Apply 提交 once_response rule；失败时直接 t.Fatal。
func (b *httpOnceBuilder) Apply(t testing.TB) {
	t.Helper()
	if b.err != nil {
		t.Fatalf("faked client: %v", b.err)
	}
	if !b.secondDone {
		t.Fatalf("faked client: once_response needs second-call behavior; call ThenRespond")
	}
	data, err := json.Marshal(httpproto.OnceResponseData{
		Response:     b.first,
		OnSecondCall: b.second,
	})
	if err != nil {
		t.Fatalf("faked client: encode once_response: %v", err)
	}
	rule := b.base.rule(httpproto.ActionKindOnceResponse, data)
	if err := b.base.client.RegisterRule(helperCtx(), rule); err != nil {
		t.Fatalf("faked client: register once_response: %v", err)
	}
}

func (b *httpRuleBuilder) rule(kind string, data json.RawMessage) httpproto.Rule {
	return httpproto.Rule{
		Endpoint: b.endpoint,
		Method:   b.method,
		Path:     b.path,
		RouteKey: b.routeKey,
		Action: httpproto.Action{
			Kind: kind,
			Data: data,
		},
	}
}
