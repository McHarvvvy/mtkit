package acceptance

import (
	"fmt"
	"io"
	stdhttp "net/http"
	"strings"
	"testing"

	fakedclient "github.com/McHarvvvy/mtkit/client/fake"
	"github.com/McHarvvvy/mtkit/test/faked/internal/testendpoint"
)

// TestDispatch_HTTPStaticHappy 是 TC-02：HTTP 注册-命中-日志 三角验证。
//
// 主路径走 application/json——SUT 主流交互格式，route_key 从 JSON 字段 account_id
// 解出（PathLogin）。form-urlencoded 作为次要格式并列覆盖（PathToken，client_id）。
func TestDispatch_HTTPStaticHappy(t *testing.T) {
	t.Parallel()

	t.Run("json", func(t *testing.T) {
		t.Parallel()
		h := sharedHarness(t)
		key := caseKey(t)
		wantBody := []byte(`{"token":"t1"}`)

		h.client.HTTP().
			On(testendpoint.HostAccTest, stdhttp.MethodPost, testendpoint.PathLogin).
			RouteKey(key).
			Static(fakedclient.Resp().
				Status(200).
				Header("Content-Type", "application/json").
				Body(wantBody)).
			Apply(t)

		cl := h.httpClient(t)
		resp := doJSON(t, cl, "http://"+testendpoint.HostAccTest+testendpoint.PathLogin,
			fmt.Sprintf(`{"account_id":%q}`, key))
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != 200 {
			t.Fatalf("status: want 200 got %d", resp.StatusCode)
		}
		if string(body) != string(wantBody) {
			t.Fatalf("body mismatch: want %q got %q", wantBody, body)
		}
		if got := resp.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type: want application/json got %q", got)
		}
		h.requireHTTPHit(t, key, 200)
	})

	t.Run("form", func(t *testing.T) {
		t.Parallel()
		h := sharedHarness(t)
		key := caseKey(t)
		wantBody := []byte("ok")

		h.client.HTTP().
			On(testendpoint.HostAccTest, stdhttp.MethodPost, testendpoint.PathToken).
			RouteKey(key).
			Static(fakedclient.Resp().Status(200).Body(wantBody)).
			Apply(t)

		cl := h.httpClient(t)
		resp := doForm(t, cl, "http://"+testendpoint.HostAccTest+testendpoint.PathToken,
			"client_id="+key+"&grant_type=client_credentials")
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != 200 || string(body) != string(wantBody) {
			t.Fatalf("form resp: status=%d body=%q", resp.StatusCode, body)
		}
		h.requireHTTPHit(t, key, 200)
	})
}

// TestDispatch_HTTPSStaticHappy 是 TC-03：HTTPS MITM 命中（application/json 主路径）。
//
// 与 TC-02 json 唯一差别是 URL scheme：客户端通过 daemon 代理建 CONNECT 隧道、握
// TLS。CA 信任链通过 harness.httpClient 装填的 RootCAs 实现（不依赖 SSL_CERT_FILE）。
func TestDispatch_HTTPSStaticHappy(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	key := caseKey(t)

	wantBody := []byte(`{"token":"https-t1"}`)
	h.client.HTTP().
		On(testendpoint.HostAccTest, stdhttp.MethodPost, testendpoint.PathLogin).
		RouteKey(key).
		Static(fakedclient.Resp().
			Status(200).
			Header("Content-Type", "application/json").
			Body(wantBody)).
		Apply(t)

	cl := h.httpClient(t)
	resp := doJSON(t, cl, "https://"+testendpoint.HostAccTest+testendpoint.PathLogin,
		fmt.Sprintf(`{"account_id":%q}`, key))
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("https status: want 200 got %d", resp.StatusCode)
	}
	if string(body) != string(wantBody) {
		t.Fatalf("https body mismatch: want %q got %q", wantBody, body)
	}
	h.requireHTTPHit(t, key, 200)
}

// doJSON 发一次 application/json POST 请求并返回响应；构造/传输失败 t.Fatal。
func doJSON(t testing.TB, cl *stdhttp.Client, url, body string) *stdhttp.Response {
	t.Helper()
	req, err := stdhttp.NewRequest(stdhttp.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build json request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("POST json via proxy: %v", err)
	}
	return resp
}
