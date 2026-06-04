package http

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	nethttp "net/http"
	"strings"
	"sync"
	"time"

	"github.com/McHarvvvy/mtkit/faked/http/ca"
)

// Proxy 是 faked HTTP 协议的 MITM 代理。
//
// 整个 daemon 只暴露一个监听地址（写入 runtime.json 的 http.proxy_addr），所有
// fake host 共享。SUT 通过 HTTP_PROXY / HTTPS_PROXY 把请求发到本代理。
//
// 实现要点：
//   - CONNECT 隧道：用 CA 动态签发叶证书，完成 TLS 握手并接管明文流。
//   - 明文 HTTP：按 Host header 直接路由。
//   - 强制 HTTP/1.1（TLS NextProtos 只通告 "http/1.1"），简化代理实现。
//   - 不支持 streaming / SSE / chunked transfer encoding：响应一次性写出。
type Proxy struct {
	listenAddr string
	engine     *ActionEngine
	routes     *RouteRegistry
	bundle     *ca.Bundle

	mu       sync.Mutex
	listener net.Listener
	muxes    map[string]*nethttp.ServeMux
	closed   chan struct{}
	closeOne sync.Once
	started  bool
}

// NewProxy 构造 Proxy；调用 Start 前路由 / engine / bundle 不会被使用。
func NewProxy(listenAddr string, engine *ActionEngine, routes *RouteRegistry, bundle *ca.Bundle) *Proxy {
	return &Proxy{
		listenAddr: listenAddr,
		engine:     engine,
		routes:     routes,
		bundle:     bundle,
	}
}

// Start 装配 host mux、监听 TCP 端口、后台异步接收请求。
// 返回监听地址（"host:port"）供 daemon 写入 runtime.json。
func (p *Proxy) Start() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return "", errors.New("http proxy already started")
	}
	p.muxes = p.buildMuxes()

	lis, err := net.Listen("tcp", p.listenAddr)
	if err != nil {
		return "", fmt.Errorf("http proxy listen %s: %w", p.listenAddr, err)
	}
	p.listener = lis
	p.closed = make(chan struct{})
	go p.serve()
	p.started = true
	return lis.Addr().String(), nil
}

// Stop 关闭监听器；已 Stop 多次幂等。
// 注意：未跟踪每个 in-flight 连接的关闭——MITM 代理是测试基础设施，进程退出会
// 把残留连接一并清理；额外跟踪反而增加复杂度。
func (p *Proxy) Stop() {
	p.closeOne.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.closed != nil {
			close(p.closed)
		}
		if p.listener != nil {
			_ = p.listener.Close()
		}
	})
}

// buildMuxes 为每个 host 创建一个 ServeMux，把 (method, path, RouteKey) 与
// engine.HandleHTTP 连起来。每个 handler 闭包捕获自己的 Route 副本，避免循环
// 变量陷阱。
func (p *Proxy) buildMuxes() map[string]*nethttp.ServeMux {
	out := make(map[string]*nethttp.ServeMux)
	for host, rts := range p.routes.RoutesByHost() {
		mux := nethttp.NewServeMux()
		for i := range rts {
			rt := rts[i]
			pattern := rt.Method + " " + rt.Path
			host := host // 显式 shadow，避免闭包捕获外层循环变量。
			mux.HandleFunc(pattern, func(w nethttp.ResponseWriter, r *nethttp.Request) {
				p.serveRoute(host, rt, w, r)
			})
		}
		out[host] = mux
	}
	return out
}

// serveRoute 在 mux 命中后调用：用 GenRouteKey 从 request 算出 route_key，
// 然后转交 ActionEngine.HandleHTTP。提取失败按未命中处理（502 + 调用日志），保持 fail loud。
func (p *Proxy) serveRoute(host string, rt Route, w nethttp.ResponseWriter, r *nethttp.Request) {
	routeKey, err := rt.GenRouteKey(r)
	if err != nil {
		// GenRouteKey 失败也是一种未命中——记调用日志，保持"所有 fake 请求都有日志"。
		p.engine.logMiss(time.Now().UTC(), Call{
			Endpoint: host,
			Method:   rt.Method,
			Path:     rt.Path,
		}, "routekey extract: "+err.Error())
		nethttp.Error(w, "routekey extract: "+err.Error(), nethttp.StatusBadGateway)
		return
	}
	_ = p.engine.HandleHTTP(Call{
		Endpoint: host,
		Method:   rt.Method,
		Path:     rt.Path,
		RouteKey: routeKey,
		W:        w,
		R:        r,
	})
}

