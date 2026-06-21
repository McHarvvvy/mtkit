// Package request 封装 HTTP 入口请求的重复细节：业务负载签名、网关身份注入、统一响应解析
// （你项目里的 test/fixture/request）。
//
// 用例只关心“打哪个 path、带什么负载、以谁的身份”，而签名算法、网关注入的 header、响应外壳解析这些
// 与具体业务无关的细节集中在此。**helper 默认只依赖 accountID/guid 等基础身份字段，不依赖 seeder 返回的
// 复杂业务对象**，以保持低耦合。
package request

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"shopdemo/test/bootstrap"
)

// Resp 是统一响应外壳的解析结果。Data 留作 RawMessage，按需经 DecodeData 解析成业务结构。
type Resp struct {
	HTTPStatus int             // 传输层状态码（业务错误也常是 200，真正语义看 ApiStatus）
	ApiStatus  int             `json:"apiStatus"`
	Info       string          `json:"info"`
	Data       json.RawMessage `json:"data"`
}

// IsSuccess 报告业务是否成功（统一外壳以 apiStatus==0 表示成功）。
func (r *Resp) IsSuccess() bool { return r.ApiStatus == 0 }

// DecodeData 把 data 解析进 v；失败直接 t.Fatal。
func (r *Resp) DecodeData(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Data, v); err != nil {
		t.Fatalf("request: 解析 data 失败: %v, raw=%s", err, string(r.Data))
	}
}

// Identity 是模拟网关注入的登录身份（只放基础字段，保持低耦合）。
type Identity struct {
	AccountID int64
	Guid      string
}

type builder struct {
	id      Identity
	present bool
}

// Option 配置一次请求的可变部分。
type Option func(*builder)

// AsUser 以指定登录身份发起请求——模拟网关注入身份 header，绕过 token 查库。
func AsUser(id Identity) Option { return func(b *builder) { b.id = id; b.present = true } }

// Anonymous 显式表示无身份（不带任何 AsUser 时即为此默认）。
func Anonymous() Option { return func(b *builder) { b.present = false } }

// PostV1 对 SUT 的 v1 HTTP 入口发一次（签名）POST，payload 为业务负载。
// 签名 / 统一外壳组装细节略：真实项目按 SUT 的签名算法把 payload 装进 {timestamp,nonce,sign,params} 外壳。
func PostV1(t testing.TB, path string, payload any, opts ...Option) *Resp {
	t.Helper()
	var b builder
	for _, o := range opts {
		o(&b)
	}

	body, _ := json.Marshal(payload) // 真实项目此处套统一外壳 + 签名
	req, err := http.NewRequest(http.MethodPost, bootstrap.SUTHTTPBaseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: 构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if b.present {
		// 模拟网关注入身份：base64(json)，字段名与 SUT 解析侧对齐。
		auth, _ := json.Marshal(map[string]any{"account_id": b.id.AccountID, "guid": b.id.Guid})
		req.Header.Set("UserAuthInfo", base64.StdEncoding.EncodeToString(auth))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("request: POST %s 失败: %v", path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	out := &Resp{HTTPStatus: resp.StatusCode}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("request: 解析响应外壳失败: %v, raw=%s", err, string(raw))
	}
	return out
}
