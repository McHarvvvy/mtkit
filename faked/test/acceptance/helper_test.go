// Package acceptance 是 faked 引擎的进程内 acceptance 测试。
//
// 形态：**整个测试包共用一个进程内 faked.Engine**（TestMain 构造，control 挂一个
// httptest server 模拟 daemon 主 mux，数据面 listener 全 :0）。go test 进程同时充当
// control client 与 SUT 角色——用真实 grpc.Dial / http.Transport 发请求。
//
// faked 内嵌后无独立 runtime 文件：引擎 Start 直接返回 Addrs（grpc 地址 / 代理地址 /
// CA / 调用日志路径），测试经 Addrs 拿全部地址，经 control server 的 URL 注册 rule。
//
// 并发隔离要点（共用 engine 下必须遵守）：
//   - 规则常驻、无覆盖/删除接口：每用例用 caseKey(t) 派生唯一 route_key。
//   - 调用日志是同一份文件：按唯一 route_key 扫描 jsonl 命中行（requireGRPCHit / requireHTTPHit）。
//   - 个别断言"无 route_key 的全局副作用"的用例改用 newIsolatedHarness 独占 engine。
//
// 不依赖任何业务 fake endpoint：endpoint 由 test/internal/testendpoint 提供，
// proto 由 test/internal/testpb 提供，CA 由引擎动态生成。
package acceptance

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	fakedclient "github.com/McHarvvvy/mtkit/client/fake"
	"github.com/McHarvvvy/mtkit/faked"
	fakedgrpc "github.com/McHarvvvy/mtkit/faked/grpc"
	fakedhttp "github.com/McHarvvvy/mtkit/faked/http"
	"github.com/McHarvvvy/mtkit/faked/test/internal/testendpoint"
	"github.com/McHarvvvy/mtkit/faked/test/internal/testpb"
)

// grpcEchoEnvKey 是测试用 gRPC endpoint 在 Addrs.GRPCEndpoints 中的索引 key。
const grpcEchoEnvKey = testendpoint.EnvKeyEcho

// 包级共用 engine 的句柄：由 TestMain 装配，所有 sharedHarness(t) 共享。
var (
	sharedRuntimeDir  string
	sharedAddrs       faked.Addrs
	sharedControlAddr string
	sharedClient      *fakedclient.Client
)

