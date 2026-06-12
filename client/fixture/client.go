package fixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

// defaultBlockSize 是每次远调 daemon 领取的 token 区块大小（HiLo 模式）：
// 每 65536 次 token 才远调一次，热路径是纯本地自增。
const defaultBlockSize = int64(65536)

// Client 是进程级 fixture 客户端，持有 blockSource 单例，通常按测试包 lazy 初始化一次。
type Client struct {
	src *blockSource
}

// New 创建进程级 fixture 客户端。addr、token 来自 RuntimeInfo.Addr / .Token，由调用方读 runtime 后显式传入。
func New(addr, token string) *Client {
	return &Client{
		src: &blockSource{
			size:  defaultBlockSize,
			kind:  protocol.KindToken,
			fetch: newHTTPFetcher(addr, token),
		},
	}
}

// NewScope 为单个测试用例创建 CaseScope，所有 scope 共享同一进程级 blockSource。
func (c *Client) NewScope(t testing.TB) *CaseScope {
	t.Helper()
	s := &CaseScope{t: t, src: c.src}
	t.Cleanup(s.cleanup)
	return s
}

// iTokenSource 仅包内使用，不进任何公共签名；纯逻辑单测可注入 trivial fake（见 newScopeWith）。
type iTokenSource interface {
	Next() (int64, error) // error 仅在基建故障时非 nil
}

// blockSource 是进程级单例：热路径在区块内本地自增，冷路径（每 65536 次一次）远调 daemon 补块。
type blockSource struct {
	mu    sync.Mutex
	next  int64
	end   int64
	size  int64
	kind  string
	fetch func(kind string, size int64) (start, end int64, err error)
}

// Next 返回下一个全局唯一 token。
//
// 注意：补块（fetch）是一次同步 HTTP 往返（超时 5s），且全程持 mu，期间阻塞进程内所有生成。
// 这条冷路径每 65536 次才走一次、且为本机回环，实践开销可忽略——但它不是“极短临界区”，勿据此假设无阻塞。
// 失败不重试：本机/CI 均为回环地址，daemon 不可达即基建故障，直接上抛由 CaseScope.next 终止用例。
func (b *blockSource) Next() (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.next >= b.end {
		start, end, err := b.fetch(b.kind, b.size)
		if err != nil {
			return 0, err
		}
		if end <= start {
			return 0, fmt.Errorf("fixture: invalid token block [%d,%d)", start, end)
		}
		b.next, b.end = start, end
	}
	v := b.next
	b.next++
	return v, nil
}

// newHTTPFetcher 返回向 daemon /v1/fixture/alloc 领取区块的 fetch 闭包。
func newHTTPFetcher(addr, token string) func(kind string, size int64) (int64, int64, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	return func(kind string, size int64) (int64, int64, error) {
		body, err := json.Marshal(protocol.AllocReq{Kind: kind, Size: size})
		if err != nil {
			return 0, 0, fmt.Errorf("fixture: marshal alloc request: %w", err)
		}
		req, err := http.NewRequest(http.MethodPost, "http://"+addr+protocol.PathAlloc, bytes.NewReader(body))
		if err != nil {
			return 0, 0, fmt.Errorf("fixture: build alloc request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set(protocol.HeaderToken, token)
		}

		resp, err := client.Do(req)
		if err != nil {
			return 0, 0, fmt.Errorf("fixture: alloc token block: %w", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, 0, fmt.Errorf("fixture: read alloc response: %w", err)
		}
		if resp.StatusCode >= 400 {
			// fixture handler 与 daemon withAuth（鉴权失败）共用 {code,message} 信封，故能统一解析出结构化 code。
			var apiErr protocol.ErrorResp
			if err := json.Unmarshal(raw, &apiErr); err == nil && apiErr.Code != "" {
				return 0, 0, fmt.Errorf("fixture: alloc failed: %s: %s", apiErr.Code, apiErr.Message)
			}
			return 0, 0, fmt.Errorf("fixture: alloc failed: http %d: %s", resp.StatusCode, string(raw))
		}
		var out protocol.AllocResp
		if err := json.Unmarshal(raw, &out); err != nil {
			return 0, 0, fmt.Errorf("fixture: decode alloc response: %w", err)
		}
		return out.Start, out.End, nil
	}
}

// newScopeWith 是包内构造函数，供单测注入 iTokenSource 绕过 HTTP 层，不暴露给公共 API。
func newScopeWith(t testing.TB, src iTokenSource) *CaseScope {
	return &CaseScope{t: t, src: src}
}