// serve 是接收循环：每条 incoming 连接交给 handleConn 异步处理。
func (p *Proxy) serve() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-p.closed:
				return
			default:
			}
			// 监听器被关闭以外的错误亦停止接收（避免 100% CPU 空转）。
			return
		}
		go p.handleConn(conn)
	}
}

// handleConn 处理一条入站连接：CONNECT 走 HTTPS 隧道（隧道内自行处理 keep-alive），
// 其余按明文 HTTP/1.1 keep-alive 循环处理——复用同一 bufio.Reader 连读多个请求，
// 直到对端关闭、读失败或某个请求声明 Connection: close。
func (p *Proxy) handleConn(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		req, err := nethttp.ReadRequest(br)
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Time{})

		if req.Method == nethttp.MethodConnect {
			p.handleHTTPS(conn, req)
			return // 隧道接管连接，不再回到明文循环
		}
		if !p.servePlaintext(conn, req) {
			return
		}
	}
}

// servePlaintext 处理一个明文代理请求，返回连接是否应继续 keep-alive 复用。
//
// dispatch 之后必须 drain 请求 body：明文 keep-alive 复用同一 bufio.Reader，
// 残留的请求 body 会让下一次 ReadRequest 从 body 中间开始解析而串包。
// 决定不复用时，回 Connection: close 让客户端及时另起连接。
func (p *Proxy) servePlaintext(conn net.Conn, req *nethttp.Request) bool {
	host := hostOnly(req.Host)
	if host == "" && req.URL != nil {
		host = hostOnly(req.URL.Host)
	}
	req.RequestURI = ""
	req.URL.Scheme = ""
	req.URL.Host = ""

	keepAlive := shouldKeepAlive(req)
	w := newRespWriter(conn, req.ProtoMajor, req.ProtoMinor)
	if !keepAlive {
		w.Header().Set("Connection", "close")
	}
	p.dispatch(host, req, w)
	drainAndCloseBody(req)
	if err := w.flush(); err != nil {
		return false
	}
	return keepAlive
}

// drainAndCloseBody 读尽并关闭请求 body。
//
// keep-alive 复用连接的前提：每个请求的 body 必须被完全消费，否则下一个
// ReadRequest 会把残留 body 字节当成请求行解析。GenRouteKey（FormValue /
// JSONField）与 action 通常只读部分或完全不读 body，由本函数兜底清空。
func drainAndCloseBody(req *nethttp.Request) {
	if req.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, req.Body)
	_ = req.Body.Close()
}

// handleHTTPS 处理 CONNECT 隧道：回 200 表示隧道建立，再用 CA 动态签发叶证书
// 完成 TLS 握手并接管明文流。
//
// 隧道内可能复用连接发多次请求（HTTP keep-alive），循环读到失败或对端关闭。
func (p *Proxy) handleHTTPS(conn net.Conn, connectReq *nethttp.Request) {
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	tlsConn := tls.Server(conn, &tls.Config{
		GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hi.ServerName
			if name == "" {
				name = hostOnly(connectReq.Host)
			}
			return p.bundle.CertFor(name)
		},
		// 不通告 h2，强制客户端走 HTTP/1.1，简化代理实现。
		NextProtos: []string{"http/1.1"},
	})
	defer tlsConn.Close()

	_ = tlsConn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	_ = tlsConn.SetReadDeadline(time.Time{})

	br := bufio.NewReader(tlsConn)
	for {
		inner, err := nethttp.ReadRequest(br)
		if err != nil {
			return
		}
		host := hostOnly(connectReq.Host)
		inner.Host = host
		inner.RequestURI = ""

		keepAlive := shouldKeepAlive(inner)
		w := newRespWriter(tlsConn, inner.ProtoMajor, inner.ProtoMinor)
		if !keepAlive {
			w.Header().Set("Connection", "close")
		}
		p.dispatch(host, inner, w)
		// 与明文路径对称：复用同一 bufio.Reader 前 drain body，避免多请求串包。
		drainAndCloseBody(inner)
		if err := w.flush(); err != nil {
			return
		}
		if !keepAlive {
			return
		}
	}
}