// TestMain 启动整个测试包共用的进程内引擎，跑完所有用例后停机。
func TestMain(m *testing.M) {
	code, err := runWithSharedEngine(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acceptance shared engine: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runWithSharedEngine(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "faked-acceptance-*")
	if err != nil {
		return 0, fmt.Errorf("mktemp: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	eng, addrs, srv, err := startEngine(dir)
	if err != nil {
		return 0, err
	}
	defer srv.Close()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = eng.Stop(ctx)
	}()

	sharedRuntimeDir = dir
	sharedAddrs = addrs
	sharedControlAddr = controlAddr(srv)
	sharedClient = fakedclient.New(sharedControlAddr)

	return m.Run(), nil
}

// startEngine 构造进程内 faked 引擎：control 挂 httptest server（模拟 daemon 主 mux），
// 数据面 :0、CA 动态生成。返回引擎、数据面地址、control server。
func startEngine(runtimeDir string) (*faked.Engine, faked.Addrs, *httptest.Server, error) {
	eng, err := faked.New(faked.Config{
		RuntimeDir:  runtimeDir,
		ProxyListen: "127.0.0.1:0",
		// CACertFile / CAKeyFile 留空：动态生成模式
	})
	if err != nil {
		return nil, faked.Addrs{}, nil, fmt.Errorf("new engine: %w", err)
	}
	mux := stdhttp.NewServeMux()
	eng.RegisterControl(mux)
	srv := httptest.NewServer(mux)
	addrs, err := eng.Start(context.Background())
	if err != nil {
		srv.Close()
		return nil, faked.Addrs{}, nil, fmt.Errorf("start engine: %w", err)
	}
	return eng, addrs, srv, nil
}

// controlAddr 取 httptest server 的 "host:port"（fakedclient.New 会拼 http:// 前缀与 path）。
func controlAddr(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://")
}

// harness 把 engine 的对外句柄（addrs / client / runtime 目录）打包给用例。
type harness struct {
	t          testing.TB
	runtimeDir string
	addrs      faked.Addrs
	client     *fakedclient.Client
}

// sharedHarness 返回指向包级共用 engine 的 harness——绝大多数用例用它。
func sharedHarness(t testing.TB) *harness {
	t.Helper()
	return &harness{
		t:          t,
		runtimeDir: sharedRuntimeDir,
		addrs:      sharedAddrs,
		client:     sharedClient,
	}
}

// newIsolatedHarness 为单个用例起一个独占进程内 engine（自带 runtime 目录与调用日志）。
// 仅供"断言无 route_key 的全局副作用、无法在共用日志里按 key 归因"的用例使用。
func newIsolatedHarness(t testing.TB) *harness {
	t.Helper()
	dir := t.TempDir()
	eng, addrs, srv, err := startEngine(dir)
	if err != nil {
		t.Fatalf("start isolated engine: %v", err)
	}
	t.Cleanup(func() {
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = eng.Stop(ctx)
	})
	return &harness{t: t, runtimeDir: dir, addrs: addrs, client: fakedclient.New(controlAddr(srv))}
}

// caseKeySeq 给 caseKey 提供进程内单调递增的后缀，保证同名测试重复执行不撞 RuleKey。
var caseKeySeq atomic.Uint64

// caseKey 派生本用例专属、跨用例且跨重复执行唯一的 route_key 基。
func caseKey(t testing.TB) string {
	name := strings.NewReplacer("/", "_", " ", "_", "#", "_").Replace(t.Name())
	return fmt.Sprintf("%s-%d", name, caseKeySeq.Add(1))
}

// dialEcho 建立到 echo gRPC endpoint 的客户端连接（连接在 t.Cleanup 关闭）。
func (h *harness) dialEcho(t testing.TB) testpb.EchoClient {
	t.Helper()
	addr := h.addrs.GRPCEndpoints[grpcEchoEnvKey]
	if addr == "" {
		t.Fatalf("addrs missing endpoint addr for %s", grpcEchoEnvKey)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial echo %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return testpb.NewEchoClient(conn)
}

// httpClient 返回一个走引擎 MITM 代理的 *http.Client；
// HTTPS 通过装填引擎动态生成的 CA 到 RootCAs 完成信任链（不依赖 SSL_CERT_FILE）。
func (h *harness) httpClient(t testing.TB) *stdhttp.Client {
	t.Helper()
	if h.addrs.ProxyAddr == "" {
		t.Fatalf("addrs missing proxy_addr")
	}
	proxyURL, err := url.Parse("http://" + h.addrs.ProxyAddr)
	if err != nil {
		t.Fatalf("parse proxy addr: %v", err)
	}
	pool := h.caPool(t)
	return &stdhttp.Client{
		Transport: &stdhttp.Transport{
			Proxy:           stdhttp.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
		Timeout: 5 * time.Second,
	}
}

// caPool 从 Addrs 暴露的 CA cert 路径构造 x509.CertPool。
func (h *harness) caPool(t testing.TB) *x509.CertPool {
	t.Helper()
	caPath := h.addrs.CACertFile
	if caPath == "" {
		t.Fatalf("addrs missing ca_cert_file")
	}
	raw, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("read ca cert: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		t.Fatalf("append ca cert from %s failed", caPath)
	}
	return pool
}

// ─── 调用日志内容级断言（按唯一 route_key 扫描 jsonl 命中行）─────────────────────────

// requireGRPCHit 断言 gRPC 调用日志中存在一行：matched==true 且 route_key==key；
// wantKind 非空时还要求 action_kind 匹配。
func (h *harness) requireGRPCHit(t testing.TB, routeKey, wantKind string) {
	t.Helper()
	h.requireGRPCEvent(t, fmt.Sprintf("grpc hit route_key=%q kind=%q", routeKey, wantKind),
		func(ev fakedgrpc.CallEvent) bool {
			if !ev.Matched || ev.RouteKey != routeKey {
				return false
			}
			return wantKind == "" || ev.ActionKind == wantKind
		})
}

// requireGRPCMiss 断言 gRPC 调用日志中存在一行：matched==false 且 route_key==key。
func (h *harness) requireGRPCMiss(t testing.TB, routeKey string) {
	t.Helper()
	h.requireGRPCEvent(t, fmt.Sprintf("grpc miss route_key=%q", routeKey),
		func(ev fakedgrpc.CallEvent) bool {
			return !ev.Matched && ev.RouteKey == routeKey
		})
}

// requireHTTPHit 断言 HTTP 调用日志中存在一行：matched==true、route_key==key，且 status==wantStatus。
func (h *harness) requireHTTPHit(t testing.TB, routeKey string, wantStatus int) {
	t.Helper()
	h.requireHTTPEvent(t, fmt.Sprintf("http hit route_key=%q status=%d", routeKey, wantStatus),
		func(ev fakedhttp.CallEvent) bool {
			return ev.Matched && ev.RouteKey == routeKey && ev.Status == wantStatus
		})
}

// requireHTTPMiss 断言 HTTP 调用日志中存在满足 match 的 matched==false 行。
func (h *harness) requireHTTPMiss(t testing.TB, desc string, match func(fakedhttp.CallEvent) bool) {
	t.Helper()
	h.requireHTTPEvent(t, "http miss "+desc, func(ev fakedhttp.CallEvent) bool {
		return !ev.Matched && match(ev)
	})
}

func (h *harness) requireGRPCEvent(t testing.TB, desc string, match func(fakedgrpc.CallEvent) bool) {
	t.Helper()
	if requireEventually(func() bool {
		for _, ev := range readGRPCCallEvents(h.addrs.GRPCCallLog) {
			if match(ev) {
				return true
			}
		}
		return false
	}) {
		return
	}
	t.Fatalf("expected call log to contain %s in %s, none found", desc, h.addrs.GRPCCallLog)
}

func (h *harness) requireHTTPEvent(t testing.TB, desc string, match func(fakedhttp.CallEvent) bool) {
	t.Helper()
	if requireEventually(func() bool {
		for _, ev := range readHTTPCallEvents(h.addrs.HTTPCallLog) {
			if match(ev) {
				return true
			}
		}
		return false
	}) {
		return
	}
	t.Fatalf("expected call log to contain %s in %s, none found", desc, h.addrs.HTTPCallLog)
}

// requireEventually 在 ~2s 内轮询 cond，命中返回 true。
func requireEventually(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readGRPCCallEvents(path string) []fakedgrpc.CallEvent {
	var out []fakedgrpc.CallEvent
	for _, line := range readJSONLLines(path) {
		var ev fakedgrpc.CallEvent
		if err := json.Unmarshal(line, &ev); err == nil {
			out = append(out, ev)
		}
	}
	return out
}

func readHTTPCallEvents(path string) []fakedhttp.CallEvent {
	var out []fakedhttp.CallEvent
	for _, line := range readJSONLLines(path) {
		var ev fakedhttp.CallEvent
		if err := json.Unmarshal(line, &ev); err == nil {
			out = append(out, ev)
		}
	}
	return out
}

func readJSONLLines(path string) [][]byte {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines [][]byte
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		lines = append(lines, []byte(l))
	}
	return lines
}

// ─── 编码 / 断言辅助 ──────────────────────────────────────────────────────

// mustEncodeProto 把 proto.Message marshal + base64，用于构造合法的 static_response data 字段。
func mustEncodeProto(t testing.TB, m proto.Message) string {
	t.Helper()
	raw, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("proto marshal: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// doForm 发一次 application/x-www-form-urlencoded POST 请求并返回响应。调用方负责关闭 resp.Body。
func doForm(t testing.TB, cl *stdhttp.Client, url, body string) *stdhttp.Response {
	t.Helper()
	req, err := stdhttp.NewRequest(stdhttp.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build form request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("POST form via proxy: %v", err)
	}
	return resp
}

// jsonRaw 把任意结构 json.Marshal 后包装成 json.RawMessage，用于直接构造 Action.Data 字段。
func jsonRaw(t testing.TB, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	return raw
}

// assertAPIError 验证 err 是 control API 返回的结构化错误，且 code / HTTP status 与契约一致。
func assertAPIError(t testing.TB, err error, wantCode string, wantStatus int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var apiErr *fakedclient.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *fakedclient.APIError, got %T: %v", err, err)
	}
	if apiErr.Code != wantCode || apiErr.HTTPStatus != wantStatus {
		t.Fatalf("error mismatch: want code=%s status=%d, got code=%s status=%d (message=%s)",
			wantCode, wantStatus, apiErr.Code, apiErr.HTTPStatus, apiErr.Message)
	}
}
