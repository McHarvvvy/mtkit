// Command sut 是 e2e 用的真 SUT 替身：由 mtkit 框架从源码构建并托管，启动后读 daemon
// 注入的连接 env，暴露触发端口供测试驱动它对各依赖发起真实调用。
//
// 它故意覆盖三条注入链路：
//   - 容器：读 REDIS_ADDR ping 真 Redis（容器连接信息注入）。
//   - faked gRPC：读 E2E_ECHO_GRPC_ADDR 直连 dial（gRPC 直连地址注入）。
//   - faked HTTP：经注入的 HTTP_PROXY + MTKIT_CA_CERT_FILE 对 acc.e2e.faked 发 HTTPS
//     （代理 + CA 注入链；CA 由同目录 mtkit_ca_addon.go 装入 RootCAs）。
package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/McHarvvvy/mtkit/test/e2e/internal/echopb"
)

func main() {
	ctrlAddr := os.Getenv("SUT_CONTROL_ADDR")
	if ctrlAddr == "" {
		fmt.Fprintln(os.Stderr, "sut: SUT_CONTROL_ADDR is required")
		os.Exit(2)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ping-redis", func(w http.ResponseWriter, _ *http.Request) {
		if err := pingRedis(os.Getenv("REDIS_ADDR")); err != nil {
			fmt.Fprintf(w, "fail:%v", err)
			return
		}
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/call-grpc", func(w http.ResponseWriter, r *http.Request) {
		msg, err := callGRPC(os.Getenv(echopb.EnvKeyEchoGRPC), r.URL.Query().Get("key"))
		if err != nil {
			fmt.Fprintf(w, "fail:%v", err)
			return
		}
		fmt.Fprintf(w, "ok:%s", msg)
	})
	mux.HandleFunc("/call-https", func(w http.ResponseWriter, r *http.Request) {
		status, body, err := callHTTPS(r.URL.Query().Get("key"))
		if err != nil {
			fmt.Fprintf(w, "fail:%v", err)
			return
		}
		fmt.Fprintf(w, "status=%d body=%s", status, body)
	})

	ln, err := net.Listen("tcp", ctrlAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sut: listen %s: %v\n", ctrlAddr, err)
		os.Exit(3)
	}
	_ = http.Serve(ln, mux)
}

// pingRedis 用裸 RESP 发一次 PING，验证容器连接 env 注入且 Redis 可达。
func pingRedis(addr string) error {
	if addr == "" {
		return fmt.Errorf("empty REDIS_ADDR")
	}
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = conn.Write([]byte("*1\r\n$4\r\nPING\r\n")); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != "+PONG" {
		return fmt.Errorf("unexpected redis reply: %q", line)
	}
	return nil
}

// callGRPC 直连注入的 fake gRPC 地址（明文），调用 Echo.Ping，返回响应 Msg。
func callGRPC(addr, key string) (string, error) {
	if addr == "" {
		return "", fmt.Errorf("empty %s", echopb.EnvKeyEchoGRPC)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := echopb.NewEchoClient(conn).Ping(ctx, &echopb.PingReq{Key: key})
	if err != nil {
		return "", err
	}
	return res.GetMsg(), nil
}

// callHTTPS 经注入的 HTTP_PROXY + CA（由 mtkit_ca_addon.go 装入 RootCAs）对 faked 发 HTTPS。
// 用 http.DefaultClient：DefaultTransport 默认 Proxy=ProxyFromEnvironment、RootCAs 由 addon 覆盖。
func callHTTPS(key string) (int, string, error) {
	url := "https://" + echopb.Host + echopb.PathLogin
	body := strings.NewReader(fmt.Sprintf(`{"account_id":%q}`, key))
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n]), nil
}