// dispatch 按 host → mux → route 分发。所有未命中都记一条 matched=false 调用日志
// 并回 502，保持 fail loud + "所有 fake 请求都有调用日志"。
//
// 未命中分三类，统一收敛为 502（不再让 ServeMux 默认返回 404/405）：
//   - host 未注册；
//   - 路由未注册（method+path 组合不存在）；
//   - method 不符（path 注册了但 method 不对）。
//
// 后两类都对应 mux.Handler 返回空 pattern——Go ServeMux 对"路径未注册"与
// "method 不符"都返回 pattern==""，因此一个判断即可统一覆盖。
func (p *Proxy) dispatch(host string, req *nethttp.Request, w nethttp.ResponseWriter) {
	mux, ok := p.muxes[host]
	if !ok {
		p.engine.logMiss(time.Now().UTC(), Call{
			Endpoint: host,
			Method:   req.Method,
			Path:     req.URL.Path,
		}, "no fake registered for host: "+host)
		nethttp.Error(w, "no fake registered for host: "+host, nethttp.StatusBadGateway)
		return
	}
	if _, pattern := mux.Handler(req); pattern == "" {
		p.engine.logMiss(time.Now().UTC(), Call{
			Endpoint: host,
			Method:   req.Method,
			Path:     req.URL.Path,
		}, "no fake route for "+req.Method+" "+req.URL.Path)
		nethttp.Error(w, "no fake route for "+req.Method+" "+req.URL.Path, nethttp.StatusBadGateway)
		return
	}
	mux.ServeHTTP(w, req)
}

func hostOnly(hostport string) string {
	if hostport == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func shouldKeepAlive(req *nethttp.Request) bool {
	conn := strings.ToLower(req.Header.Get("Connection"))
	if conn == "close" {
		return false
	}
	if req.ProtoMajor == 1 && req.ProtoMinor == 0 && conn != "keep-alive" {
		return false
	}
	return true
}

// ─── 极简 ResponseWriter：把 HTTP 响应写到原始连接 ────────────────────────

// respWriter 在裸 net.Conn 上实现 http.ResponseWriter，避免引入完整的 http.Server
// 仅为内部分发使用。仅支持 HTTP/1.x；不支持 streaming / chunked。
type respWriter struct {
	conn        net.Conn
	header      nethttp.Header
	buf         []byte
	statusCode  int
	wroteHeader bool
	major       int
	minor       int
}

func newRespWriter(conn net.Conn, major, minor int) *respWriter {
	if major == 0 {
		major, minor = 1, 1
	}
	return &respWriter{
		conn:   conn,
		header: make(nethttp.Header),
		major:  major,
		minor:  minor,
	}
}

func (w *respWriter) Header() nethttp.Header { return w.header }

func (w *respWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.statusCode = statusCode
}

func (w *respWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(nethttp.StatusOK)
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

// flush 把累积的响应一次性写出。使用 Content-Length 而非 chunked，简化实现。
func (w *respWriter) flush() error {
	if !w.wroteHeader {
		w.WriteHeader(nethttp.StatusOK)
	}
	if w.header.Get("Content-Length") == "" {
		w.header.Set("Content-Length", fmt.Sprintf("%d", len(w.buf)))
	}
	if w.header.Get("Date") == "" {
		w.header.Set("Date", time.Now().UTC().Format(nethttp.TimeFormat))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/%d.%d %d %s\r\n", w.major, w.minor, w.statusCode, nethttp.StatusText(w.statusCode))
	if err := w.header.Write(headerWriter{&b}); err != nil {
		return err
	}
	b.WriteString("\r\n")

	if _, err := io.WriteString(w.conn, b.String()); err != nil {
		return err
	}
	if len(w.buf) > 0 {
		if _, err := w.conn.Write(w.buf); err != nil {
			return err
		}
	}
	return nil
}

// headerWriter 适配 http.Header.Write 期望的 io.Writer。
type headerWriter struct{ *strings.Builder }

func (h headerWriter) Write(p []byte) (int, error) {
	return h.Builder.Write(p)
}
