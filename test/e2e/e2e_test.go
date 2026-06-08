//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fakedclient "github.com/McHarvvvy/mtkit/client/fake"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
	"github.com/McHarvvvy/mtkit/test/e2e/internal/echoendpoint"
	"github.com/McHarvvvy/mtkit/test/e2e/internal/echopb"
)

// directClient 不走任何代理，专用于测试进程直连 SUT 控制端口（避免误用环境 HTTP_PROXY）。
var directClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}

// TestE2E1_FullStack 是全栈双线 smoke：真容器 + 真 SUT + faked，验证 F1/F6/F7 全链路。
//
// 闭包内：经 runtime 文件的 control 地址注册 gRPC/HTTPS 两条 fake 规则 → 触发真 SUT 依次
// ping Redis / dial 注入的 gRPC 直连地址 / 经注入的 HTTP_PROXY+CA 发 HTTPS → 校验三线皆通。
func TestE2E1_FullStack(t *testing.T) {
	requireDocker(t)
	name := uniqueName("e2e-full")
	dir := t.TempDir()
	ctrl := freePort(t)
	setupEnv(t, envOpts{
		runtimeDir: dir, container: "redis", containerName: name,
		sut: true, faked: true, sutControlAddr: ctrl, idleTTLSec: 30,
	})
	t.Cleanup(func() {
		cleanup(name, dir)
		_ = os.RemoveAll(filepath.Join(sutSrcDir, ".mtkit-build"))
	})

	var pingOut, grpcOut, httpsOut string
	code := mustBuildKit(t).Run(fakeRunnable{fn: func() int {
		fc := fakedclient.New(controlAddr(t, dir))
		key := "e2e-key"

		// gRPC 直连线规则。
		fc.GRPC().
			On(echoendpoint.EndpointEcho, echopb.Echo_Ping_FullMethodName).
			RouteKey(key).
			Static(&echopb.PingRes{Code: 7, Msg: "grpc-ok"}).
			Apply(t)
		// HTTP 代理 + CA 线规则。
		fc.HTTP().
			On(echopb.Host, http.MethodPost, echopb.PathLogin).
			RouteKey(key).
			Static(fakedclient.Resp().Status(200).Header("Content-Type", "application/json").Body([]byte(`{"token":"https-ok"}`))).
			Apply(t)

		// 触发真 SUT 执行三线调用。
		pingOut = sutTrigger(t, ctrl, "/ping-redis", "")
		grpcOut = sutTrigger(t, ctrl, "/call-grpc", key)
		httpsOut = sutTrigger(t, ctrl, "/call-https", key)

		if pingOut == "ok" && strings.Contains(grpcOut, "grpc-ok") && strings.Contains(httpsOut, "https-ok") {
			return 0
		}
		return 1
	}})

	if code != 0 {
		t.Fatalf("全栈 smoke 失败 code=%d\n  redis ping = %q\n  grpc       = %q\n  https      = %q", code, pingOut, grpcOut, httpsOut)
	}
}

// TestE2E2_ExitCodePassthrough 验证退出码端到端透传（F1）。无需 Docker（noop 容器）。
func TestE2E2_ExitCodePassthrough(t *testing.T) {
	for _, want := range []int{0, 1} {
		want := want
		t.Run(fmt.Sprintf("return-%d", want), func(t *testing.T) {
			dir := t.TempDir()
			setupEnv(t, envOpts{
				runtimeDir: dir, container: "noop", containerName: uniqueName("e2e-noop"), idleTTLSec: 5,
			})
			got := mustBuildKit(t).Run(fakeRunnable{fn: func() int { return want }})
			if got != want {
				t.Fatalf("Run 应透传退出码 %d，得 %d", want, got)
			}
		})
	}
}

// TestE2E3_Reuse 验证连续独立 Run 跨进程复用同一 daemon 与同一容器（F4）。
func TestE2E3_Reuse(t *testing.T) {
	requireDocker(t)
	name := uniqueName("e2e-reuse")
	dir := t.TempDir()
	setupEnv(t, envOpts{runtimeDir: dir, container: "redis", containerName: name, idleTTLSec: 30})
	t.Cleanup(func() { cleanup(name, dir) })

	var id1, id2 string
	var pid1, pid2 int
	mustBuildKit(t).Run(fakeRunnable{fn: func() int {
		id1 = containerIDByName(t, name)
		pid1 = daemonPID(t, dir)
		return 0
	}})
	mustBuildKit(t).Run(fakeRunnable{fn: func() int {
		id2 = containerIDByName(t, name)
		pid2 = daemonPID(t, dir)
		return 0
	}})

	if id1 == "" || id1 != id2 {
		t.Fatalf("应复用同一容器：id1=%q id2=%q", id1, id2)
	}
	if pid1 == 0 || pid1 != pid2 {
		t.Fatalf("应复用同一 daemon：pid1=%d pid2=%d", pid1, pid2)
	}
}

// TestE2E4_IdleCleanup 验证 idle 自动退出 + 真容器/runtime 清理、无孤儿（F5）。
func TestE2E4_IdleCleanup(t *testing.T) {
	requireDocker(t)
	name := uniqueName("e2e-idle")
	dir := t.TempDir()
	setupEnv(t, envOpts{runtimeDir: dir, container: "redis", containerName: name, idleTTLSec: 1})
	t.Cleanup(func() { cleanup(name, dir) })

	var pid int
	mustBuildKit(t).Run(fakeRunnable{fn: func() int {
		pid = daemonPID(t, dir)
		return 0
	}})

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		_, statErr := os.Stat(runtimeFile(dir))
		if os.IsNotExist(statErr) && !containerExists(name) && !processAlive(pid) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("idle 清理超时：runtime 文件 / 容器 / daemon 进程之一仍在")
}

// ─── 用例内工具 ─────────────────────────────────────────────────────────────

func controlAddr(t *testing.T, dir string) string {
	t.Helper()
	info, err := mtruntime.Read(runtimeFile(dir))
	if err != nil {
		t.Fatalf("read runtime for control addr: %v", err)
	}
	return info.Addr
}

func sutTrigger(t *testing.T, ctrlAddr, path, key string) string {
	t.Helper()
	url := "http://" + ctrlAddr + path
	if key != "" {
		url += "?key=" + key
	}
	resp, err := directClient.Get(url)
	if err != nil {
		t.Fatalf("trigger SUT %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
