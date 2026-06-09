package orchestrator

import (
	"net/http"
	"os"
	"slices"
	"testing"

	"github.com/McHarvvvy/mtkit/container"
	"github.com/McHarvvvy/mtkit/orchestrator/protocol"
)

// TestOR01_AcquireBringsInfraReady 验证 acquire 串联基础设施就绪后才返回租约（F1）。
//
// Given 单个 fake 容器；When acquire；Then 返回非空 leaseID，且 state 显示
// InfraReady=true、Containers 含注册名。
func TestOR01_AcquireBringsInfraReady(t *testing.T) {
	reg, _ := oneFakeReg("redis")
	h := newHarness(t, []container.ContainerRegistration{reg}, nil)

	leaseID := h.acquire()
	if leaseID == "" {
		t.Fatal("期望非空 leaseID")
	}

	st := h.state()
	if !st.InfraReady {
		t.Fatal("acquire 后 state.InfraReady 应为 true")
	}
	if !containsStr(st.Containers, "redis") {
		t.Fatalf("state.Containers 应含 redis，得 %v", st.Containers)
	}
}

// TestOR02_TokenAuth 验证 token 鉴权对**任一** lease 接口一致生效（F2/横切）。
func TestOR02_TokenAuth(t *testing.T) {
	reg, _ := oneFakeReg("redis")
	h := newHarness(t, []container.ContainerRegistration{reg}, nil)

	// withAuth 包住全部 lease 路由——逐一断言无 token 一律 401（不只 acquire）。
	endpoints := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"acquire", http.MethodPost, protocol.PathAcquire, protocol.AcquireReq{PID: os.Getpid()}},
		{"heartbeat", http.MethodPost, protocol.PathHeartbeat, protocol.HeartbeatReq{LeaseID: "x"}},
		{"release", http.MethodPost, protocol.PathRelease, protocol.ReleaseReq{LeaseID: "x"}},
		{"state", http.MethodGet, protocol.PathState, nil},
	}
	for _, ep := range endpoints {
		t.Run("missing token -> 401: "+ep.name, func(t *testing.T) {
			resp, _ := h.doToken(ep.method, ep.path, ep.body, "")
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s 无 token 期望 401，得 %d", ep.name, resp.StatusCode)
			}
		})
	}

	body := protocol.AcquireReq{PID: os.Getpid()}
	t.Run("wrong token -> 401", func(t *testing.T) {
		resp, _ := h.doToken(http.MethodPost, protocol.PathAcquire, body, "wrong-token")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("错 token 期望 401，得 %d", resp.StatusCode)
		}
	})
	t.Run("correct token -> 200", func(t *testing.T) {
		resp, _ := h.doToken(http.MethodPost, protocol.PathAcquire, body, h.token)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("对 token 期望 200，得 %d", resp.StatusCode)
		}
	})
}

// TestOR03_MethodConstraints 验证 HTTP 方法约束。
func TestOR03_MethodConstraints(t *testing.T) {
	reg, _ := oneFakeReg("redis")
	h := newHarness(t, []container.ContainerRegistration{reg}, nil)

	t.Run("acquire with GET -> 405", func(t *testing.T) {
		resp, _ := h.do(http.MethodGet, protocol.PathAcquire, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET acquire 期望 405，得 %d", resp.StatusCode)
		}
	})
	t.Run("state with POST -> 405", func(t *testing.T) {
		resp, _ := h.do(http.MethodPost, protocol.PathState, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST state 期望 405，得 %d", resp.StatusCode)
		}
	})
}

// TestOR04_BadRequestBody 验证非法请求体被拒（F2）。
func TestOR04_BadRequestBody(t *testing.T) {
	reg, _ := oneFakeReg("redis")
	h := newHarness(t, []container.ContainerRegistration{reg}, nil)

	// 直接发非法 JSON（原样字符串，绕过 json.Marshal 校验）。
	resp, _ := h.doToken(http.MethodPost, protocol.PathAcquire, `{not-json`, h.token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 body 期望 400，得 %d", resp.StatusCode)
	}
}

// TestOR05_UnknownLease 验证对不存在租约的 heartbeat/release 返回 404。
func TestOR05_UnknownLease(t *testing.T) {
	reg, _ := oneFakeReg("redis")
	h := newHarness(t, []container.ContainerRegistration{reg}, nil)

	t.Run("heartbeat unknown -> 404", func(t *testing.T) {
		resp, _ := h.do(http.MethodPost, protocol.PathHeartbeat, protocol.HeartbeatReq{LeaseID: "lease-does-not-exist"})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("未知租约 heartbeat 期望 404，得 %d", resp.StatusCode)
		}
	})
	t.Run("release unknown -> 404", func(t *testing.T) {
		resp, _ := h.do(http.MethodPost, protocol.PathRelease, protocol.ReleaseReq{LeaseID: "lease-does-not-exist"})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("未知租约 release 期望 404，得 %d", resp.StatusCode)
		}
	})
}

// TestOR06_ReleasedLeaseInvalidated 验证释放后该租约失效（生命周期）。
//
// Given 已 acquire 的 lease；When release 后再 heartbeat 同一 lease；Then 404。
func TestOR06_ReleasedLeaseInvalidated(t *testing.T) {
	reg, _ := oneFakeReg("redis")
	h := newHarness(t, []container.ContainerRegistration{reg}, nil)

	leaseID := h.acquire()

	resp, raw := h.do(http.MethodPost, protocol.PathRelease, protocol.ReleaseReq{LeaseID: leaseID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("release 期望 200，得 %d body=%s", resp.StatusCode, raw)
	}

	resp, _ = h.do(http.MethodPost, protocol.PathHeartbeat, protocol.HeartbeatReq{LeaseID: leaseID})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("已释放租约 heartbeat 期望 404，得 %d", resp.StatusCode)
	}
}

// TestOR11_ReuseIdempotent 验证同 daemon 重复 acquire 复用既有基础设施（F1）。
//
// 不咬"Start 被调几次"（机制）；只断言两次 acquire 都成功、容器集合不变。
func TestOR11_ReuseIdempotent(t *testing.T) {
	reg, _ := oneFakeReg("redis")
	h := newHarness(t, []container.ContainerRegistration{reg}, nil)

	_ = h.acquire()
	first := h.state().Containers

	_ = h.acquire()
	second := h.state().Containers

	if !equalStrSet(first, second) {
		t.Fatalf("两次 acquire 容器集合应一致：first=%v second=%v", first, second)
	}
}

// ─── 小工具 ───────────────────────────────────────────────────────────────

func containsStr(ss []string, want string) bool {
	return slices.Contains(ss, want)
}

func equalStrSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
