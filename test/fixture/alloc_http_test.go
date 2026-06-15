// Package fixture_test 是 fixture 模块的组件/集成测试：经真实 HTTP（httptest loopback）与真实 OS 文件锁
// （含跨进程）验证对外契约。**真实容器 / 真实 re-exec daemon 的 e2e 冒烟在 test/e2e/**（受 build tag e2e 保护）；
// 纯函数与需 unexported 的白盒单测留在 client/fixture、fixtured 原包。
package fixture_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	fixture "github.com/McHarvvvy/mtkit/client/fixture"
	"github.com/McHarvvvy/mtkit/fixtured"
	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

// newAllocServer 起一个真实 httptest server，把 fixtured 发号路由挂上去。
// wrap 非 nil 时用作鉴权中间件（等价 daemon withAuth）；返回 base URL。
func newAllocServer(t *testing.T, wrap func(http.HandlerFunc) http.HandlerFunc) string {
	t.Helper()
	srv := fixtured.New()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux, wrap)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL
}

// requireToken 是测试用鉴权中间件：头部 token 不等于 want 则 401，否则放行。
func requireToken(want string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(protocol.HeaderToken) != want {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
}

// TestAllocHTTPHappyPath 守护"发号端点经真实 HTTP + 正确鉴权能正常发出半开区间"这条主路径（A1/A2）。
//
// 验证手段：起真 httptest server（带要求 token=secret 的鉴权 wrap），带正确 token POST 一个 size=3
// 的请求，断言 200 且返回 [0,3)。这是 wire 契约的端到端 happy 验证，区别于纯内存的 Allocator 单测。
func TestAllocHTTPHappyPath(t *testing.T) {
	base := newAllocServer(t, requireToken("secret"))

	body, err := json.Marshal(protocol.AllocReq{Kind: protocol.KindToken, Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+protocol.PathAlloc, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(protocol.HeaderToken, "secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out protocol.AllocResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Start != 0 || out.End != 3 {
		t.Fatalf("alloc response = [%d,%d), want [0,3)", out.Start, out.End)
	}
}

// TestAllocHTTPRejectsBadRequests 守护"非法请求被结构化拒绝"契约（A3）。
//
// 验证手段：对每类非法输入发真实 HTTP 请求，断言 HTTP 状态码 + wire 层结构化 code。code 定义在
// protocol 包、是 client 据以分类错误的对外契约，故断言它不算"咬内部字符串"。size 超上界用一个
// 明显过大的值（不绑定 maxAllocSize 的确切阈值，避免改阈值即假挂——只验"过大被拒"这一行为）。
func TestAllocHTTPRejectsBadRequests(t *testing.T) {
	base := newAllocServer(t, nil)

	cases := []struct {
		name       string
		method     string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"非 POST 方法", http.MethodGet, "", http.StatusMethodNotAllowed, protocol.ErrCodeBadRequest},
		{"坏 JSON", http.MethodPost, "{not json", http.StatusBadRequest, protocol.ErrCodeBadRequest},
		{"不支持的 kind", http.MethodPost, `{"kind":"nope","size":10}`, http.StatusBadRequest, protocol.ErrCodeInvalidKind},
		{"size 非正", http.MethodPost, `{"kind":"token","size":0}`, http.StatusBadRequest, protocol.ErrCodeInvalidSize},
		{"size 远超上界", http.MethodPost, `{"kind":"token","size":1073741824}`, http.StatusBadRequest, protocol.ErrCodeInvalidSize},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(c.method, base+protocol.PathAlloc, strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, c.wantStatus)
			}
			var out protocol.ErrorResp
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
			if out.Code != c.wantCode {
				t.Fatalf("error code = %q, want %q", out.Code, c.wantCode)
			}
		})
	}
}

// TestAllocHTTPRequiresAuth 守护"发号需 daemon 鉴权"这条横切契约（A2）：
// 缺 token / 错 token 一律被拒（正确 token 放行由 TestAllocHTTPHappyPath 覆盖）。
//
// 验证手段：经要求 token=secret 的鉴权 wrap，分别用空 token 与错 token 发合法请求，断言 401。
func TestAllocHTTPRequiresAuth(t *testing.T) {
	base := newAllocServer(t, requireToken("secret"))

	body, err := json.Marshal(protocol.AllocReq{Kind: protocol.KindToken, Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range []string{"", "wrong"} {
		req, err := http.NewRequest(http.MethodPost, base+protocol.PathAlloc, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if tk != "" {
			req.Header.Set(protocol.HeaderToken, tk)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q: status = %d, want 401", tk, resp.StatusCode)
		}
	}
}

// TestClientGeneratesUniqueOverRealHTTP 是生成方向的**进程内组件级联测**（B1/B3）：经真实 HTTP（client
// blockSource → 真 fixtured.Server）打通"取号→permute→编码"整条管线，验证产出唯一、格式合法。
//
// 注意定位：这里用 httptest（同进程 loopback）+ 手挂 mux，**不是 e2e**——它绕过了真实跨进程 daemon 与真实
// withAuth 鉴权。发号方向的**真实环境 smoke**是 test/e2e 的 TestFixtureAllocE2E（经真 re-exec daemon + 真 token）。
//
// 验证手段：用 client.New + NewScope 经真 httptest server 连取 5000 个 UUIDNoDash，断言两两互异、
// 长度恰 32、全为小写 hex。断言用生成时拿到的值自比，不写任何字面量（token 分配跨运行非确定）。
func TestClientGeneratesUniqueOverRealHTTP(t *testing.T) {
	base := newAllocServer(t, nil)
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	client := fixture.New(u.Host, "")
	scope := client.NewScope(t)

	const n = 5000
	seen := make(map[string]bool, n)
	for i := range n {
		v := scope.UUIDNoDash()
		if len(v) != 32 {
			t.Fatalf("UUIDNoDash 长度 = %d, want 32: %q", len(v), v)
		}
		for _, ch := range v {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
				t.Fatalf("非 hex 字符 %q in %q", ch, v)
			}
		}
		if seen[v] {
			t.Fatalf("第 %d 个出现重复值 %q", i, v)
		}
		seen[v] = true
	}
	if len(seen) != n {
		t.Fatalf("distinct = %d, want %d", len(seen), n)
	}
}
