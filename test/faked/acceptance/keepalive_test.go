package acceptance

import (
	"context"
	"io"
	stdhttp "net/http"
	"net/http/httptrace"
	"strings"
	"testing"

	fakedclient "github.com/McHarvvvy/mtkit/client/fake"
	"github.com/McHarvvvy/mtkit/test/faked/internal/testendpoint"
)

// TestKeepAlive_PlaintextReuse 验证明文 HTTP/1.1 keep-alive：同一 client 连发两个
// 带 body 的 POST，第二个请求复用第一个的 TCP 连接（GotConn.Reused==true）且都命中。
//
// 带 body 是关键——它同时验证 daemon 的 body drain：若 drain 不正确，残留 body 会让
// 第二个请求在 daemon 侧串包失败。httptrace 直接证明连接被复用，而非各开各的。
func TestKeepAlive_PlaintextReuse(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	key := caseKey(t)

	h.client.HTTP().
		On(testendpoint.HostAccTest, stdhttp.MethodPost, testendpoint.PathToken).
		RouteKey(key).
		Static(fakedclient.Resp().Status(200).Body([]byte("ok"))).
		Apply(t)

	cl := h.httpClient(t) // keep-alive 默认开启（helper 已不再 DisableKeepAlives）

	doReq := func(seq int) (reused bool) {
		trace := &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
		}
		ctx := httptrace.WithClientTrace(context.Background(), trace)
		// body 带额外填充，确保 daemon 必须 drain 才能正确读到下一请求边界。
		req, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodPost,
			"http://"+testendpoint.HostAccTest+testendpoint.PathToken,
			strings.NewReader("client_id="+key+"&pad=keepalive-body-padding"))
		if err != nil {
			t.Fatalf("build req %d: %v", seq, err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("req %d: %v", seq, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || string(body) != "ok" {
			t.Fatalf("req %d: status=%d body=%q", seq, resp.StatusCode, body)
		}
		return reused
	}

	if doReq(1) {
		t.Fatalf("first request should open a fresh connection, but reported reused")
	}
	if !doReq(2) {
		t.Fatalf("second request should reuse the keep-alive connection, but opened a new one")
	}
}
